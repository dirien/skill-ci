package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SkillCIConfigName is the name of the only SkillCIConfig the controller
// reads, in its own namespace. Objects with another name are ignored.
const SkillCIConfigName = "default"

// Sources of the configuration in force (status.effective.source).
const (
	// ConfigSourceSkillCIConfig means the spec of the SkillCIConfig is in force.
	ConfigSourceSkillCIConfig = "SkillCIConfig"
	// ConfigSourceLastValid means the spec is invalid and the last valid spec
	// the controller saw is still in force.
	ConfigSourceLastValid = "LastValid"
	// ConfigSourceBuiltIn means the built-in defaults are in force.
	ConfigSourceBuiltIn = "BuiltIn"
)

// Enforcement backends a SkillCIConfig resolves to (status.effective.network.enforcedBy).
const (
	// EnforcedByCilium means CiliumNetworkPolicies enforce the network modes.
	EnforcedByCilium = "cilium"
	// EnforcedByNone means skill-ci creates no policies.
	EnforcedByNone = "none"
)

// Host is a host trial sandboxes may reach on TCP 443: a lowercase RFC 1123
// hostname with at least two labels, optionally starting with "*.".
// +kubebuilder:validation:MaxLength=255
// +kubebuilder:validation:Pattern=`^(\*\.)?[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?(\.[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?)+$`
type Host string

// BundleHosts are the hosts of one network bundle.
// +kubebuilder:validation:MinItems=1
// +kubebuilder:validation:MaxItems=20
type BundleHosts []Host

// PlatformNetwork is the platform's network policy for trial sandboxes
// (CONTRACT.md "Network modes").
// +kubebuilder:validation:XValidation:rule="!has(self.modes) || !has(self.default) || self.default in self.modes",message="network.default must be one of network.modes"
type PlatformNetwork struct {
	// Default is the mode of a skill that declares none: none, allowlist or
	// full (the internet, never the cluster).
	// +optional
	// +kubebuilder:default=full
	// +kubebuilder:validation:Enum=none;allowlist;full
	Default string `json:"default,omitempty"`
	// Modes are the modes a skill may declare; a run with another mode fails.
	// +optional
	// +kubebuilder:default={none,allowlist,full}
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=3
	// +kubebuilder:validation:items:Enum=none;allowlist;full
	// +listType=set
	Modes []string `json:"modes,omitempty"`
	// Enforcement says how modes are enforced. auto: CiliumNetworkPolicies
	// when the ciliumnetworkpolicies.cilium.io CRD exists; cilium: require
	// Cilium; off: no policies, every mode runs unenforced.
	// +optional
	// +kubebuilder:default=auto
	// +kubebuilder:validation:Enum=auto;cilium;off
	Enforcement string `json:"enforcement,omitempty"`
	// AlwaysAllow are hosts added to the none and allowlist modes, for example a proxy.
	// +optional
	// +kubebuilder:validation:MaxItems=20
	// +listType=set
	AlwaysAllow []Host `json:"alwaysAllow,omitempty"`
	// Bundles are the host bundles a skill names as @<name> in
	// skillci.dev/network-allow. Setting the field replaces the defaults.
	// +optional
	// +kubebuilder:default={"apt":{"deb.debian.org","security.debian.org"},"github":{"github.com","api.github.com","codeload.github.com","objects.githubusercontent.com"},"node":{"registry.npmjs.org"},"python":{"pypi.org","files.pythonhosted.org"}}
	// +kubebuilder:validation:MaxProperties=32
	// +kubebuilder:validation:XValidation:rule="self.all(k, size(k) <= 63 && k.matches('^[a-z0-9]([a-z0-9-]*[a-z0-9])?$'))",message="bundle names must be at most 63 lowercase letters, digits and dashes"
	Bundles map[string]BundleHosts `json:"bundles,omitempty"`
	// AgentInstallHosts are added to none and allowlist when a model list
	// entry's agent version is not preinstalled in the sandbox image (Harbor
	// installs the CLI with npm then).
	// +optional
	// +kubebuilder:default={"registry.npmjs.org"}
	// +kubebuilder:validation:MaxItems=20
	// +listType=set
	AgentInstallHosts []Host `json:"agentInstallHosts,omitempty"`
}

// SkillCIConfigSpec holds the platform settings of a skill-ci install.
type SkillCIConfigSpec struct {
	// DefaultSandboxProfile is the SandboxProfile of a model list entry that
	// names none. When it does not exist, runs start a bare Sandbox per trial.
	// +optional
	// +kubebuilder:default=default
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	DefaultSandboxProfile string `json:"defaultSandboxProfile,omitempty"`
	// Network is the network policy of the trial sandboxes.
	// +optional
	// +kubebuilder:default={}
	Network PlatformNetwork `json:"network,omitempty"`
}

// EffectiveNetwork summarises the network policy in force.
type EffectiveNetwork struct {
	// Default is the mode of a skill that declares none.
	// +optional
	Default string `json:"default,omitempty"`
	// Modes are the modes a skill may declare.
	// +optional
	// +listType=atomic
	Modes []string `json:"modes,omitempty"`
	// Enforcement is the configured setting: auto, cilium or off.
	// +optional
	Enforcement string `json:"enforcement,omitempty"`
	// EnforcedBy is what enforcement resolves to on this cluster: cilium or none.
	// +optional
	EnforcedBy string `json:"enforcedBy,omitempty"`
	// AlwaysAllow are the hosts added to none and allowlist.
	// +optional
	// +listType=atomic
	AlwaysAllow []string `json:"alwaysAllow,omitempty"`
	// Bundles are the names of the defined bundles, sorted.
	// +optional
	// +listType=atomic
	Bundles []string `json:"bundles,omitempty"`
	// AgentInstallHosts are the hosts added for an agent version that is not preinstalled.
	// +optional
	// +listType=atomic
	AgentInstallHosts []string `json:"agentInstallHosts,omitempty"`
}

// EffectiveConfig is the configuration the controller applies.
type EffectiveConfig struct {
	// Source is SkillCIConfig (this spec), LastValid (this spec is invalid,
	// the last valid one is kept) or BuiltIn (the built-in defaults).
	// +optional
	Source string `json:"source,omitempty"`
	// Generation is the generation of the spec in force; zero for BuiltIn.
	// +optional
	Generation int64 `json:"generation,omitempty"`
	// DefaultSandboxProfile is the SandboxProfile of an entry that names none.
	// +optional
	DefaultSandboxProfile string `json:"defaultSandboxProfile,omitempty"`
	// Network is the network policy in force.
	// +optional
	Network EffectiveNetwork `json:"network,omitempty"`
}

// SkillCIConfigStatus is the observed state of a SkillCIConfig.
type SkillCIConfigStatus struct {
	// ObservedGeneration is the generation the status describes.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Conditions holds Ready: True when the spec is valid and in force.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// Effective is the configuration in force. Empty on an ignored object.
	// +optional
	Effective *EffectiveConfig `json:"effective,omitempty"`
}

// SkillCIConfig holds the platform settings of a skill-ci install: the
// default SandboxProfile and the network policy of the trial sandboxes. The
// controller reads only the object named default in its own namespace and
// applies a change to the runs it resolves afterwards, without a restart.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=scc
// +kubebuilder:printcolumn:name="Default-Network",type=string,JSONPath=`.status.effective.network.default`
// +kubebuilder:printcolumn:name="Enforcement",type=string,JSONPath=`.status.effective.network.enforcedBy`
// +kubebuilder:printcolumn:name="Default-Profile",type=string,JSONPath=`.status.effective.defaultSandboxProfile`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type SkillCIConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +kubebuilder:default={}
	Spec   SkillCIConfigSpec   `json:"spec,omitempty"`
	Status SkillCIConfigStatus `json:"status,omitempty"`
}

// SkillCIConfigList is a list of SkillCIConfigs.
// +kubebuilder:object:root=true
type SkillCIConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SkillCIConfig `json:"items"`
}

// HostStrings converts hosts to plain strings.
func HostStrings(hosts []Host) []string {
	if hosts == nil {
		return nil
	}
	out := make([]string, len(hosts))
	for i, h := range hosts {
		out[i] = string(h)
	}
	return out
}
