// Package network resolves the network a SkillEval's trials get (mode, hosts,
// enforcement) from the skill's declaration and the platform policy, and
// renders the CiliumNetworkPolicies that enforce it. Everything here is pure:
// the controller supplies what it read from the cluster.
package network

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
)

// OCI manifest annotations a skill declares its network with. demo/push-skill.sh
// copies them from the SKILL.md frontmatter metadata (oras push --annotation).
const (
	// AnnotationMode holds none, allowlist or full.
	AnnotationMode = "skillci.dev/network"
	// AnnotationAllow holds comma separated hosts and @bundles, for allowlist.
	AnnotationAllow = "skillci.dev/network-allow"
)

// Enforcement says how network modes are enforced.
type Enforcement string

// Enforcement settings (--network-enforcement).
const (
	// EnforcementAuto uses Cilium when the CiliumNetworkPolicy CRD exists.
	EnforcementAuto Enforcement = "auto"
	// EnforcementCilium requires Cilium.
	EnforcementCilium Enforcement = "cilium"
	// EnforcementOff creates no policies; every mode runs unenforced.
	EnforcementOff Enforcement = "off"
)

// Reasons of the NetworkPolicy condition.
const (
	// ReasonEnforced means a CiliumNetworkPolicy enforces the mode.
	ReasonEnforced = "Enforced"
	// ReasonNotEnforced means the mode runs without a policy of skill-ci.
	ReasonNotEnforced = "NotEnforced"
	// ReasonNotAllowed means the platform does not allow the declared mode.
	ReasonNotAllowed = "NotAllowed"
	// ReasonNoCilium means the mode needs Cilium and the cluster has none.
	ReasonNoCilium = "NoCilium"
	// ReasonUnknownBundle means the skill names a bundle the platform does not define.
	ReasonUnknownBundle = "UnknownBundle"
	// ReasonInvalidAllowlist means the declared hosts are malformed or too many.
	ReasonInvalidAllowlist = "InvalidAllowlist"
	// ReasonUnknownModelHost means no model API host is known for the entry.
	ReasonUnknownModelHost = "UnknownModelHost"
)

// MaxAllowEntries caps the declared hosts after bundle expansion.
const MaxAllowEntries = 20

// Modes lists every mode, most restrictive first.
var Modes = []string{v1alpha1.NetworkNone, v1alpha1.NetworkAllowlist, v1alpha1.NetworkFull}

// DefaultBundles are the host bundles a skill can name as @<bundle>.
var DefaultBundles = map[string][]string{
	"python": {"pypi.org", "files.pythonhosted.org"},
	"node":   {"registry.npmjs.org"},
	"github": {"github.com", "api.github.com", "codeload.github.com", "objects.githubusercontent.com"},
	"apt":    {"deb.debian.org", "security.debian.org"},
}

// DefaultPreinstalledAgents are the agent@version pins the sandbox image ships.
var DefaultPreinstalledAgents = []string{"claude-code@2.1.292", "codex@0.161.0", "opencode@1.19.1"}

// DefaultAgentInstallHosts are the hosts an agent install needs (Harbor installs with npm).
var DefaultAgentInstallHosts = []string{"registry.npmjs.org"}

// Policy is the platform's network policy, from the controller flags.
type Policy struct {
	// Default is the mode of a skill that declares none; empty means full.
	Default string
	// Allowed are the modes a skill may use; empty means all of them.
	Allowed []string
	// Enforcement says how modes are enforced; empty means auto.
	Enforcement Enforcement
	// AlwaysAllow are hosts added to none and allowlist.
	AlwaysAllow []string
	// Bundles maps a bundle name, without @, to its hosts.
	Bundles map[string][]string
	// Preinstalled are the agent@version pins in the sandbox image.
	Preinstalled []string
	// InstallHosts are added to none and allowlist when the entry's agent
	// version is not preinstalled.
	InstallHosts []string
}

// DefaultMode returns the mode of a skill that declares none.
func (p Policy) DefaultMode() string {
	if p.Default == "" {
		return v1alpha1.NetworkFull
	}
	return p.Default
}

// AllowedModes returns the modes a skill may use.
func (p Policy) AllowedModes() []string {
	if len(p.Allowed) == 0 {
		return Modes
	}
	return p.Allowed
}

// EnforcementMode returns the enforcement setting, auto when unset.
func (p Policy) EnforcementMode() Enforcement {
	if p.Enforcement == "" {
		return EnforcementAuto
	}
	return p.Enforcement
}

// Cilium reports whether network policies are enforced with Cilium, given
// whether the CiliumNetworkPolicy CRD exists.
func (p Policy) Cilium(crdPresent bool) bool {
	switch p.EnforcementMode() {
	case EnforcementOff:
		return false
	case EnforcementCilium:
		return true
	default:
		return crdPresent
	}
}

// Validate checks the policy flags.
func (p Policy) Validate() error {
	if !slices.Contains(Modes, p.DefaultMode()) {
		return fmt.Errorf("network default %q is not a mode (%s)", p.Default, strings.Join(Modes, ", "))
	}
	for _, m := range p.AllowedModes() {
		if !slices.Contains(Modes, m) {
			return fmt.Errorf("network mode %q is not a mode (%s)", m, strings.Join(Modes, ", "))
		}
	}
	if !slices.Contains(p.AllowedModes(), p.DefaultMode()) {
		return fmt.Errorf("network default %q is not one of the allowed modes (%s)", p.DefaultMode(), strings.Join(p.AllowedModes(), ", "))
	}
	switch p.EnforcementMode() {
	case EnforcementAuto, EnforcementCilium, EnforcementOff:
	default:
		return fmt.Errorf("network enforcement %q must be auto, cilium or off", p.Enforcement)
	}
	for _, h := range slices.Concat(p.AlwaysAllow, p.InstallHosts) {
		if err := ValidateHost(h); err != nil {
			return err
		}
	}
	for name, hosts := range p.Bundles {
		if !bundleName.MatchString(name) {
			return fmt.Errorf("network bundle name %q must be lowercase letters, digits and dashes", name)
		}
		for _, h := range hosts {
			if err := ValidateHost(h); err != nil {
				return fmt.Errorf("network bundle @%s: %w", name, err)
			}
		}
	}
	return nil
}

var (
	bundleName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
	hostLabel  = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
)

// ValidateHost checks one host: a lowercase RFC 1123 hostname with at least
// two labels, optionally starting with "*.".
func ValidateHost(h string) error {
	name := strings.TrimPrefix(h, "*.")
	labels := strings.Split(name, ".")
	ok := len(name) > 0 && len(name) <= 253 && len(labels) >= 2
	for _, l := range labels {
		ok = ok && hostLabel.MatchString(l)
	}
	if !ok {
		return fmt.Errorf(`invalid network host %q: must be a lowercase RFC 1123 hostname, optionally starting with "*."`, h)
	}
	return nil
}

// Declared turns the manifest annotations of a skill digest into the spec of
// its SkillEvals: the declared mode (source skill), or the platform default
// (source default). Allow keeps the declared entries as written.
func Declared(annotations map[string]string, p Policy) *v1alpha1.NetworkSpec {
	spec := &v1alpha1.NetworkSpec{Mode: p.DefaultMode(), Source: v1alpha1.NetworkSourceDefault}
	if m := strings.TrimSpace(annotations[AnnotationMode]); m != "" {
		spec.Mode, spec.Source = strings.ToLower(m), v1alpha1.NetworkSourceSkill
	}
	spec.Allow = SplitList(annotations[AnnotationAllow])
	return spec
}

// SplitList splits a comma separated list, trimming blanks and dropping empty entries.
func SplitList(s string) []string {
	var out []string
	for item := range strings.SplitSeq(s, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// Entry is the model list entry a run evaluates with.
type Entry struct {
	// Agent is the harness: claude-code, codex or opencode.
	Agent string
	// AgentVersion is the agent CLI release; empty means the default pin.
	AgentVersion string
	// Model is the model name.
	Model string
}

// Effective is the network a run's trials get.
type Effective struct {
	// Mode is none, allowlist or full.
	Mode string
	// Source is skill or default.
	Source string
	// Hosts are the hosts the trials may reach on TCP 443 (none and
	// allowlist), sorted; empty for full.
	Hosts []string
	// Cilium reports whether a CiliumNetworkPolicy enforces the mode.
	Cilium bool
	// Note is set when part of the declaration was ignored, for example
	// network-allow next to mode none or full.
	Note string
}

// Error is a run that cannot start with its network. Reason is the reason of
// the NetworkPolicy condition, Message the run's failure message.
type Error struct {
	// Reason is one of the Reason constants.
	Reason string
	// Message is the failure message of the SkillEval.
	Message string
}

// Error implements error.
func (e *Error) Error() string { return e.Message }

// Input is what Resolve needs besides the policy.
type Input struct {
	// Spec is the SkillEval's network; nil is a run without a declaration,
	// which gets the platform default.
	Spec *v1alpha1.NetworkSpec
	// Entry is the model list entry of the run.
	Entry Entry
	// OpenAIBaseURL is the llm-keys openai-base-url value, empty when unset.
	OpenAIBaseURL string
	// CiliumCRD reports whether the CiliumNetworkPolicy CRD exists.
	CiliumCRD bool
}

// Resolve computes the effective network of a run. It returns an *Error for
// a run that must fail without a Job.
func Resolve(p Policy, in Input) (Effective, error) {
	spec := in.Spec
	if spec == nil {
		spec = &v1alpha1.NetworkSpec{Mode: p.DefaultMode(), Source: v1alpha1.NetworkSourceDefault}
	}
	eff := Effective{Mode: spec.Mode, Source: spec.Source, Cilium: p.Cilium(in.CiliumCRD)}
	if len(spec.Allow) > 0 && spec.Mode != v1alpha1.NetworkAllowlist {
		eff.Note = fmt.Sprintf("%s ignored: it is used only with mode allowlist", AnnotationAllow)
	}
	if !slices.Contains(p.AllowedModes(), eff.Mode) {
		return Effective{}, &Error{Reason: ReasonNotAllowed, Message: fmt.Sprintf(
			"network mode %q is not allowed on this platform (allowed: %s)", eff.Mode, strings.Join(p.AllowedModes(), ", "))}
	}
	if p.EnforcementMode() == EnforcementCilium && !in.CiliumCRD {
		return Effective{}, &Error{Reason: ReasonNoCilium, Message: "network enforcement is cilium, but the CiliumNetworkPolicy CRD was not found"}
	}
	if eff.Mode == v1alpha1.NetworkFull {
		return eff, nil
	}
	if p.EnforcementMode() == EnforcementAuto && !in.CiliumCRD {
		return Effective{}, &Error{Reason: ReasonNoCilium, Message: fmt.Sprintf(
			"network mode %q needs Cilium (CiliumNetworkPolicy CRD not found); this cluster can only run full", eff.Mode)}
	}
	hosts, err := ModelHosts(in.Entry, in.OpenAIBaseURL)
	if err != nil {
		return Effective{}, err
	}
	hosts = append(hosts, p.AlwaysAllow...)
	if !p.preinstalled(in.Entry) {
		hosts = append(hosts, p.InstallHosts...)
	}
	if eff.Mode == v1alpha1.NetworkAllowlist {
		declared, err := p.Expand(spec.Allow)
		if err != nil {
			return Effective{}, err
		}
		hosts = append(hosts, declared...)
	}
	eff.Hosts = sortedUnique(hosts)
	return eff, nil
}

// preinstalled reports whether the entry's agent release is in the sandbox
// image. An entry without a version runs the default pin, which is.
func (p Policy) preinstalled(e Entry) bool {
	if e.AgentVersion == "" {
		return true
	}
	return slices.Contains(p.Preinstalled, e.Agent+"@"+e.AgentVersion)
}

// Expand replaces the @bundles of a declared list by their hosts, validates
// every host and enforces MaxAllowEntries.
func (p Policy) Expand(allow []string) ([]string, error) {
	var hosts []string
	for _, a := range allow {
		name, isBundle := strings.CutPrefix(a, "@")
		if !isBundle {
			if err := ValidateHost(a); err != nil {
				return nil, &Error{Reason: ReasonInvalidAllowlist, Message: err.Error()}
			}
			hosts = append(hosts, a)
			continue
		}
		bundle, ok := p.Bundles[name]
		if !ok {
			return nil, &Error{Reason: ReasonUnknownBundle, Message: fmt.Sprintf(
				"unknown network bundle %q (defined: %s)", a, p.bundleList())}
		}
		hosts = append(hosts, bundle...)
	}
	hosts = sortedUnique(hosts)
	if len(hosts) > MaxAllowEntries {
		return nil, &Error{Reason: ReasonInvalidAllowlist, Message: fmt.Sprintf(
			"network allowlist has %d hosts after bundle expansion, at most %d are allowed", len(hosts), MaxAllowEntries)}
	}
	return hosts, nil
}

// bundleList renders the defined bundles, "@apt, @github, @node, @python".
func (p Policy) bundleList() string {
	names := make([]string, 0, len(p.Bundles))
	for n := range p.Bundles {
		names = append(names, "@"+n)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

// Hosts of the model APIs, the same table as runner/skill_eval.py GATEWAY_PROVIDERS.
const (
	hostAnthropic  = "api.anthropic.com"
	hostOpenAI     = "api.openai.com"
	hostOpenRouter = "openrouter.ai"
	hostOllama     = "ollama.com"
	hostNvidia     = "integrate.api.nvidia.com"
)

// ModelHosts returns the model API host the entry's agent talks to.
// openAIBaseURL replaces api.openai.com when it is set.
func ModelHosts(e Entry, openAIBaseURL string) ([]string, error) {
	openai := func() ([]string, error) {
		if strings.TrimSpace(openAIBaseURL) == "" {
			return []string{hostOpenAI}, nil
		}
		u, err := url.Parse(strings.TrimSpace(openAIBaseURL))
		if err != nil || u.Hostname() == "" {
			return nil, &Error{Reason: ReasonUnknownModelHost, Message: fmt.Sprintf(
				"llm-keys openai-base-url %q has no host", openAIBaseURL)}
		}
		return []string{strings.ToLower(u.Hostname())}, nil
	}
	switch e.Agent {
	case "claude-code":
		return []string{hostAnthropic}, nil
	case "codex":
		return openai()
	case "opencode":
		provider, _, _ := strings.Cut(e.Model, "/")
		switch provider {
		case "anthropic":
			return []string{hostAnthropic}, nil
		case "openai":
			return openai()
		case "openrouter":
			return []string{hostOpenRouter}, nil
		case "ollama-cloud":
			return []string{hostOllama}, nil
		case "nvidia":
			return []string{hostNvidia}, nil
		}
	}
	return nil, &Error{Reason: ReasonUnknownModelHost, Message: fmt.Sprintf(
		"no model API host is known for agent %q with model %q, so it cannot run with network none or allowlist", e.Agent, e.Model)}
}

// NeedsOpenAIBaseURL reports whether the entry's model host depends on the
// llm-keys openai-base-url value.
func NeedsOpenAIBaseURL(e Entry) bool {
	return e.Agent == "codex" || (e.Agent == "opencode" && strings.HasPrefix(e.Model, "openai/"))
}

func sortedUnique(in []string) []string {
	out := slices.Clone(in)
	slices.Sort(out)
	return slices.Compact(out)
}

// ParseModes reads --network-modes, a comma separated list of modes.
func ParseModes(s string) ([]string, error) {
	modes := SplitList(s)
	if len(modes) == 0 {
		return nil, fmt.Errorf("no network modes in %q", s)
	}
	for _, m := range modes {
		if !slices.Contains(Modes, m) {
			return nil, fmt.Errorf("network mode %q is not a mode (%s)", m, strings.Join(Modes, ", "))
		}
	}
	return modes, nil
}

// ParseBundles reads --network-bundles: bundles separated by semicolons, each
// name=host,host. An empty string defines no bundles.
func ParseBundles(s string) (map[string][]string, error) {
	out := map[string][]string{}
	for item := range strings.SplitSeq(s, ";") {
		if item = strings.TrimSpace(item); item == "" {
			continue
		}
		name, hosts, ok := strings.Cut(item, "=")
		name = strings.TrimPrefix(strings.TrimSpace(name), "@")
		if !ok || name == "" {
			return nil, fmt.Errorf("network bundle %q must look like name=host,host", item)
		}
		if _, dup := out[name]; dup {
			return nil, fmt.Errorf("network bundle @%s is defined twice", name)
		}
		out[name] = SplitList(hosts)
	}
	return out, nil
}

// FormatBundles renders bundles the way ParseBundles reads them, sorted by name.
func FormatBundles(b map[string][]string) string {
	names := make([]string, 0, len(b))
	for n := range b {
		names = append(names, n)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, n := range names {
		parts = append(parts, n+"="+strings.Join(b[n], ","))
	}
	return strings.Join(parts, ";")
}
