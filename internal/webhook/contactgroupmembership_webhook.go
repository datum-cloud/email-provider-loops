package webhook

import (
	"context"
	"fmt"

	notificationmiloapiscomv1alpha1 "go.miloapis.com/milo/pkg/apis/notification/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create
// +kubebuilder:rbac:groups=notification.miloapis.com,resources=contacts,verbs=get;list

func NewLoopsContactGroupMembershipWebhookV1(k8sClient client.Client, signingSecret string) *Webhook {
	return &Webhook{
		Handler: HandlerFunc(func(ctx context.Context, req Request) Response {
			log := logf.FromContext(ctx).WithName("loops-webhook-handler")

			userUID := req.BaseEvent.ContactIdentity.UserID
			if userUID == "" {
				log.Info("ContactIdentity.UserID is empty, cannot find contact")
				return BadRequestResponse()
			}

			contact, err := getContactByProviderID(ctx, k8sClient, userUID)
			if err != nil {
				log.Error(err, "Failed to get contact by user UID",
					"userUID", userUID)
				return InternalServerErrorResponse()
			}
			if contact == nil {
				log.Info("Contact not found for user UID",
					"userID", userUID)
				return BadRequestResponse()
			}
			log.Info("Found contact for webhook event", "contactName", contact.Name, "contactNamespace", contact.Namespace, "contactUID", contact.UID)

			var groupID string
			if req.MailingListSubscribedEvent != nil {
				groupID = req.MailingListSubscribedEvent.MailingList.ID
			}
			if req.MailingListUnsubscribedEvent != nil {
				groupID = req.MailingListUnsubscribedEvent.MailingList.ID
			}
			if groupID == "" {
				log.Info("MailingList.ID is empty, cannot find contact group")
				return BadRequestResponse()
			}

			group, err := getContactGroupByProviderID(ctx, k8sClient, groupID)
			if err != nil {
				log.Error(err, "Failed to get contact group by group ID",
					"groupID", groupID)
				return InternalServerErrorResponse()
			}
			if group == nil {
				log.Info("Contact group not found for group ID",
					"groupID", groupID)
				return BadRequestResponse()
			}
			log.Info("Found contact group for webhook event", "groupID", groupID, "groupName", group.Name, "groupNamespace", group.Namespace, "groupUID", group.UID)

			// Handle mailing list subscribed event
			if req.MailingListSubscribedEvent != nil {
				log.Info("Processing SUBSCRIBED event")

				// Get assoaciate contact group memebership removal
				removal, err := getContactGroupMembershipRemoval(ctx, k8sClient, contact, group)
				if err != nil && !apierrors.IsNotFound(err) {
					log.Error(err, "Failed to get contact group membership removal", "contactName", contact.Name, "contactNamespace", contact.Namespace, "groupID", groupID)
					return InternalServerErrorResponse()
				}

				// If there is a removal, we need to delete it
				if removal != nil {
					log.Info("Contact group membership removal found, deleting", "contactName", removal.Spec.ContactRef.Name, "contactNamespace", removal.Spec.ContactRef.Namespace)
					err := deleteContactGroupMembershipRemoval(ctx, k8sClient, removal)
					if err != nil {
						log.Error(err, "Failed to delete contact group membership removal", "contactName", removal.Spec.ContactRef.Name, "contactNamespace", removal.Spec.ContactRef.Namespace)
						return InternalServerErrorResponse()
					}
				} else {
					log.Info("Contact group membership removal not found, continuing")
				}

				// Create the corresponding contact group membership
				err = createContactGroupMembership(ctx, k8sClient, contact, group)
				if err != nil && !apierrors.IsAlreadyExists(err) {
					log.Error(err, "Failed to create contact group membership")
					return InternalServerErrorResponse()
				}

				return OkResponse()
			}

			// Handle mailing list unsubscribed event
			if req.MailingListUnsubscribedEvent != nil {
				log.Info("Processing UNSUBSCRIBED event")

				// Get assoaciate contact group memebership removal
				removal, err := getContactGroupMembershipRemoval(ctx, k8sClient, contact, group)
				if err != nil && !apierrors.IsNotFound(err) {
					log.Error(err, "Failed to get contact group membership removal", "contactName", contact.Name, "contactNamespace", contact.Namespace, "groupID", groupID)
					return InternalServerErrorResponse()
				}

				if removal != nil {
					log.Info("Contact group membership removal found, skiping creation", "contactName", removal.Spec.ContactRef.Name, "contactNamespace", removal.Spec.ContactRef.Namespace)
					return OkResponse()
				} else {
					err := createContactGroupMembershipRemoval(ctx, k8sClient, contact, group)
					if err != nil {
						log.Error(err, "Failed to create contact group membership removal", "contactName", contact.Name, "contactNamespace", contact.Namespace, "groupID", groupID)
						return InternalServerErrorResponse()
					}
				}

				return OkResponse()
			}

			log.Info("No recognized event in request")
			return BadRequestResponse()
		}),
		Endpoint:      "/apis/emailnotification.k8s.io/v1/loops/contactgroupmemberships",
		signingSecret: signingSecret,
	}
}

// getContactByProviderID retrieves a Contact by its status.providerID field using the indexed field
func getContactByProviderID(ctx context.Context, k8sClient client.Client, providerID string) (*notificationmiloapiscomv1alpha1.Contact, error) {
	log := logf.FromContext(ctx)

	var contactList notificationmiloapiscomv1alpha1.ContactList
	if err := k8sClient.List(ctx, &contactList,
		client.MatchingFields{contactStatusProviderIDIndexKey: providerID},
	); err != nil {
		return nil, err
	}

	if len(contactList.Items) == 0 {
		return nil, nil
	}

	if len(contactList.Items) > 1 {
		log.Info("Multiple contacts found with same provider ID, using first one",
			"providerID", providerID,
			"count", len(contactList.Items))
	}

	return &contactList.Items[0], nil
}

// getContactGroupByProviderID retrieves a ContactGroup by its spec.providers.loops.providerID field using the indexed field
func getContactGroupByProviderID(ctx context.Context, k8sClient client.Client, providerID string) (*notificationmiloapiscomv1alpha1.ContactGroup, error) {
	log := logf.FromContext(ctx)

	var contactGroupList notificationmiloapiscomv1alpha1.ContactGroupList
	if err := k8sClient.List(ctx, &contactGroupList,
		client.MatchingFields{groupProviderIDIndexKey: providerID},
	); err != nil {
		return nil, err
	}

	if len(contactGroupList.Items) == 0 {
		return nil, nil
	}

	if len(contactGroupList.Items) > 1 {
		log.Info("Multiple contact groups found with same provider ID, using first one",
			"providerID", providerID,
			"count", len(contactGroupList.Items))
	}

	return &contactGroupList.Items[0], nil
}

// CreateContactGroupMembership creates a ContactGroupMembership in Kubernetes
func createContactGroupMembership(ctx context.Context, k8sClient client.Client, contact *notificationmiloapiscomv1alpha1.Contact, group *notificationmiloapiscomv1alpha1.ContactGroup) error {
	log := logf.FromContext(ctx)

	contactGroupMembership := &notificationmiloapiscomv1alpha1.ContactGroupMembership{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: fmt.Sprintf("%s-%s", group.Name, contact.Name),
			Namespace:    group.Namespace,
		},
		Spec: notificationmiloapiscomv1alpha1.ContactGroupMembershipSpec{
			ContactRef: notificationmiloapiscomv1alpha1.ContactReference{
				Name:      contact.Name,
				Namespace: contact.Namespace,
			},
			ContactGroupRef: notificationmiloapiscomv1alpha1.ContactGroupReference{
				Name:      group.Name,
				Namespace: group.Namespace,
			},
		},
	}

	if err := k8sClient.Create(ctx, contactGroupMembership); err != nil {
		return err
	}

	log.Info("Created contact group membership", "contactName", contact.Name, "contactNamespace", contact.Namespace, "contactUID", contact.UID)
	return nil
}

// GetContactGroupMembershipRemoval retrieves a ContactGroupMembershipRemoval by its spec.contactRef and spec.contactGroupRef using the indexed field
func getContactGroupMembershipRemoval(ctx context.Context, k8sClient client.Client, contact *notificationmiloapiscomv1alpha1.Contact, group *notificationmiloapiscomv1alpha1.ContactGroup) (*notificationmiloapiscomv1alpha1.ContactGroupMembershipRemoval, error) {
	log := logf.FromContext(ctx)

	var removalList notificationmiloapiscomv1alpha1.ContactGroupMembershipRemovalList
	if err := k8sClient.List(ctx, &removalList,
		client.MatchingFields{groupMembershipRemovalIndexKey: buildGroupMembershipRemovalIndexKey(&notificationmiloapiscomv1alpha1.ContactReference{
			Name:      contact.Name,
			Namespace: contact.Namespace,
		}, &notificationmiloapiscomv1alpha1.ContactGroupReference{
			Name:      group.Name,
			Namespace: group.Namespace,
		})},
	); err != nil {
		return nil, err
	}

	if len(removalList.Items) == 0 {
		return nil, nil
	}

	if len(removalList.Items) > 1 {
		log.Info("Multiple contact group membership removals found with same contact and group, using first one", "contactName", contact.Name, "contactNamespace", contact.Namespace, "groupName", group.Name, "groupNamespace", group.Namespace, "count", len(removalList.Items))
	}

	return &removalList.Items[0], nil
}

// DeleteContactGroupMembershipRemoval deletes a ContactGroupMembershipRemoval by its name and namespace
func deleteContactGroupMembershipRemoval(ctx context.Context, k8sClient client.Client, removal *notificationmiloapiscomv1alpha1.ContactGroupMembershipRemoval) error {
	log := logf.FromContext(ctx)

	if err := k8sClient.Delete(ctx, removal); err != nil {
		return err
	}

	log.Info("Deleted contact group membership removal", "removalName", removal.Name, "removalNamespace", removal.Namespace)
	return nil
}

// CreateContactGroupMembershipRemoval creates a ContactGroupMembershipRemoval in Kubernetes
func createContactGroupMembershipRemoval(ctx context.Context, k8sClient client.Client, contact *notificationmiloapiscomv1alpha1.Contact, group *notificationmiloapiscomv1alpha1.ContactGroup) error {
	log := logf.FromContext(ctx)

	removal := &notificationmiloapiscomv1alpha1.ContactGroupMembershipRemoval{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: fmt.Sprintf("%s-%s", group.Name, contact.Name),
			Namespace:    group.Namespace,
		},
		Spec: notificationmiloapiscomv1alpha1.ContactGroupMembershipRemovalSpec{
			ContactRef: notificationmiloapiscomv1alpha1.ContactReference{
				Name:      contact.Name,
				Namespace: contact.Namespace,
			},
			ContactGroupRef: notificationmiloapiscomv1alpha1.ContactGroupReference{
				Name:      group.Name,
				Namespace: group.Namespace,
			},
		},
	}

	if err := k8sClient.Create(ctx, removal); err != nil {
		return err
	}

	log.Info("Created contact group membership removal", "removalName", removal.Name, "removalNamespace", removal.Namespace)
	return nil
}
