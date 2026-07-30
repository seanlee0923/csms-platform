package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// CSMSConfig holds csms-runtime environment variable overrides applied
// through the ConfigMap the Operator manages.
type CSMSConfig struct {
	// LogLevel is the CSMS_LOG_LEVEL value: debug, info, warn or error.
	// +optional
	// +kubebuilder:default="info"
	LogLevel string `json:"logLevel,omitempty"`

	// HeartbeatIntervalSeconds is the BootNotification interval in seconds
	// (CSMS_HEARTBEAT_INTERVAL).
	// +optional
	// +kubebuilder:default=300
	HeartbeatIntervalSeconds *int32 `json:"heartbeatIntervalSeconds,omitempty"`

	// ShutdownTimeout is the graceful shutdown timeout (CSMS_SHUTDOWN_TIMEOUT),
	// for example "30s".
	// +optional
	// +kubebuilder:default="30s"
	ShutdownTimeout string `json:"shutdownTimeout,omitempty"`

	// CommandRateLimit is the per-credential command API requests per minute
	// (CSMS_COMMAND_RATE_LIMIT).
	// +optional
	// +kubebuilder:default=60
	CommandRateLimit *int32 `json:"commandRateLimit,omitempty"`

	// SessionLeaseTTL is the Redis session ownership TTL
	// (CSMS_SESSION_LEASE_TTL), for example "30s".
	// +optional
	// +kubebuilder:default="30s"
	SessionLeaseTTL string `json:"sessionLeaseTTL,omitempty"`

	// SessionRenewInterval is the session ownership renew period
	// (CSMS_SESSION_RENEW_INTERVAL). Must be shorter than SessionLeaseTTL.
	// +optional
	// +kubebuilder:default="10s"
	SessionRenewInterval string `json:"sessionRenewInterval,omitempty"`
}

// CSMSSpec defines the desired state of a CSMS Runtime deployment.
// +kubebuilder:validation:XValidation:rule="!(self.replicas > 1) || size(self.redisSecretName) > 0",message="redisSecretName is required when replicas is greater than 1: Runtime session state is process-local without distributed session ownership"
type CSMSSpec struct {
	// Image is the csms-runtime container image, for example
	// "csms-runtime:0.1.0".
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image"`

	// Replicas is the desired Runtime replica count.
	// +optional
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	Replicas *int32 `json:"replicas,omitempty"`

	// DatabaseSecretName references an existing Secret providing
	// CSMS_MYSQL_DSN. The Operator does not create this Secret. Leave empty
	// to run without MySQL persistence.
	// +optional
	// +kubebuilder:default=""
	DatabaseSecretName string `json:"databaseSecretName,omitempty"`

	// RedisSecretName references an existing Secret providing
	// CSMS_REDIS_URL. The Operator does not create this Secret. Leave empty
	// to disable distributed session ownership; Replicas must then be 1.
	// +optional
	// +kubebuilder:default=""
	RedisSecretName string `json:"redisSecretName,omitempty"`

	// APISecretName references an existing Secret providing CSMS_API_KEY
	// and/or CSMS_API_KEYS. The Operator does not create this Secret. Leave
	// empty to keep the command API disabled.
	// +optional
	// +kubebuilder:default=""
	APISecretName string `json:"apiSecretName,omitempty"`

	// Config holds Runtime environment variable overrides.
	// +optional
	Config CSMSConfig `json:"config,omitempty"`

	// Resources sets the Runtime container resource requests and limits.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// MinAvailable configures a PodDisruptionBudget for the Runtime
	// Deployment. Leave unset to skip creating a PodDisruptionBudget.
	// +optional
	MinAvailable *intstr.IntOrString `json:"minAvailable,omitempty"`

	// Ingress optionally exposes the Runtime Service through a Kubernetes
	// Ingress. Leave unset to skip creating an Ingress entirely — most
	// deployments terminate TLS at a platform-managed Ingress/Gateway that
	// targets the Runtime Service directly, without the Operator's
	// involvement. Set this only when a single CSMS resource should own its
	// own Ingress end to end.
	// +optional
	Ingress *CSMSIngress `json:"ingress,omitempty"`
}

// CSMSIngress configures an optional Ingress for the Runtime Service. The
// Operator never creates or renews the referenced TLS Secret; a
// cert-manager Certificate or an externally provisioned Secret must already
// exist.
type CSMSIngress struct {
	// Host is the DNS hostname routed to the Runtime Service.
	// +kubebuilder:validation:MinLength=1
	Host string `json:"host"`

	// IngressClassName selects the Ingress controller, for example "nginx".
	// Leave empty to use the cluster default IngressClass.
	// +optional
	IngressClassName string `json:"ingressClassName,omitempty"`

	// TLSSecretName references an existing Secret with a TLS certificate for
	// Host. The Operator does not create this Secret. Leave empty to serve
	// plain HTTP through the Ingress, which is not appropriate for OCPP
	// endpoints reachable outside a trusted network.
	// +optional
	TLSSecretName string `json:"tlsSecretName,omitempty"`

	// Annotations are applied verbatim to the generated Ingress, for example
	// WebSocket timeout or buffering settings specific to the Ingress
	// controller in use.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// CSMSConditionType enumerates status condition types reported for a CSMS.
const (
	// CSMSConditionAvailable indicates the Runtime Deployment has at least
	// one ready replica.
	CSMSConditionAvailable = "Available"
	// CSMSConditionProgressing indicates the Runtime Deployment has not yet
	// reached the desired replica count.
	CSMSConditionProgressing = "Progressing"
)

// CSMSStatus reflects the observed state of a CSMS Runtime deployment.
type CSMSStatus struct {
	// Replicas is the observed Deployment replica count.
	// +optional
	Replicas int32 `json:"replicas,omitempty"`

	// ReadyReplicas is the observed Deployment ready replica count.
	// +optional
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`

	// ObservedGeneration is the CSMS generation the Operator last
	// reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions holds the latest observed status conditions.
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Image",type=string,JSONPath=`.spec.image`
// +kubebuilder:printcolumn:name="Replicas",type=integer,JSONPath=`.spec.replicas`
// +kubebuilder:printcolumn:name="Ready",type=integer,JSONPath=`.status.readyReplicas`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// CSMS is the Schema for the csms API. It describes a csms-runtime
// deployment: the Runtime Deployment, Service, ConfigMap and, optionally, a
// PodDisruptionBudget. The Operator never creates the MySQL, Redis or API
// key Secrets it references; those remain externally managed.
type CSMS struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CSMSSpec   `json:"spec,omitempty"`
	Status CSMSStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// CSMSList contains a list of CSMS resources.
type CSMSList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CSMS `json:"items"`
}

func init() {
	SchemeBuilder.Register(&CSMS{}, &CSMSList{})
}
