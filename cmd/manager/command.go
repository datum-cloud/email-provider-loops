package manager

import (
	"crypto/tls"
	"flag"
	"fmt"
	"path/filepath"
	"time"

	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	iammiloapiscomv1alpha1 "go.miloapis.com/milo/pkg/apis/iam/v1alpha1"
	notificationmiloapiscomv1alpha1 "go.miloapis.com/milo/pkg/apis/notification/v1alpha1"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/runtime"
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/certwatcher"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

// nolint:gocyclo
func CreateManagerCommand() *cobra.Command {
	var (
		metricsAddr                                                           string
		metricsCertPath, metricsCertName, metricsCertKey                      string
		webhookCertPath, webhookCertName, webhookCertKey                      string
		enableLeaderElection                                                  bool
		probeAddr                                                             string
		secureMetrics                                                         bool
		enableHTTP2                                                           bool
		leaderElectionID, leaderElectionNamespace, leaderElectionResourceLock string
		leaseDuration, renewDeadline, retryPeriod                             time.Duration
	)

	cmd := &cobra.Command{
		Use:   "manager",
		Short: "Start the controller manager",
		Long:  "Start the Kubernetes controller manager for the email provider resend",
		RunE: func(_ *cobra.Command, _ []string) error {
			setupLog := ctrl.Log.WithName("setup")

			var tlsOpts []func(*tls.Config)

			disableHTTP2 := func(c *tls.Config) {
				setupLog.Info("disabling http/2")
				c.NextProtos = []string{"http/1.1"}
			}

			if !enableHTTP2 {
				tlsOpts = append(tlsOpts, disableHTTP2)
			}

			// Create watchers for metrics and webhooks certificates
			var metricsCertWatcher, webhookCertWatcher *certwatcher.CertWatcher

			// Initial webhook TLS options
			webhookTLSOpts := tlsOpts

			if len(webhookCertPath) > 0 {
				setupLog.Info("Initializing webhook certificate watcher using provided certificates",
					"webhook-cert-path", webhookCertPath, "webhook-cert-name", webhookCertName, "webhook-cert-key", webhookCertKey)

				var err error
				webhookCertWatcher, err = certwatcher.New(
					filepath.Join(webhookCertPath, webhookCertName),
					filepath.Join(webhookCertPath, webhookCertKey),
				)
				if err != nil {
					setupLog.Error(err, "Failed to initialize webhook certificate watcher")
					return fmt.Errorf("failed to initialize webhook certificate watcher: %w", err)
				}

				webhookTLSOpts = append(webhookTLSOpts, func(config *tls.Config) {
					config.GetCertificate = webhookCertWatcher.GetCertificate
				})
			}

			webhookServer := webhook.NewServer(webhook.Options{
				TLSOpts: webhookTLSOpts,
			})

			// Metrics endpoint is enabled in 'config/default/kustomization.yaml'. The Metrics options configure the server.
			// More info:
			// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.21.0/pkg/metrics/server
			// - https://book.kubebuilder.io/reference/metrics.html
			metricsServerOptions := metricsserver.Options{
				BindAddress:   metricsAddr,
				SecureServing: secureMetrics,
				TLSOpts:       tlsOpts,
			}

			if secureMetrics {
				// FilterProvider is used to protect the metrics endpoint with authn/authz.
				// These configurations ensure that only authorized users and service accounts
				// can access the metrics endpoint. The RBAC are configured in 'config/rbac/kustomization.yaml'. More info:
				// https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.21.0/pkg/metrics/filters#WithAuthenticationAndAuthorization
				metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
			}

			// If the certificate is not specified, controller-runtime will automatically
			// generate self-signed certificates for the metrics server. While convenient for development and testing,
			// this setup is not recommended for production.
			//
			// TODO(user): If you enable certManager, uncomment the following lines:
			// - [METRICS-WITH-CERTS] at config/default/kustomization.yaml to generate and use certificates
			// managed by cert-manager for the metrics server.
			// - [PROMETHEUS-WITH-CERTS] at config/prometheus/kustomization.yaml for TLS certification.
			if len(metricsCertPath) > 0 {
				setupLog.Info("Initializing metrics certificate watcher using provided certificates",
					"metrics-cert-path", metricsCertPath, "metrics-cert-name", metricsCertName, "metrics-cert-key", metricsCertKey)

				var err error
				metricsCertWatcher, err = certwatcher.New(
					filepath.Join(metricsCertPath, metricsCertName),
					filepath.Join(metricsCertPath, metricsCertKey),
				)
				if err != nil {
					setupLog.Error(err, "to initialize metrics certificate watcher", "error", err)
					return fmt.Errorf("failed to initialize metrics certificate watcher: %w", err)
				}

				metricsServerOptions.TLSOpts = append(metricsServerOptions.TLSOpts, func(config *tls.Config) {
					config.GetCertificate = metricsCertWatcher.GetCertificate
				})
			}

			scheme := runtime.NewScheme()
			utilruntime.Must(clientgoscheme.AddToScheme(scheme))
			utilruntime.Must(iammiloapiscomv1alpha1.AddToScheme(scheme))
			utilruntime.Must(notificationmiloapiscomv1alpha1.AddToScheme(scheme))

			mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
				Scheme:                     scheme,
				Metrics:                    metricsServerOptions,
				WebhookServer:              webhookServer,
				HealthProbeBindAddress:     probeAddr,
				LeaderElection:             enableLeaderElection,
				LeaderElectionID:           leaderElectionID,
				LeaderElectionNamespace:    leaderElectionNamespace,
				LeaderElectionResourceLock: leaderElectionResourceLock,
				LeaseDuration:              &leaseDuration,
				RenewDeadline:              &renewDeadline,
				RetryPeriod:                &retryPeriod,
			})
			if err != nil {
				setupLog.Error(err, "unable to start manager")
				return fmt.Errorf("unable to start manager: %w", err)
			}

			if metricsCertWatcher != nil {
				setupLog.Info("Adding metrics certificate watcher to manager")
				if err := mgr.Add(metricsCertWatcher); err != nil {
					setupLog.Error(err, "unable to add metrics certificate watcher to manager")
					return fmt.Errorf("unable to add metrics certificate watcher to manager: %w", err)
				}
			}

			if webhookCertWatcher != nil {
				setupLog.Info("Adding webhook certificate watcher to manager")
				if err := mgr.Add(webhookCertWatcher); err != nil {
					setupLog.Error(err, "unable to add webhook certificate watcher to manager")
					return err
				}
			}

			if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
				setupLog.Error(err, "unable to set up health check")
				return fmt.Errorf("unable to set up health check: %w", err)
			}
			if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
				setupLog.Error(err, "unable to set up ready check")
				return fmt.Errorf("unable to set up ready check: %w", err)
			}

			setupLog.Info("starting manager")
			if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
				setupLog.Error(err, "problem running manager")
				return fmt.Errorf("problem running manager: %w", err)
			}
			return nil
		},
	}

	// Manager configuration flags
	cmd.Flags().StringVar(&metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to. "+
		"Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.")
	cmd.Flags().StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	cmd.Flags().BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")

	// Webhook configuration flags
	cmd.Flags().BoolVar(&secureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	cmd.Flags().StringVar(&webhookCertPath, "webhook-cert-path", "",
		"The directory that contains the webhook certificate.")
	cmd.Flags().StringVar(&webhookCertName, "webhook-cert-name", "tls.crt", "The name of the webhook certificate file.")
	cmd.Flags().StringVar(&webhookCertKey, "webhook-cert-key", "tls.key", "The name of the webhook key file.")

	// Metrics configuration flags
	cmd.Flags().StringVar(&metricsCertPath, "metrics-cert-path", "",
		"The directory that contains the metrics server certificate.")
	cmd.Flags().StringVar(&metricsCertName, "metrics-cert-name", "tls.crt",
		"The name of the metrics server certificate file.")
	cmd.Flags().StringVar(&metricsCertKey, "metrics-cert-key", "tls.key", "The name of the metrics server key file.")
	cmd.Flags().BoolVar(&enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")

	// Leader election configuration flags
	cmd.Flags().StringVar(&leaderElectionID, "leader-election-id", "1adf6d2b.resend.notification.miloapis.com",
		"The name of the resource that leader election will use for holding the leader lock.")
	cmd.Flags().StringVar(&leaderElectionNamespace, "leader-election-namespace", "",
		"Namespace to use for leader election. If empty, the controller will discover the namespace it is running in.")
	cmd.Flags().StringVar(&leaderElectionResourceLock, "leader-election-resource-lock", "leases",
		"The type of resource object that is used for locking during leader election. Supported options are 'leases', "+
			"'endpointsleases' and 'configmapsleases'.")
	cmd.Flags().DurationVar(&leaseDuration, "leader-election-lease-duration", 15*time.Second,
		"The duration that non-leader candidates will wait after observing a leadership renewal until attempting to "+
			"acquire leadership of a led but unrenewed leader slot.")
	cmd.Flags().DurationVar(&renewDeadline, "leader-election-renew-deadline", 10*time.Second,
		"The interval between attempts by the acting master to renew a leadership slot before it stops leading.")
	cmd.Flags().DurationVar(&retryPeriod, "leader-election-retry-period", 2*time.Second,
		"The duration the clients should wait between attempting acquisition and renewal of a leadership.")

	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	return cmd
}
