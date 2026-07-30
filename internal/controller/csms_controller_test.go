package controller

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	csmsv1alpha1 "github.com/seanlee0923/csms-platform/api/v1alpha1"
)

func newTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add client-go scheme: %v", err)
	}
	if err := csmsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add csms scheme: %v", err)
	}
	return scheme
}

func newReconciler(t *testing.T, objs ...client.Object) (*CSMSReconciler, client.Client) {
	t.Helper()
	scheme := newTestScheme(t)
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&csmsv1alpha1.CSMS{}, &appsv1.Deployment{}).
		WithObjects(objs...).
		Build()
	return &CSMSReconciler{Client: c, Scheme: scheme}, c
}

func TestReconcileNotFoundReturnsNoError(t *testing.T) {
	r, _ := newReconciler(t)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "missing", Namespace: "default"}}

	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("expected no error for missing CSMS, got %v", err)
	}
}

func TestReconcileCreatesRuntimeResources(t *testing.T) {
	csms := &csmsv1alpha1.CSMS{
		ObjectMeta: metav1.ObjectMeta{Name: "csms-runtime", Namespace: "default"},
		Spec: csmsv1alpha1.CSMSSpec{
			Image:              "csms-runtime:0.1.0",
			DatabaseSecretName: "csms-runtime-database",
			Config:             map[string]string{"SOME_OTHER_RUNTIMES_VAR": "whatever-it-wants"},
		},
	}
	r, c := newReconciler(t, csms)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "csms-runtime", Namespace: "default"}}
	ctx := context.Background()

	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	// The Operator must not assume any particular runtime's env var names:
	// it only passes spec.config through verbatim, with nothing injected
	// or defaulted on top.
	var cm corev1.ConfigMap
	if err := c.Get(ctx, req.NamespacedName, &cm); err != nil {
		t.Fatalf("get configmap: %v", err)
	}
	if len(cm.Data) != 1 || cm.Data["SOME_OTHER_RUNTIMES_VAR"] != "whatever-it-wants" {
		t.Errorf("expected configmap to be a verbatim passthrough of spec.config, got %+v", cm.Data)
	}

	var svc corev1.Service
	if err := c.Get(ctx, req.NamespacedName, &svc); err != nil {
		t.Fatalf("get service: %v", err)
	}
	if len(svc.Spec.Ports) != 1 || svc.Spec.Ports[0].Port != 8080 {
		t.Errorf("unexpected service ports: %+v", svc.Spec.Ports)
	}

	var deploy appsv1.Deployment
	if err := c.Get(ctx, req.NamespacedName, &deploy); err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	if deploy.Spec.Replicas == nil || *deploy.Spec.Replicas != 1 {
		t.Errorf("expected default replicas 1, got %v", deploy.Spec.Replicas)
	}
	if deploy.Spec.Template.Spec.TerminationGracePeriodSeconds == nil || *deploy.Spec.Template.Spec.TerminationGracePeriodSeconds != 30 {
		t.Errorf("expected default terminationGracePeriodSeconds 30, got %v", deploy.Spec.Template.Spec.TerminationGracePeriodSeconds)
	}

	container := deploy.Spec.Template.Spec.Containers[0]
	if container.Image != "csms-runtime:0.1.0" {
		t.Errorf("unexpected image %q", container.Image)
	}
	if len(container.Ports) != 1 || container.Ports[0].ContainerPort != 8080 {
		t.Errorf("expected default container port 8080, got %+v", container.Ports)
	}
	if container.ReadinessProbe.HTTPGet.Path != "/readyz" {
		t.Errorf("expected default readiness path /readyz, got %q", container.ReadinessProbe.HTTPGet.Path)
	}
	if container.LivenessProbe.HTTPGet.Path != "/livez" {
		t.Errorf("expected default liveness path /livez, got %q", container.LivenessProbe.HTTPGet.Path)
	}

	foundDBSecret := false
	for _, ef := range container.EnvFrom {
		if ef.SecretRef == nil {
			continue
		}
		if ef.SecretRef.Name == "csms-runtime-database" {
			foundDBSecret = true
		}
		if ef.SecretRef.Name == "csms-runtime-redis" || ef.SecretRef.Name == "csms-runtime-api" {
			t.Errorf("unexpected secret ref %q for unset spec field", ef.SecretRef.Name)
		}
	}
	if !foundDBSecret {
		t.Errorf("expected database secret envFrom, got %+v", container.EnvFrom)
	}

	var pdb policyv1.PodDisruptionBudget
	if err := c.Get(ctx, req.NamespacedName, &pdb); !apierrors.IsNotFound(err) {
		t.Errorf("expected no poddisruptionbudget when MinAvailable unset, got err=%v", err)
	}

	var ing networkingv1.Ingress
	if err := c.Get(ctx, req.NamespacedName, &ing); !apierrors.IsNotFound(err) {
		t.Errorf("expected no ingress when Ingress unset, got err=%v", err)
	}

	var got csmsv1alpha1.CSMS
	if err := c.Get(ctx, req.NamespacedName, &got); err != nil {
		t.Fatalf("get csms: %v", err)
	}
	available := meta.FindStatusCondition(got.Status.Conditions, csmsv1alpha1.CSMSConditionAvailable)
	if available == nil || available.Status != metav1.ConditionFalse {
		t.Errorf("expected Available=False before any replica is ready, got %+v", available)
	}
}

func TestReconcileHonorsCustomPortProbePathsAndTerminationGrace(t *testing.T) {
	grace := int64(45)
	csms := &csmsv1alpha1.CSMS{
		ObjectMeta: metav1.ObjectMeta{Name: "csms-runtime", Namespace: "default"},
		Spec: csmsv1alpha1.CSMSSpec{
			Image:                         "some-other-ocpp-runtime:1.0.0",
			Port:                          9000,
			LivenessPath:                  "/healthz",
			ReadinessPath:                 "/ready",
			TerminationGracePeriodSeconds: &grace,
		},
	}
	r, c := newReconciler(t, csms)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "csms-runtime", Namespace: "default"}}
	ctx := context.Background()

	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var svc corev1.Service
	if err := c.Get(ctx, req.NamespacedName, &svc); err != nil {
		t.Fatalf("get service: %v", err)
	}
	if len(svc.Spec.Ports) != 1 || svc.Spec.Ports[0].Port != 9000 {
		t.Errorf("expected service port 9000, got %+v", svc.Spec.Ports)
	}

	var deploy appsv1.Deployment
	if err := c.Get(ctx, req.NamespacedName, &deploy); err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	if deploy.Spec.Template.Spec.TerminationGracePeriodSeconds == nil || *deploy.Spec.Template.Spec.TerminationGracePeriodSeconds != 45 {
		t.Errorf("expected terminationGracePeriodSeconds 45, got %v", deploy.Spec.Template.Spec.TerminationGracePeriodSeconds)
	}
	container := deploy.Spec.Template.Spec.Containers[0]
	if len(container.Ports) != 1 || container.Ports[0].ContainerPort != 9000 {
		t.Errorf("expected container port 9000, got %+v", container.Ports)
	}
	if container.ReadinessProbe.HTTPGet.Path != "/ready" {
		t.Errorf("expected readiness path /ready, got %q", container.ReadinessProbe.HTTPGet.Path)
	}
	if container.LivenessProbe.HTTPGet.Path != "/healthz" {
		t.Errorf("expected liveness path /healthz, got %q", container.LivenessProbe.HTTPGet.Path)
	}
}

func TestReconcileStatusBecomesAvailableWhenReplicasReady(t *testing.T) {
	csms := &csmsv1alpha1.CSMS{
		ObjectMeta: metav1.ObjectMeta{Name: "csms-runtime", Namespace: "default"},
		Spec:       csmsv1alpha1.CSMSSpec{Image: "csms-runtime:0.1.0"},
	}
	r, c := newReconciler(t, csms)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "csms-runtime", Namespace: "default"}}
	ctx := context.Background()

	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}

	var deploy appsv1.Deployment
	if err := c.Get(ctx, req.NamespacedName, &deploy); err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	deploy.Status.Replicas = 1
	deploy.Status.ReadyReplicas = 1
	if err := c.Status().Update(ctx, &deploy); err != nil {
		t.Fatalf("update deployment status: %v", err)
	}

	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}

	var got csmsv1alpha1.CSMS
	if err := c.Get(ctx, req.NamespacedName, &got); err != nil {
		t.Fatalf("get csms: %v", err)
	}
	available := meta.FindStatusCondition(got.Status.Conditions, csmsv1alpha1.CSMSConditionAvailable)
	if available == nil || available.Status != metav1.ConditionTrue {
		t.Fatalf("expected Available=True, got %+v", available)
	}
	progressing := meta.FindStatusCondition(got.Status.Conditions, csmsv1alpha1.CSMSConditionProgressing)
	if progressing == nil || progressing.Status != metav1.ConditionFalse {
		t.Fatalf("expected Progressing=False, got %+v", progressing)
	}
	if got.Status.ReadyReplicas != 1 {
		t.Errorf("expected status.readyReplicas=1, got %d", got.Status.ReadyReplicas)
	}
}

func TestReconcileCreatesPodDisruptionBudgetWhenMinAvailableSet(t *testing.T) {
	minAvailable := intstr.FromInt(1)
	csms := &csmsv1alpha1.CSMS{
		ObjectMeta: metav1.ObjectMeta{Name: "csms-runtime", Namespace: "default"},
		Spec: csmsv1alpha1.CSMSSpec{
			Image:        "csms-runtime:0.1.0",
			MinAvailable: &minAvailable,
		},
	}
	r, c := newReconciler(t, csms)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "csms-runtime", Namespace: "default"}}
	ctx := context.Background()

	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var pdb policyv1.PodDisruptionBudget
	if err := c.Get(ctx, req.NamespacedName, &pdb); err != nil {
		t.Fatalf("get poddisruptionbudget: %v", err)
	}
	if pdb.Spec.MinAvailable == nil || pdb.Spec.MinAvailable.IntValue() != 1 {
		t.Errorf("unexpected minAvailable: %+v", pdb.Spec.MinAvailable)
	}
}

func TestReconcileRemovesPodDisruptionBudgetWhenMinAvailableUnset(t *testing.T) {
	minAvailable := intstr.FromInt(1)
	csms := &csmsv1alpha1.CSMS{
		ObjectMeta: metav1.ObjectMeta{Name: "csms-runtime", Namespace: "default"},
		Spec: csmsv1alpha1.CSMSSpec{
			Image:        "csms-runtime:0.1.0",
			MinAvailable: &minAvailable,
		},
	}
	r, c := newReconciler(t, csms)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "csms-runtime", Namespace: "default"}}
	ctx := context.Background()

	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("reconcile with minAvailable: %v", err)
	}

	var got csmsv1alpha1.CSMS
	if err := c.Get(ctx, req.NamespacedName, &got); err != nil {
		t.Fatalf("get csms: %v", err)
	}
	got.Spec.MinAvailable = nil
	if err := c.Update(ctx, &got); err != nil {
		t.Fatalf("clear minAvailable: %v", err)
	}

	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("reconcile without minAvailable: %v", err)
	}

	var pdb policyv1.PodDisruptionBudget
	if err := c.Get(ctx, req.NamespacedName, &pdb); !apierrors.IsNotFound(err) {
		t.Errorf("expected poddisruptionbudget removed, got err=%v", err)
	}
}

func TestReconcileCreatesIngressWhenSet(t *testing.T) {
	csms := &csmsv1alpha1.CSMS{
		ObjectMeta: metav1.ObjectMeta{Name: "csms-runtime", Namespace: "default"},
		Spec: csmsv1alpha1.CSMSSpec{
			Image: "csms-runtime:0.1.0",
			Ingress: &csmsv1alpha1.CSMSIngress{
				Host:             "csms.example.com",
				IngressClassName: "nginx",
				TLSSecretName:    "csms-runtime-tls",
				Annotations:      map[string]string{"nginx.ingress.kubernetes.io/proxy-read-timeout": "3600"},
			},
		},
	}
	r, c := newReconciler(t, csms)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "csms-runtime", Namespace: "default"}}
	ctx := context.Background()

	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var ing networkingv1.Ingress
	if err := c.Get(ctx, req.NamespacedName, &ing); err != nil {
		t.Fatalf("get ingress: %v", err)
	}
	if ing.Annotations["nginx.ingress.kubernetes.io/proxy-read-timeout"] != "3600" {
		t.Errorf("expected annotation passthrough, got %+v", ing.Annotations)
	}
	if ing.Spec.IngressClassName == nil || *ing.Spec.IngressClassName != "nginx" {
		t.Errorf("unexpected ingress class: %+v", ing.Spec.IngressClassName)
	}
	if len(ing.Spec.Rules) != 1 || ing.Spec.Rules[0].Host != "csms.example.com" {
		t.Fatalf("unexpected rules: %+v", ing.Spec.Rules)
	}
	backend := ing.Spec.Rules[0].HTTP.Paths[0].Backend.Service
	if backend == nil || backend.Name != "csms-runtime" || backend.Port.Name != "http" {
		t.Errorf("unexpected backend: %+v", backend)
	}
	if len(ing.Spec.TLS) != 1 || ing.Spec.TLS[0].SecretName != "csms-runtime-tls" || ing.Spec.TLS[0].Hosts[0] != "csms.example.com" {
		t.Errorf("unexpected TLS config: %+v", ing.Spec.TLS)
	}
}

func TestReconcileRemovesIngressWhenUnset(t *testing.T) {
	csms := &csmsv1alpha1.CSMS{
		ObjectMeta: metav1.ObjectMeta{Name: "csms-runtime", Namespace: "default"},
		Spec: csmsv1alpha1.CSMSSpec{
			Image:   "csms-runtime:0.1.0",
			Ingress: &csmsv1alpha1.CSMSIngress{Host: "csms.example.com"},
		},
	}
	r, c := newReconciler(t, csms)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "csms-runtime", Namespace: "default"}}
	ctx := context.Background()

	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("reconcile with ingress: %v", err)
	}

	var got csmsv1alpha1.CSMS
	if err := c.Get(ctx, req.NamespacedName, &got); err != nil {
		t.Fatalf("get csms: %v", err)
	}
	got.Spec.Ingress = nil
	if err := c.Update(ctx, &got); err != nil {
		t.Fatalf("clear ingress: %v", err)
	}

	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("reconcile without ingress: %v", err)
	}

	var ing networkingv1.Ingress
	if err := c.Get(ctx, req.NamespacedName, &ing); !apierrors.IsNotFound(err) {
		t.Errorf("expected ingress removed, got err=%v", err)
	}
}

func TestReconcileIngressWithoutTLSSecretOmitsTLS(t *testing.T) {
	csms := &csmsv1alpha1.CSMS{
		ObjectMeta: metav1.ObjectMeta{Name: "csms-runtime", Namespace: "default"},
		Spec: csmsv1alpha1.CSMSSpec{
			Image:   "csms-runtime:0.1.0",
			Ingress: &csmsv1alpha1.CSMSIngress{Host: "csms.example.com"},
		},
	}
	r, c := newReconciler(t, csms)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "csms-runtime", Namespace: "default"}}
	ctx := context.Background()

	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var ing networkingv1.Ingress
	if err := c.Get(ctx, req.NamespacedName, &ing); err != nil {
		t.Fatalf("get ingress: %v", err)
	}
	if len(ing.Spec.TLS) != 0 {
		t.Errorf("expected no TLS block without TLSSecretName, got %+v", ing.Spec.TLS)
	}
	if ing.Spec.IngressClassName != nil {
		t.Errorf("expected no IngressClassName without IngressClassName set, got %+v", ing.Spec.IngressClassName)
	}
}
