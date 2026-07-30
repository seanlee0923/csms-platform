// Package controller reconciles CSMS resources into a csms-runtime
// Deployment, Service, ConfigMap and, optionally, a PodDisruptionBudget.
package controller

import (
	"context"
	"fmt"
	"strconv"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	csmsv1alpha1 "github.com/seanlee0923/csms-platform/api/v1alpha1"
)

const (
	labelName     = "app.kubernetes.io/name"
	labelInstance = "app.kubernetes.io/instance"
	componentName = "csms-runtime"

	defaultLogLevel        = "info"
	defaultHeartbeat       = int32(300)
	defaultShutdownTimeout = "30s"
	defaultRateLimit       = int32(60)
	defaultLeaseTTL        = "30s"
	defaultRenewInterval   = "10s"
)

// CSMSReconciler reconciles a CSMS object.
type CSMSReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=csms.seanlee0923.dev,resources=csmses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=csms.seanlee0923.dev,resources=csmses/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=csms.seanlee0923.dev,resources=csmses/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services;configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=policy,resources=poddisruptionbudgets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete

// Reconcile brings the Runtime Deployment, Service, ConfigMap and optional
// PodDisruptionBudget for a CSMS resource in line with its spec, then
// reports observed Deployment state back onto CSMS status. It never creates
// or manages the MySQL, Redis or API key Secrets a CSMS references.
func (r *CSMSReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := ctrl.LoggerFrom(ctx)

	var csms csmsv1alpha1.CSMS
	if err := r.Get(ctx, req.NamespacedName, &csms); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	labels := map[string]string{
		labelName:     componentName,
		labelInstance: csms.Name,
	}

	if err := r.reconcileConfigMap(ctx, &csms, labels); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile configmap: %w", err)
	}

	if err := r.reconcileService(ctx, &csms, labels); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile service: %w", err)
	}

	deployment, err := r.reconcileDeployment(ctx, &csms, labels)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile deployment: %w", err)
	}

	if err := r.reconcilePodDisruptionBudget(ctx, &csms, labels); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile poddisruptionbudget: %w", err)
	}

	if err := r.reconcileIngress(ctx, &csms, labels); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile ingress: %w", err)
	}

	if err := r.updateStatus(ctx, &csms, deployment); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status: %w", err)
	}

	logger.V(1).Info("reconciled csms", "name", csms.Name, "namespace", csms.Namespace)
	return ctrl.Result{}, nil
}

func (r *CSMSReconciler) reconcileConfigMap(ctx context.Context, csms *csmsv1alpha1.CSMS, labels map[string]string) error {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: csms.Name, Namespace: csms.Namespace},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, cm, func() error {
		cm.Labels = labels
		cm.Data = runtimeConfigData(csms.Spec.Config)
		return controllerutil.SetControllerReference(csms, cm, r.Scheme)
	})
	return err
}

func runtimeConfigData(cfg csmsv1alpha1.CSMSConfig) map[string]string {
	heartbeat := defaultHeartbeat
	if cfg.HeartbeatIntervalSeconds != nil {
		heartbeat = *cfg.HeartbeatIntervalSeconds
	}
	rateLimit := defaultRateLimit
	if cfg.CommandRateLimit != nil {
		rateLimit = *cfg.CommandRateLimit
	}

	return map[string]string{
		"CSMS_HTTP_ADDR":              ":8080",
		"CSMS_HEARTBEAT_INTERVAL":     strconv.Itoa(int(heartbeat)),
		"CSMS_SHUTDOWN_TIMEOUT":       stringOrDefault(cfg.ShutdownTimeout, defaultShutdownTimeout),
		"CSMS_LOG_LEVEL":              stringOrDefault(cfg.LogLevel, defaultLogLevel),
		"CSMS_SESSION_LEASE_TTL":      stringOrDefault(cfg.SessionLeaseTTL, defaultLeaseTTL),
		"CSMS_SESSION_RENEW_INTERVAL": stringOrDefault(cfg.SessionRenewInterval, defaultRenewInterval),
		"CSMS_COMMAND_RATE_LIMIT":     strconv.Itoa(int(rateLimit)),
	}
}

func stringOrDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func (r *CSMSReconciler) reconcileService(ctx context.Context, csms *csmsv1alpha1.CSMS, labels map[string]string) error {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: csms.Name, Namespace: csms.Namespace},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		svc.Labels = labels
		svc.Spec.Selector = labels
		svc.Spec.Ports = []corev1.ServicePort{
			{Name: "http", Port: 8080, TargetPort: intstr.FromString("http")},
		}
		return controllerutil.SetControllerReference(csms, svc, r.Scheme)
	})
	return err
}

func (r *CSMSReconciler) reconcileDeployment(ctx context.Context, csms *csmsv1alpha1.CSMS, labels map[string]string) (*appsv1.Deployment, error) {
	replicas := int32(1)
	if csms.Spec.Replicas != nil {
		replicas = *csms.Spec.Replicas
	}

	resources := csms.Spec.Resources
	if resources.Requests == nil && resources.Limits == nil {
		resources = corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("50m"),
				corev1.ResourceMemory: resource.MustParse("64Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("500m"),
				corev1.ResourceMemory: resource.MustParse("256Mi"),
			},
		}
	}

	envFrom := []corev1.EnvFromSource{
		{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: csms.Name}}},
	}
	optional := true
	for _, secretName := range []string{csms.Spec.DatabaseSecretName, csms.Spec.RedisSecretName, csms.Spec.APISecretName} {
		if secretName == "" {
			continue
		}
		envFrom = append(envFrom, corev1.EnvFromSource{
			SecretRef: &corev1.SecretEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
				Optional:             &optional,
			},
		})
	}

	terminationGrace := int64(40)
	if d, err := time.ParseDuration(csms.Spec.Config.ShutdownTimeout); err == nil {
		terminationGrace = int64(d.Seconds()) + 10
	}

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: csms.Name, Namespace: csms.Namespace},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, deployment, func() error {
		deployment.Labels = labels
		deployment.Spec.Replicas = &replicas
		deployment.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
		deployment.Spec.Template = corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: labels},
			Spec: corev1.PodSpec{
				TerminationGracePeriodSeconds: &terminationGrace,
				Containers: []corev1.Container{
					{
						Name:            "runtime",
						Image:           csms.Spec.Image,
						ImagePullPolicy: corev1.PullIfNotPresent,
						EnvFrom:         envFrom,
						Env: []corev1.EnvVar{
							{
								Name: "CSMS_INSTANCE_ID",
								ValueFrom: &corev1.EnvVarSource{
									FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"},
								},
							},
						},
						Ports: []corev1.ContainerPort{
							{Name: "http", ContainerPort: 8080},
						},
						ReadinessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								HTTPGet: &corev1.HTTPGetAction{Path: "/readyz", Port: intstr.FromString("http")},
							},
							PeriodSeconds: 5,
						},
						LivenessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								HTTPGet: &corev1.HTTPGetAction{Path: "/livez", Port: intstr.FromString("http")},
							},
							PeriodSeconds: 10,
						},
						Resources: resources,
					},
				},
			},
		}
		return controllerutil.SetControllerReference(csms, deployment, r.Scheme)
	})
	if err != nil {
		return nil, err
	}
	return deployment, nil
}

func (r *CSMSReconciler) reconcilePodDisruptionBudget(ctx context.Context, csms *csmsv1alpha1.CSMS, labels map[string]string) error {
	pdb := &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Name: csms.Name, Namespace: csms.Namespace},
	}

	if csms.Spec.MinAvailable == nil {
		err := r.Delete(ctx, pdb)
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		return nil
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, pdb, func() error {
		pdb.Labels = labels
		pdb.Spec.MinAvailable = csms.Spec.MinAvailable
		pdb.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
		return controllerutil.SetControllerReference(csms, pdb, r.Scheme)
	})
	return err
}

// reconcileIngress creates or updates an Ingress routing csms.Spec.Ingress.Host
// to the Runtime Service when set, and deletes any previously created
// Ingress when unset. It never creates or renews the referenced TLS Secret.
func (r *CSMSReconciler) reconcileIngress(ctx context.Context, csms *csmsv1alpha1.CSMS, labels map[string]string) error {
	ingress := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: csms.Name, Namespace: csms.Namespace},
	}

	if csms.Spec.Ingress == nil {
		err := r.Delete(ctx, ingress)
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		return nil
	}

	spec := csms.Spec.Ingress
	pathType := networkingv1.PathTypePrefix

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, ingress, func() error {
		ingress.Labels = labels
		ingress.Annotations = spec.Annotations
		if spec.IngressClassName != "" {
			ingress.Spec.IngressClassName = &spec.IngressClassName
		} else {
			ingress.Spec.IngressClassName = nil
		}
		ingress.Spec.Rules = []networkingv1.IngressRule{
			{
				Host: spec.Host,
				IngressRuleValue: networkingv1.IngressRuleValue{
					HTTP: &networkingv1.HTTPIngressRuleValue{
						Paths: []networkingv1.HTTPIngressPath{
							{
								Path:     "/",
								PathType: &pathType,
								Backend: networkingv1.IngressBackend{
									Service: &networkingv1.IngressServiceBackend{
										Name: csms.Name,
										Port: networkingv1.ServiceBackendPort{Name: "http"},
									},
								},
							},
						},
					},
				},
			},
		}
		if spec.TLSSecretName != "" {
			ingress.Spec.TLS = []networkingv1.IngressTLS{
				{Hosts: []string{spec.Host}, SecretName: spec.TLSSecretName},
			}
		} else {
			ingress.Spec.TLS = nil
		}
		return controllerutil.SetControllerReference(csms, ingress, r.Scheme)
	})
	return err
}

func (r *CSMSReconciler) updateStatus(ctx context.Context, csms *csmsv1alpha1.CSMS, deployment *appsv1.Deployment) error {
	desired := int32(1)
	if csms.Spec.Replicas != nil {
		desired = *csms.Spec.Replicas
	}

	available := metav1.Condition{
		Type:               csmsv1alpha1.CSMSConditionAvailable,
		Status:             metav1.ConditionFalse,
		Reason:             "NoReadyReplicas",
		Message:            "no Runtime replica is ready",
		ObservedGeneration: csms.Generation,
	}
	if deployment.Status.ReadyReplicas > 0 {
		available.Status = metav1.ConditionTrue
		available.Reason = "ReplicasReady"
		available.Message = fmt.Sprintf("%d/%d replicas ready", deployment.Status.ReadyReplicas, desired)
	}

	progressing := metav1.Condition{
		Type:               csmsv1alpha1.CSMSConditionProgressing,
		Status:             metav1.ConditionTrue,
		Reason:             "ReplicasNotReady",
		Message:            fmt.Sprintf("%d/%d replicas ready", deployment.Status.ReadyReplicas, desired),
		ObservedGeneration: csms.Generation,
	}
	if deployment.Status.ReadyReplicas == desired {
		progressing.Status = metav1.ConditionFalse
		progressing.Reason = "ReplicasReady"
	}

	csms.Status.Replicas = deployment.Status.Replicas
	csms.Status.ReadyReplicas = deployment.Status.ReadyReplicas
	csms.Status.ObservedGeneration = csms.Generation
	meta.SetStatusCondition(&csms.Status.Conditions, available)
	meta.SetStatusCondition(&csms.Status.Conditions, progressing)

	return r.Status().Update(ctx, csms)
}

// SetupWithManager registers the reconciler with the controller manager.
func (r *CSMSReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&csmsv1alpha1.CSMS{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&policyv1.PodDisruptionBudget{}).
		Owns(&networkingv1.Ingress{}).
		Complete(r)
}
