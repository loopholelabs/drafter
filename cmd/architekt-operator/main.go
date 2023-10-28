package main

import (
	"flag"
	"net"
	"net/url"
	"os"
	"strconv"

	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	architectv1alpha1 "github.com/loopholelabs/architekt/pkg/api/k8s/v1alpha1"
	"github.com/loopholelabs/architekt/pkg/client"
	"github.com/loopholelabs/architekt/pkg/controllers"
	//+kubebuilder:scaffold:imports
)

var (
	scheme = runtime.NewScheme()
	log    = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(architectv1alpha1.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme
}

func main() {
	metricsLaddr := flag.String("metrics-laddr", ":8080", "Listen address for metrics")
	probeLaddr := flag.String("probe-laddr", ":8081", "Listen address for probe")
	webhookLaddr := flag.String("webhook-laddr", ":9443", "Listen address for Webhook server")
	leaderElection := flag.Bool("leader-election", false, "Whether to enable leader election")
	leaderElectionID := flag.String("leader-election-id", "46b4a8ff.io.loopholelabs.architekt", "Leader election ID to use")
	rawManagerURL := flag.String("manager-url", "http://localhost:1400", "URL for manager API")

	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)

	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	managerURL, err := url.Parse(*rawManagerURL)
	if err != nil {
		log.Error(err, "Could not to parse manager URL")

		os.Exit(1)
	}

	webhookHost, rawWebhookPort, err := net.SplitHostPort(*webhookLaddr)
	if err != nil {
		log.Error(err, "Could not to parse webhook server laddr")

		os.Exit(1)
	}

	webhookPort, err := strconv.Atoi(rawWebhookPort)
	if err != nil {
		log.Error(err, "Could not to parse webhook server port")

		os.Exit(1)
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: server.Options{
			BindAddress: *metricsLaddr,
		},
		WebhookServer: webhook.NewServer(webhook.Options{
			Host: webhookHost,
			Port: webhookPort,
		}),
		HealthProbeBindAddress: *probeLaddr,
		LeaderElection:         *leaderElection,
		LeaderElectionID:       *leaderElectionID,
	})
	if err != nil {
		log.Error(err, "Could not start manager")

		os.Exit(1)
	}

	if err := controllers.NewInstanceReconciler(
		mgr.GetClient(),
		mgr.GetScheme(),
		mgr.GetEventRecorderFor("instance-controller"),

		*client.NewManagerRESTClient(*managerURL),
	).SetupWithManager(mgr); err != nil {
		log.Error(err, "Could not to create controller", "controller", "Instance")

		os.Exit(1)
	}
	//+kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		log.Error(err, "Could not set up health check")

		os.Exit(1)
	}

	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		log.Error(err, "Could not set up ready check")

		os.Exit(1)
	}

	log.Info("Starting manager")

	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error(err, "Could not run manager")

		os.Exit(1)
	}
}
