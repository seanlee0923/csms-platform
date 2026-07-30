package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// CSMSSpec defines the desired state of an OCPP runtime deployment. The
// Operator treats the container at Image as opaque: any runtime built on
// the ocpp library that listens on Port and serves LivenessPath/
// ReadinessPath can be deployed, not just this repository's reference
// cmd/csms-server.
// +kubebuilder:validation:XValidation:rule="!(self.replicas > 1) || size(self.redisSecretName) > 0",message="redisSecretName is required when replicas is greater than 1: a Runtime with process-local session state cannot safely run more than one replica without distributed session ownership"
type CSMSSpec struct {
	// Image is the OCPP runtime container image, for example
	// "csms-runtime:0.1.0".
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image"`

	// Replicas is the desired Runtime replica count.
	// +optional
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	Replicas *int32 `json:"replicas,omitempty"`

	// Port is the container port the Runtime listens on for HTTP traffic,
	// used for the Service target port and the liveness/readiness probes.
	// +optional
	// +kubebuilder:default=8080
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port,omitempty"`

	// LivenessPath is the HTTP path the Runtime serves for its liveness
	// probe.
	// +optional
	// +kubebuilder:default="/livez"
	// +kubebuilder:validation:Pattern="^/"
	LivenessPath string `json:"livenessPath,omitempty"`

	// ReadinessPath is the HTTP path the Runtime serves for its readiness
	// probe.
	// +optional
	// +kubebuilder:default="/readyz"
	// +kubebuilder:validation:Pattern="^/"
	ReadinessPath string `json:"readinessPath,omitempty"`

	// TerminationGracePeriodSeconds bounds how long Kubernetes waits after
	// SIGTERM before killing the Runtime Pod, giving it time to drain
	// in-flight OCPP sessions and command handling on shutdown.
	// +optional
	// +kubebuilder:default=30
	// +kubebuilder:validation:Minimum=0
	TerminationGracePeriodSeconds *int64 `json:"terminationGracePeriodSeconds,omitempty"`

	// DatabaseSecretName references an existing Secret injected into the
	// Runtime container via envFrom. The Operator does not create this
	// Secret and does not inspect its keys. Leave empty to omit it.
	// +optional
	// +kubebuilder:default=""
	// +kubebuilder:validation:Pattern="^$|^[a-z0-9]([-a-z0-9]*[a-z0-9])?$"
	DatabaseSecretName string `json:"databaseSecretName,omitempty"`

	// RedisSecretName references an existing Secret injected into the
	// Runtime container via envFrom. The Operator does not create this
	// Secret and does not inspect its keys. Leave empty to disable
	// distributed session ownership; Replicas must then be 1.
	// +optional
	// +kubebuilder:default=""
	// +kubebuilder:validation:Pattern="^$|^[a-z0-9]([-a-z0-9]*[a-z0-9])?$"
	RedisSecretName string `json:"redisSecretName,omitempty"`

	// APISecretName references an existing Secret injected into the
	// Runtime container via envFrom. The Operator does not create this
	// Secret and does not inspect its keys. Leave empty to omit it.
	// +optional
	// +kubebuilder:default=""
	// +kubebuilder:validation:Pattern="^$|^[a-z0-9]([-a-z0-9]*[a-z0-9])?$"
	APISecretName string `json:"apiSecretName,omitempty"`

	// Config holds arbitrary environment variables applied through the
	// ConfigMap the Operator manages. Keys and values are opaque to the
	// Operator — it makes no assumption about which env vars any given
	// runtime image reads.
	// +optional
	Config map[string]string `json:"config,omitempty"`

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
	// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$"
	Host string `json:"host"`

	// IngressClassName selects the Ingress controller, for example "nginx".
	// Leave empty to use the cluster default IngressClass.
	// +optional
	// +kubebuilder:validation:Pattern="^$|^[a-z0-9]([-a-z0-9]*[a-z0-9])?$"
	IngressClassName string `json:"ingressClassName,omitempty"`

	// TLSSecretName references an existing Secret with a TLS certificate for
	// Host. The Operator does not create this Secret. Leave empty to serve
	// plain HTTP through the Ingress, which is not appropriate for OCPP
	// endpoints reachable outside a trusted network.
	// +optional
	// +kubebuilder:validation:Pattern="^$|^[a-z0-9]([-a-z0-9]*[a-z0-9])?$"
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

// CSMS is the Schema for the csms API. It describes the deployment of an
// OCPP runtime container: the Runtime Deployment, Service, ConfigMap and,
// optionally, an Ingress and a PodDisruptionBudget. It is not specific to
// this repository's reference cmd/csms-server image — any container that
// honors the Port/LivenessPath/ReadinessPath contract can be deployed. The
// Operator never creates the Secrets it references; those remain
// externally managed.
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
