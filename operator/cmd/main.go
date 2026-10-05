/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"crypto/tls"
	"flag"
	"net/http"
	"os"
	"path/filepath"
	"time"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/certwatcher"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/controller"
	"github.com/kubemoot/kubemoot/operator/internal/crewmemory"
	kubemootnats "github.com/kubemoot/kubemoot/operator/internal/nats"
	"github.com/kubemoot/kubemoot/operator/internal/notifications"
	kubemootscheduler "github.com/kubemoot/kubemoot/operator/internal/scheduler"
	kubemootwebhook "github.com/kubemoot/kubemoot/operator/internal/webhook"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(aiv1alpha1.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme
}

// controllerSetup pairs a controller name with its setup function.
type controllerSetup struct {
	name    string
	setupFn func(ctrl.Manager) error
}

// mustSetupControllers registers each controller with the manager,
// logging and exiting on any failure.
func mustSetupControllers(mgr ctrl.Manager, controllers []controllerSetup) {
	for _, c := range controllers {
		if err := c.setupFn(mgr); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", c.name)
			os.Exit(1)
		}
	}
}

// runnableSetup pairs a runnable's description with the runnable.
type runnableSetup struct {
	name     string
	runnable manager.Runnable
}

// mustAddRunnables registers the operator's non-controller runnables, logging
// and exiting on any failure.
func mustAddRunnables(mgr ctrl.Manager, natsPublisher *kubemootnats.Publisher) {
	runnables := []runnableSetup{
		// Agent self-scheduling poller. Reads the kubemoot_scheduled KV bucket
		// every 30s and publishes thread_start for any due records.
		{"scheduler poller", kubemootscheduler.New(natsPublisher)},
		// One-time crew-memory key migration: <crew>.<topic>.<key> becomes
		// <ns>.<crew>.<topic>.<key> when exactly one namespace has a Crew of that name.
		{"crew memory migration", &crewmemory.Migrator{Reader: mgr.GetAPIReader(), KV: natsPublisher}},
		// NotificationSink dispatcher. Subscribes to kubemoot.discuss.> and
		// fires per-crew webhooks on concern signals. Leader-elected so only
		// one operator pod dispatches at a time.
		{"notifications dispatcher", &notifications.Dispatcher{
			Client:        mgr.GetClient(),
			Publisher:     natsPublisher,
			DashboardBase: os.Getenv("KUBEMOOT_DASHBOARD_BASE"),
		}},
		// Deletes provider-state entries no ModelProvider owns, once at start.
		{"provider state sweep", &controller.ProviderStateSweeper{
			Reader: mgr.GetAPIReader(),
			Store:  natsPublisher,
		}},
		// On-demand fitness report server: generates the XLSX fresh from transcripts
		// on each download (no pre-baked artifact), so every report reflects the
		// currently deployed generator. The dashboard's download endpoint proxies here.
		{"fitness report server", &controller.ReportServer{
			Client:    mgr.GetClient(),
			Publisher: natsPublisher,
			Addr:      ":8082",
		}},
	}
	for _, r := range runnables {
		if err := mgr.Add(r.runnable); err != nil {
			setupLog.Error(err, "unable to register runnable", "runnable", r.name)
			os.Exit(1)
		}
	}
}

// webhookSetup names a CRD and registers its validating webhook.
type webhookSetup struct {
	name     string
	register func(ctrl.Manager) error
}

// validatingWebhook builds the registration for one CRD's typed validator.
func validatingWebhook[T runtime.Object](name string, obj T, validator admission.Validator[T]) webhookSetup {
	return webhookSetup{name: name, register: func(mgr ctrl.Manager) error {
		return ctrl.NewWebhookManagedBy(mgr, obj).WithValidator(validator).Complete()
	}}
}

// mustSetupWebhooks registers each validating webhook with the manager,
// logging and exiting on any failure.
func mustSetupWebhooks(mgr ctrl.Manager) {
	webhooks := []webhookSetup{
		validatingWebhook("Agent", &aiv1alpha1.Agent{}, &kubemootwebhook.AgentValidator{}),
		validatingWebhook("MCPServer", &aiv1alpha1.MCPServer{}, &kubemootwebhook.MCPServerValidator{}),
		validatingWebhook("RAGSource", &aiv1alpha1.RAGSource{}, &kubemootwebhook.RAGSourceValidator{}),
		validatingWebhook("Crew", &aiv1alpha1.Crew{}, &kubemootwebhook.CrewValidator{}),
		validatingWebhook("CrewFitness", &aiv1alpha1.CrewFitness{}, &kubemootwebhook.CrewFitnessValidator{}),
	}
	for _, w := range webhooks {
		if err := w.register(mgr); err != nil {
			setupLog.Error(err, "unable to create webhook", "webhook", w.name)
			os.Exit(1)
		}
	}
	setupLog.Info("Registered validating webhooks for Agent, Crew, CrewFitness, MCPServer, RAGSource")
}

// managerFlags holds all parsed command-line flags.
type managerFlags struct {
	metricsAddr          string
	metricsCertPath      string
	metricsCertName      string
	metricsCertKey       string
	webhookCertPath      string
	webhookCertName      string
	webhookCertKey       string
	enableLeaderElection bool
	leaseDuration        time.Duration
	renewDeadline        time.Duration
	retryPeriod          time.Duration
	probeAddr            string
	secureMetrics        bool
	enableHTTP2          bool
}

// parseFlags registers and parses all command-line flags.
func parseFlags() managerFlags {
	var f managerFlags
	flag.StringVar(&f.metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to. "+
		"Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.")
	flag.StringVar(&f.probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&f.enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	// Defaults are generous to tolerate brief control-plane API hiccups
	// (homelab observed: 5s timeouts on lease renew → operator pod crashes
	// → CrashLoopBackOff). controller-runtime defaults are 15s/10s/2s
	// which are too aggressive for an intermittent network.
	flag.DurationVar(&f.leaseDuration, "leader-lease-duration", 60*time.Second,
		"How long a non-leader will wait before attempting to acquire leadership.")
	flag.DurationVar(&f.renewDeadline, "leader-renew-deadline", 50*time.Second,
		"How long the current leader will retry to renew its leadership before giving up.")
	flag.DurationVar(&f.retryPeriod, "leader-retry-period", 10*time.Second,
		"How often clients should poll the lease.")
	flag.BoolVar(&f.secureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	flag.StringVar(&f.webhookCertPath, "webhook-cert-path", "", "The directory that contains the webhook certificate.")
	flag.StringVar(&f.webhookCertName, "webhook-cert-name", "tls.crt", "The name of the webhook certificate file.")
	flag.StringVar(&f.webhookCertKey, "webhook-cert-key", "tls.key", "The name of the webhook key file.")
	flag.StringVar(&f.metricsCertPath, "metrics-cert-path", "",
		"The directory that contains the metrics server certificate.")
	flag.StringVar(&f.metricsCertName, "metrics-cert-name", "tls.crt", "The name of the metrics server certificate file.")
	flag.StringVar(&f.metricsCertKey, "metrics-cert-key", "tls.key", "The name of the metrics server key file.")
	flag.BoolVar(&f.enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")
	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	return f
}

// buildTLSOpts returns base TLS options, disabling HTTP/2 unless explicitly enabled.
func buildTLSOpts(enableHTTP2 bool) []func(*tls.Config) {
	var tlsOpts []func(*tls.Config)
	if !enableHTTP2 {
		tlsOpts = append(tlsOpts, func(c *tls.Config) {
			setupLog.Info("disabling http/2")
			c.NextProtos = []string{"http/1.1"}
		})
	}
	return tlsOpts
}

// setupCertWatcher creates a certificate watcher for the given cert directory,
// returning nil if the path is empty.
func setupCertWatcher(certPath, certName, certKey, label string) *certwatcher.CertWatcher {
	if len(certPath) == 0 {
		return nil
	}
	setupLog.Info("Initializing "+label+" certificate watcher using provided certificates",
		label+"-cert-path", certPath, label+"-cert-name", certName, label+"-cert-key", certKey)
	watcher, err := certwatcher.New(
		filepath.Join(certPath, certName),
		filepath.Join(certPath, certKey),
	)
	if err != nil {
		setupLog.Error(err, "Failed to initialize "+label+" certificate watcher")
		os.Exit(1)
	}
	return watcher
}

// mustAddCertWatcher adds a certificate watcher to the manager if non-nil.
func mustAddCertWatcher(mgr ctrl.Manager, watcher *certwatcher.CertWatcher, label string) {
	if watcher == nil {
		return
	}
	setupLog.Info("Adding " + label + " certificate watcher to manager")
	if err := mgr.Add(watcher); err != nil {
		setupLog.Error(err, "unable to add "+label+" certificate watcher to manager")
		os.Exit(1)
	}
}

// The secure metrics endpoint authenticates callers with TokenReview and authorizes
// them with SubjectAccessReview.
// +kubebuilder:rbac:groups=authentication.k8s.io,resources=tokenreviews,verbs=create
// +kubebuilder:rbac:groups=authorization.k8s.io,resources=subjectaccessreviews,verbs=create

// withLeaderElection sets the leader election options. The leader releases its lease
// when it stops, so a rollout hands over in seconds instead of waiting out the lease;
// this is safe because main exits as soon as the manager returns.
func withLeaderElection(o ctrl.Options, f managerFlags) ctrl.Options {
	o.LeaderElection = f.enableLeaderElection
	o.LeaderElectionID = "7b551ae9.kubemoot.ai"
	o.LeaderElectionReleaseOnCancel = true
	o.LeaseDuration = &f.leaseDuration
	o.RenewDeadline = &f.renewDeadline
	o.RetryPeriod = &f.retryPeriod
	return o
}

func main() {
	f := parseFlags()
	tlsOpts := buildTLSOpts(f.enableHTTP2)

	webhookCertWatcher := setupCertWatcher(f.webhookCertPath, f.webhookCertName, f.webhookCertKey, "webhook")
	webhookTLSOpts := tlsOpts
	if webhookCertWatcher != nil {
		webhookTLSOpts = append(webhookTLSOpts, func(config *tls.Config) {
			config.GetCertificate = webhookCertWatcher.GetCertificate
		})
	}
	webhookServer := webhook.NewServer(webhook.Options{TLSOpts: webhookTLSOpts})

	metricsServerOptions := metricsserver.Options{
		BindAddress:   f.metricsAddr,
		SecureServing: f.secureMetrics,
		TLSOpts:       tlsOpts,
	}
	if f.secureMetrics {
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}
	metricsCertWatcher := setupCertWatcher(f.metricsCertPath, f.metricsCertName, f.metricsCertKey, "metrics")
	if metricsCertWatcher != nil {
		metricsServerOptions.TLSOpts = append(metricsServerOptions.TLSOpts, func(config *tls.Config) {
			config.GetCertificate = metricsCertWatcher.GetCertificate
		})
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), withLeaderElection(ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions,
		WebhookServer:          webhookServer,
		HealthProbeBindAddress: f.probeAddr,
	}, f))
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// Create shared config cache for KubemootConfig and load the singleton before any
	// controller runs: the API reader bypasses the informer cache, which has not
	// started yet. Otherwise the first Agent or RAGSource reconcile after an operator
	// restart would build its Deployment from fallback images with no pull secret.
	configCache := controller.NewConfigCache()
	if err := configCache.Prime(context.Background(), mgr.GetAPIReader()); err != nil {
		setupLog.Error(err, "unable to load KubemootConfig at startup; using fallback defaults until it reconciles")
	}

	// Create NATS publisher (no-op when NATS_URL is not set)
	natsPublisher := kubemootnats.NewPublisher("")
	defer natsPublisher.Close()

	// KubemootConfig controller is listed first to populate the cache before others start.
	// Registration order is preserved from the original setup sequence.
	mustSetupControllers(mgr, []controllerSetup{
		{"KubemootConfig", (&controller.KubemootConfigReconciler{
			Client: mgr.GetClient(), Scheme: mgr.GetScheme(), ConfigCache: configCache,
		}).SetupWithManager},
		{"ModelProvider", (&controller.ModelProviderReconciler{
			Client: mgr.GetClient(), Scheme: mgr.GetScheme(), ConfigCache: configCache, NATSPublisher: natsPublisher,
		}).SetupWithManager},
		{"Model", (&controller.ModelReconciler{
			Client: mgr.GetClient(), Scheme: mgr.GetScheme(),
		}).SetupWithManager},
		{"MCPServer", (&controller.MCPServerReconciler{
			Client: mgr.GetClient(), Scheme: mgr.GetScheme(), ConfigCache: configCache, NATSPublisher: natsPublisher,
		}).SetupWithManager},
		{"EmbeddingModel", (&controller.EmbeddingModelReconciler{
			Client: mgr.GetClient(), Scheme: mgr.GetScheme(),
		}).SetupWithManager},
		{"RAGSource", (&controller.RAGSourceReconciler{
			Client: mgr.GetClient(), Scheme: mgr.GetScheme(), ConfigCache: configCache,
			HTTPClient: &http.Client{Timeout: 10 * time.Second},
		}).SetupWithManager},
		{"Agent", (&controller.AgentReconciler{
			Client: mgr.GetClient(), ConfigCache: configCache, NATSPublisher: natsPublisher,
		}).SetupWithManager},
		{"Skill", (&controller.SkillReconciler{
			Client: mgr.GetClient(), Scheme: mgr.GetScheme(), NATSPublisher: natsPublisher,
		}).SetupWithManager},
		{"CrewSchedulingPolicy", (&controller.CrewSchedulingPolicyReconciler{
			Client: mgr.GetClient(),
		}).SetupWithManager},
		{"MootArchetype", (&controller.MootArchetypeReconciler{
			Client: mgr.GetClient(),
		}).SetupWithManager},
		{"MCPGateway", (&controller.MCPGatewayReconciler{
			Client: mgr.GetClient(), Scheme: mgr.GetScheme(), ConfigCache: configCache, NATSPublisher: natsPublisher,
		}).SetupWithManager},
		{"Crew", (&controller.CrewReconciler{
			Client: mgr.GetClient(), Scheme: mgr.GetScheme(), ConfigCache: configCache, NATSPublisher: natsPublisher,
		}).SetupWithManager},
		{"MCPCatalog", (&controller.MCPCatalogReconciler{
			Client: mgr.GetClient(), Scheme: mgr.GetScheme(),
		}).SetupWithManager},
		{"MCPServerReport", (&controller.MCPServerReportReconciler{
			Client: mgr.GetClient(), Scheme: mgr.GetScheme(), NATSPublisher: natsPublisher,
		}).SetupWithManager},
		{"CrewFitness", (&controller.CrewFitnessReconciler{
			Client: mgr.GetClient(), Scheme: mgr.GetScheme(), ConfigCache: configCache,
		}).SetupWithManager},
		{"CrewFitnessSuite", (&controller.CrewFitnessSuiteReconciler{
			Client: mgr.GetClient(), APIReader: mgr.GetAPIReader(), Scheme: mgr.GetScheme(), NATSPublisher: natsPublisher,
		}).SetupWithManager},
		{"NotificationSink", (&controller.NotificationSinkReconciler{
			Client: mgr.GetClient(), Scheme: mgr.GetScheme(),
		}).SetupWithManager},
		{"Namespace", (&controller.NamespaceReconciler{
			Client: mgr.GetClient(), ConfigCache: configCache,
		}).SetupWithManager},
	})
	// +kubebuilder:scaffold:builder

	mustAddRunnables(mgr, natsPublisher)

	// Register validating webhooks
	if len(f.webhookCertPath) > 0 {
		mustSetupWebhooks(mgr)
	}

	mustAddCertWatcher(mgr, metricsCertWatcher, "metrics")
	mustAddCertWatcher(mgr, webhookCertWatcher, "webhook")

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager", "webhooks", len(f.webhookCertPath) > 0)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
