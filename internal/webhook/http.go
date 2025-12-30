package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"go.miloapis.com/email-provider-loops/pkg/loops"
	notificationmiloapiscomv1alpha1 "go.miloapis.com/milo/pkg/apis/notification/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Webhook struct {
	Handler       Handler
	Endpoint      string
	signingSecret string // Loops signing secret for webhook verification
}

type Request struct {
	MailingListSubscribedEvent   *loops.MailingListSubscribedEvent
	MailingListUnsubscribedEvent *loops.MailingListUnsubscribedEvent
	BaseEvent                    *loops.WebhookEvent
}

type Response struct {
	HttpStatus int `json:"HttpStatus"`
}

type HandlerFunc func(context.Context, Request) Response

func (f HandlerFunc) Handle(ctx context.Context, req Request) Response {
	return f(ctx, req)
}

type Handler interface {
	Handle(context.Context, Request) Response
}

// WebhookVerificationError represents errors that can occur during webhook verification
type WebhookVerificationError struct {
	Code    string
	Message string
	Err     error
}

func (e *WebhookVerificationError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Err)
	}
	return e.Message
}

// Webhook verification error codes
var (
	ErrMissingHeaders     = errors.New("missing required webhook header")
	ErrMissingSecret      = errors.New("missing LOOPS_SIGNING_SECRET environment variable")
	ErrInvalidSignature   = errors.New("invalid signature")
	ErrVerificationFailed = errors.New("webhook verification failed")
)

// verifyWebhook verifies the webhook signature from Loops
func verifyWebhook(r *http.Request, body []byte, secret string) error {
	// Get the webhook-related headers
	eventID := r.Header.Get("webhook-id")
	timestamp := r.Header.Get("webhook-timestamp")
	webhookSignature := r.Header.Get("webhook-signature")

	// Verify required headers are present
	if eventID == "" || timestamp == "" || webhookSignature == "" {
		return &WebhookVerificationError{
			Code:    "MISSING_HEADERS",
			Message: "Missing required webhook header",
			Err:     ErrMissingHeaders,
		}
	}

	// Create signed content
	signedContent := fmt.Sprintf("%s.%s.%s", eventID, timestamp, string(body))

	// Extract the base64-encoded secret (after the prefix)
	parts := strings.Split(secret, "_")
	if len(parts) < 2 {
		return &WebhookVerificationError{
			Code:    "INVALID_SECRET_FORMAT",
			Message: "Invalid LOOPS_SIGNING_SECRET format",
			Err:     ErrMissingSecret,
		}
	}

	secretBytes, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return &WebhookVerificationError{
			Code:    "INVALID_SECRET_ENCODING",
			Message: "Failed to decode LOOPS_SIGNING_SECRET",
			Err:     err,
		}
	}

	// Create HMAC-SHA256 signature
	h := hmac.New(sha256.New, secretBytes)
	h.Write([]byte(signedContent))
	signature := base64.StdEncoding.EncodeToString(h.Sum(nil))

	// Check if the signature matches
	// The webhook-signature header contains space-separated signatures
	signatureFound := false
	for _, sig := range strings.Split(webhookSignature, " ") {
		// Each signature is in the format "v1,<signature>"
		if strings.Contains(sig, ","+signature) {
			signatureFound = true
			break
		}
	}

	if !signatureFound {
		return &WebhookVerificationError{
			Code:    "INVALID_SIGNATURE",
			Message: "Invalid signature",
			Err:     ErrInvalidSignature,
		}
	}

	return nil
}

const (
	contactStatusProviderIDIndexKey = "contact-status-providerID"
	groupProviderIDIndexKey         = "group-providerID"
	groupMembershipRemovalIndexKey  = "group-membership-removal"
)

func buildGroupMembershipRemovalIndexKey(contactRef *notificationmiloapiscomv1alpha1.ContactReference, groupRef *notificationmiloapiscomv1alpha1.ContactGroupReference) string {
	return fmt.Sprintf("%s-%s-%s-%s", contactRef.Name, contactRef.Namespace, groupRef.Name, groupRef.Namespace)
}

// setupIndexes sets up the required field indexes for webhook operations
func setupIndexes(mgr ctrl.Manager) error {
	// Index Contact objects by .status.providerID so that the webhook handler can
	// quickly look them up when processing incoming Loops webhook events.
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(),
		&notificationmiloapiscomv1alpha1.Contact{},
		contactStatusProviderIDIndexKey,
		func(rawObj client.Object) []string {
			contact := rawObj.(*notificationmiloapiscomv1alpha1.Contact)
			if contact.UID == "" {
				return nil
			}
			return []string{string(contact.UID)}
		},
	); err != nil {
		return fmt.Errorf("failed to create contact index for providerID: %w", err)
	}

	// Index ContactGroup objects by .spec.providers.loops.providerID so that the webhook handler can
	// quickly look them up when processing incoming Loops webhook events.
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(),
		&notificationmiloapiscomv1alpha1.ContactGroup{},
		groupProviderIDIndexKey,
		func(rawObj client.Object) []string {
			group := rawObj.(*notificationmiloapiscomv1alpha1.ContactGroup)
			for _, provider := range group.Spec.Providers {
				if provider.Name == "Loops" {
					return []string{provider.ID}
				}
			}
			return nil
		},
	); err != nil {
		return fmt.Errorf("failed to create contact index for providerID: %w", err)
	}

	// Index ContactGroup objects by .spec.providers.loops.providerID so that the webhook handler can
	// quickly look them up when processing incoming Loops webhook events.
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(),
		&notificationmiloapiscomv1alpha1.ContactGroupMembershipRemoval{},
		groupMembershipRemovalIndexKey,
		func(rawObj client.Object) []string {
			removal := rawObj.(*notificationmiloapiscomv1alpha1.ContactGroupMembershipRemoval)
			return []string{buildGroupMembershipRemovalIndexKey(&removal.Spec.ContactRef, &removal.Spec.ContactGroupRef)}
		},
	); err != nil {
		return fmt.Errorf("failed to create contact index for providerID: %w", err)
	}

	return nil
}

// SetupWithManager sets up the webhook with the Manager
func (w *Webhook) SetupWithManager(mgr ctrl.Manager) error {
	// Setup field indexes first
	if err := setupIndexes(mgr); err != nil {
		return err
	}

	hookServer := mgr.GetWebhookServer()
	hookServer.Register(w.Endpoint, w)

	return nil
}

func (wh *Webhook) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	log := logf.FromContext(r.Context()).WithName("loops-http-webhook")
	log.Info("Handling request", "method", r.Method, "remoteAddr", r.RemoteAddr)

	// panic recovery
	defer func() {
		if r := recover(); r != nil {
			log.Error(nil, "Panic in webhook handler", "panic", r)
			wh.writeResponse(w, InternalServerErrorResponse())
		}
	}()

	if r.Method != http.MethodPost {
		log.Error(nil, "Method not allowed", "method", r.Method)
		w.Header().Set("Allow", http.MethodPost)
		wh.writeResponse(w, MethodNotAllowedResponse())
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Error(err, "Failed to read request body")
		wh.writeResponse(w, InternalServerErrorResponse())
		return
	}
	defer func() {
		if err := r.Body.Close(); err != nil {
			log.Error(err, "Failed to close request body")
		}
	}()

	// Log the raw body for debugging
	log.Info("Received webhook body", "body", string(body))

	// Verify webhook signature
	if err := verifyWebhook(r, body, wh.signingSecret); err != nil {
		var verifyErr *WebhookVerificationError
		if errors.As(err, &verifyErr) {
			log.Error(err, "Webhook verification failed", "code", verifyErr.Code)
		} else {
			log.Error(err, "Webhook verification failed")
		}
		wh.writeResponse(w, UnauthorizedResponse())
		return
	}

	// First, parse to determine the event type
	var baseEvent loops.WebhookEvent
	if err := json.Unmarshal(body, &baseEvent); err != nil {
		log.Error(err, "Failed to parse base webhook event")
		wh.writeResponse(w, BadRequestResponse())
		return
	}

	log.Info("Parsed base event", "eventName", baseEvent.EventName, "eventTime", baseEvent.EventTime)

	// Handle based on event type
	switch baseEvent.EventName {
	case loops.EventNameMailingListSubscribed:
		var subscribedEvent loops.MailingListSubscribedEvent
		if err := json.Unmarshal(body, &subscribedEvent); err != nil {
			log.Error(err, "Failed to parse mailing list subscribed event")
			wh.writeResponse(w, BadRequestResponse())
			return
		}

		response := wh.Handler.Handle(r.Context(), Request{
			MailingListSubscribedEvent: &subscribedEvent,
			BaseEvent:                  &baseEvent,
		})
		wh.writeResponse(w, response)
		return

	case loops.EventNameMailingListUnsubscribed:
		var unsubscribedEvent loops.MailingListUnsubscribedEvent
		if err := json.Unmarshal(body, &unsubscribedEvent); err != nil {
			log.Error(err, "Failed to parse mailing list unsubscribed event")
			wh.writeResponse(w, BadRequestResponse())
			return
		}

		response := wh.Handler.Handle(r.Context(), Request{
			MailingListUnsubscribedEvent: &unsubscribedEvent,
			BaseEvent:                    &baseEvent,
		})
		wh.writeResponse(w, response)
		return

	default:
		log.Info("Unknown event type", "eventName", baseEvent.EventName)
		wh.writeResponse(w, BadRequestResponse())
		return
	}
}

func (wh *Webhook) writeResponse(w http.ResponseWriter, response Response) {
	w.WriteHeader(response.HttpStatus)
}
