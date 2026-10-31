package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Defaults of a SandboxProfile, applied by the CRD and again by the controller
// for objects written before the defaults existed.
const (
	// DefaultSandboxCPU is the CPU request of a trial sandbox.
	DefaultSandboxCPU = "1"
	// DefaultSandboxMemory is the memory request and limit of a trial sandbox.
	DefaultSandboxMemory = "2Gi"
	// DefaultWarmPoolReplicas is the number of warm trial pods per profile.
	DefaultWarmPoolReplicas int32 = 2
)

// SandboxResources is the size of one trial sandbox. Memory is both the
// request and the limit; CPU is a request only.
type SandboxResources struct {
	// CPU is the CPU request of the sandbox container.
	// +optional
	// +kubebuilder:default="1"
	CPU *resource.Quantity `json:"cpu,omitempty"`
	// Memory is the memory request and limit of the sandbox container.
	// +optional
	// +kubebuilder:default="2Gi"
	Memory *resource.Quantity `json:"memory,omitempty"`
}

// SandboxWarmPoolSettings says how many trial pods are kept running and unclaimed.
type SandboxWarmPoolSettings struct {
	// Replicas is the number of warm trial pods. Zero keeps none: every trial
	// cold-starts a pod from the template.
	// +optional
	// +kubebuilder:default=2
	// +kubebuilder:validation:Minimum=0
	Replicas *int32 `json:"replicas,omitempty"`
}

// SandboxProfileSpec describes the trial sandboxes of the runs that use the profile.
type SandboxProfileSpec struct {
	// Image is the sandbox image. Empty means the controller's --sandbox-image.
	// +optional
	Image string `json:"image,omitempty"`
	// Resources is the size of one trial sandbox.
	// +optional
	// +kubebuilder:default={}
	Resources SandboxResources `json:"resources,omitempty"`
	// WarmPool sizes the pool of pre-started trial pods.
	// +optional
	// +kubebuilder:default={}
	WarmPool SandboxWarmPoolSettings `json:"warmPool,omitempty"`
	// RuntimeClassName is the RuntimeClass of the trial pods, for example gvisor.
	// +optional
	RuntimeClassName string `json:"runtimeClassName,omitempty"`
	// NodeSelector constrains the nodes trial pods run on.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	// Tolerations of the trial pods.
	// +optional
	// +listType=atomic
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`
}

// SandboxProfileStatus is the observed state of a SandboxProfile.
type SandboxProfileStatus struct {
	// TemplateName is the SandboxTemplate the controller keeps for the profile.
	// +optional
	TemplateName string `json:"templateName,omitempty"`
	// WarmPoolName is the SandboxWarmPool the runner claims trial pods from
	// (SANDBOX_WARM_POOL of the runner Job). Empty while the Agent Sandbox
	// extensions are not installed: runs then start a bare Sandbox per trial.
	// +optional
	WarmPoolName string `json:"warmPoolName,omitempty"`
	// ReadyReplicas is the number of warm trial pods that are ready.
	// +optional
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`
	// ObservedGeneration is the generation the status describes.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Conditions holds Ready.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// SandboxProfile is the kind of trial sandbox a model list entry runs in: the
// image, the size and a warm pool of pre-started pods. The controller keeps a
// SandboxTemplate and a SandboxWarmPool (Agent Sandbox extensions) of the same
// name for it.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=sbp
// +kubebuilder:printcolumn:name="Image",type=string,JSONPath=`.spec.image`
// +kubebuilder:printcolumn:name="CPU",type=string,JSONPath=`.spec.resources.cpu`
// +kubebuilder:printcolumn:name="Memory",type=string,JSONPath=`.spec.resources.memory`
// +kubebuilder:printcolumn:name="Warm",type=integer,JSONPath=`.spec.warmPool.replicas`
// +kubebuilder:printcolumn:name="Ready",type=integer,JSONPath=`.status.readyReplicas`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type SandboxProfile struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +kubebuilder:default={}
	Spec   SandboxProfileSpec   `json:"spec,omitempty"`
	Status SandboxProfileStatus `json:"status,omitempty"`
}

// SandboxProfileList is a list of SandboxProfiles.
// +kubebuilder:object:root=true
type SandboxProfileList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SandboxProfile `json:"items"`
}

// WarmReplicas returns the desired warm pool size, the default when unset.
func (p *SandboxProfile) WarmReplicas() int32 {
	if p.Spec.WarmPool.Replicas == nil {
		return DefaultWarmPoolReplicas
	}
	return max(*p.Spec.WarmPool.Replicas, 0)
}

// CPU returns the CPU request of a trial sandbox, the default when unset.
func (p *SandboxProfile) CPU() resource.Quantity {
	if q := p.Spec.Resources.CPU; q != nil && !q.IsZero() {
		return *q
	}
	return resource.MustParse(DefaultSandboxCPU)
}

// Memory returns the memory request and limit of a trial sandbox, the default when unset.
func (p *SandboxProfile) Memory() resource.Quantity {
	if q := p.Spec.Resources.Memory; q != nil && !q.IsZero() {
		return *q
	}
	return resource.MustParse(DefaultSandboxMemory)
}
