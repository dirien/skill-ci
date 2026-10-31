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

// Observability holds where the runner reads the trials' network flows.
type Observability struct {
	// HubbleRelay is host:port of a Hubble Relay the runner pods can reach,
	// for example hubble-relay.kube-system.svc.cluster.local:80: a hostname
	// or IP and a port from 1 to 65535, no scheme. The controller passes it
	// to new runner Jobs as HUBBLE_SERVER. Empty (the default) collects no
	// network flows.
	// +optional
	// +kubebuilder:default=""
	// +kubebuilder:validation:MaxLength=255
	// +kubebuilder:validation:Pattern=`^$|^([a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?(\.[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?)*|\[[0-9a-fA-F:.]+\]):[0-9]{1,5}$`
	HubbleRelay string `json:"hubbleRelay,omitempty"`
}

// Judge providers skill-ci supports (SKILL_EVAL_LLM_PROVIDER). SkillEvaluator
// also knows bedrock, which no agent of the runner can be scored with.
const (
	// JudgeAnthropic is the Anthropic Messages API, the default judge.
	JudgeAnthropic = "anthropic"
	// JudgeOpenAI is the OpenAI Chat Completions API.
	JudgeOpenAI = "openai"
	// JudgeOpenAICompatible is any OpenAI-compatible endpoint at judge.baseURL.
	JudgeOpenAICompatible = "openai-compatible"
	// JudgeNVBuild is NVIDIA's build.nvidia.com endpoint.
	JudgeNVBuild = "nv_build"
)

// DefaultJudgeSecretKey is the llm-keys key of the default judge's API key.
const DefaultJudgeSecretKey = "anthropic"

// Judge is the LLM judge that scores every trial, whatever the agent under
// test. The controller passes it to new runner Jobs as SKILL_EVAL_LLM_PROVIDER,
// SKILL_EVAL_LLM_MODEL, SKILL_EVAL_LLM_BASE_URL and SKILL_EVAL_LLM_API_KEY
// (from llm-keys/<secretKey>), and allows its host in the network modes none
// and allowlist.
// +kubebuilder:validation:XValidation:rule="!has(self.provider) || self.provider != 'openai-compatible' || (has(self.baseURL) && size(self.baseURL) > 0)",message="judge.baseURL is required for provider openai-compatible"
// +kubebuilder:validation:XValidation:rule="!has(self.provider) || self.provider != 'nv_build' || !has(self.baseURL) || size(self.baseURL) == 0",message="judge.baseURL is used only with the providers anthropic, openai and openai-compatible"
type Judge struct {
	// Provider is the judge's API: anthropic (default), openai,
	// openai-compatible (needs baseURL) or nv_build.
	// +optional
	// +kubebuilder:default=anthropic
	// +kubebuilder:validation:Enum=anthropic;openai;openai-compatible;nv_build
	Provider string `json:"provider,omitempty"`
	// Model is the judge model; empty means SkillEvaluator's default for the provider.
	// +optional
	// +kubebuilder:validation:MaxLength=256
	// +kubebuilder:validation:Pattern=`^$|^[A-Za-z0-9][A-Za-z0-9._:/@+-]*$`
	Model string `json:"model,omitempty"`
	// BaseURL is the API root of the judge, an http or https URL without
	// credentials, query or fragment. Required for openai-compatible, optional
	// for anthropic and openai, not used by nv_build.
	// +optional
	// +kubebuilder:validation:MaxLength=2048
	// +kubebuilder:validation:Pattern=`^$|^https?://[^\s?#@]+$`
	BaseURL string `json:"baseURL,omitempty"`
	// SecretKey is the key of the llm-keys Secret that holds the judge's API key.
	// +optional
	// +kubebuilder:default=anthropic
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[-._a-zA-Z0-9]+$`
	SecretKey string `json:"secretKey,omitempty"`
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
	// Observability says where the runner reads network flows.
	// +optional
	// +kubebuilder:default={}
	Observability Observability `json:"observability,omitempty"`
	// Judge is the LLM judge of every run (default: anthropic, its key in llm-keys anthropic).
	// +optional
	// +kubebuilder:default={}
	Judge Judge `json:"judge,omitempty"`
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

// EffectiveObservability is the observability setting in force.
type EffectiveObservability struct {
	// HubbleRelay is the HUBBLE_SERVER of new runner Jobs; empty collects no flows.
	// +optional
	HubbleRelay string `json:"hubbleRelay,omitempty"`
}

// EffectiveJudge is the judge in force.
type EffectiveJudge struct {
	// Provider is the judge's API.
	// +optional
	Provider string `json:"provider,omitempty"`
	// Model is the judge model; empty means SkillEvaluator's default.
	// +optional
	Model string `json:"model,omitempty"`
	// BaseURL is the judge's API root; empty means the provider's default.
	// +optional
	BaseURL string `json:"baseURL,omitempty"`
	// SecretKey is the llm-keys key of the judge's API key.
	// +optional
	SecretKey string `json:"secretKey,omitempty"`
	// Host is the judge host allowed in the network modes none and allowlist.
	// +optional
	Host string `json:"host,omitempty"`
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
	// Observability is the observability setting in force.
	// +optional
	Observability EffectiveObservability `json:"observability,omitempty"`
	// Judge is the judge in force.
	// +optional
	Judge EffectiveJudge `json:"judge,omitempty"`
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
// default SandboxProfile, the network policy of the trial sandboxes, the
// Hubble Relay of the network flows and the LLM judge. The
// controller reads only the object named default in its own namespace and
// applies a change to the runs it resolves afterwards, without a restart.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=scc
// +kubebuilder:printcolumn:name="Default-Network",type=string,JSONPath=`.status.effective.network.default`
// +kubebuilder:printcolumn:name="Enforcement",type=string,JSONPath=`.status.effective.network.enforcedBy`
// +kubebuilder:printcolumn:name="Default-Profile",type=string,JSONPath=`.status.effective.defaultSandboxProfile`
// +kubebuilder:printcolumn:name="Judge",type=string,JSONPath=`.status.effective.judge.provider`
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
