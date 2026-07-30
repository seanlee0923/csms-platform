package main

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/go-logr/logr"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	csmsv1alpha1 "github.com/seanlee0923/csms-platform/api/v1alpha1"
	"github.com/seanlee0923/csms-platform/internal/controller"
)

func main() {
	logHandler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
	logger := slog.New(logHandler)
	ctrl.SetLogger(logr.FromSlogHandler(logHandler))

	scheme := clientgoscheme.Scheme
	utilruntime.Must(csmsv1alpha1.AddToScheme(scheme))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: ":8080"},
		HealthProbeBindAddress: ":8081",
		LeaderElection:         true,
		LeaderElectionID:       "csms-operator.csms.seanlee0923.dev",
	})
	if err != nil {
		logger.Error("create manager", "error", err)
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthzCheck); err != nil {
		logger.Error("register healthz check", "error", err)
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthzCheck); err != nil {
		logger.Error("register readyz check", "error", err)
		os.Exit(1)
	}

	reconciler := &controller.CSMSReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}
	if err := reconciler.SetupWithManager(mgr); err != nil {
		logger.Error("setup csms controller", "error", err)
		os.Exit(1)
	}

	logger.Info("starting csms-operator")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		logger.Error("manager stopped", "error", err)
		os.Exit(1)
	}
}

func healthzCheck(_ *http.Request) error {
	return nil
}
