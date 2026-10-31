// Package platform holds the platform settings of a skill-ci install (the
// default SandboxProfile, the network policy, the Hubble Relay and the LLM
// judge) and looks them up in the
// SkillCIConfig named default. Precedence: a valid SkillCIConfig default,
// else the last valid spec of that object this process saw, else the
// built-in defaults.
package platform

import (
	"context"
	"fmt"
	"maps"
	"net"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/dirien/skill-ci/controller/api/v1alpha1"
	"github.com/dirien/skill-ci/controller/internal/network"
)

// DefaultSandboxProfile is the built-in default SandboxProfile name.
const DefaultSandboxProfile = "default"

// Settings are the platform settings runs are resolved with.
type Settings struct {
	// DefaultSandboxProfile is the SandboxProfile of a model list entry that names none.
	DefaultSandboxProfile string
	// Network is the network policy of the trial sandboxes.
	Network network.Policy
	// HubbleRelay is host:port of the Hubble Relay new runner Jobs read
	// network flows from (HUBBLE_SERVER); empty collects none.
	HubbleRelay string
	// Judge is the LLM judge of new runs.
	Judge network.Judge
}

// Defaults returns the built-in settings. preinstalled are the agent@version
// pins of the sandbox image (--preinstalled-agents).
func Defaults(preinstalled []string) Settings {
	return Settings{
		DefaultSandboxProfile: DefaultSandboxProfile,
		Network: network.Policy{
			Default:      v1alpha1.NetworkFull,
			Allowed:      slices.Clone(network.Modes),
			Enforcement:  network.EnforcementAuto,
			Bundles:      cloneBundles(network.DefaultBundles),
			Preinstalled: slices.Clone(preinstalled),
			InstallHosts: slices.Clone(network.DefaultAgentInstallHosts),
		},
		Judge: network.DefaultJudge(),
	}
}

// FromSpec validates a SkillCIConfig spec and turns it into settings. Fields
// left out (nil) get the built-in defaults; an explicitly empty bundles map
// or agentInstallHosts list means none. The error says what is wrong in a
// sentence fit for the Ready condition.
func FromSpec(spec v1alpha1.SkillCIConfigSpec, preinstalled []string) (Settings, error) {
	s := Defaults(preinstalled)
	if spec.DefaultSandboxProfile != "" {
		s.DefaultSandboxProfile = spec.DefaultSandboxProfile
	}
	n := spec.Network
	if n.Default != "" {
		s.Network.Default = n.Default
	}
	if len(n.Modes) > 0 {
		s.Network.Allowed = slices.Clone(n.Modes)
	}
	if n.Enforcement != "" {
		s.Network.Enforcement = network.Enforcement(n.Enforcement)
	}
	s.Network.AlwaysAllow = v1alpha1.HostStrings(n.AlwaysAllow)
	if n.Bundles != nil {
		s.Network.Bundles = make(map[string][]string, len(n.Bundles))
		for name, hosts := range n.Bundles {
			s.Network.Bundles[name] = v1alpha1.HostStrings(hosts)
		}
	}
	if n.AgentInstallHosts != nil {
		s.Network.InstallHosts = v1alpha1.HostStrings(n.AgentInstallHosts)
	}
	s.HubbleRelay = spec.Observability.HubbleRelay
	j := spec.Judge
	if j.Provider != "" {
		s.Judge.Provider = j.Provider
	}
	if j.SecretKey != "" {
		s.Judge.SecretKey = j.SecretKey
	}
	s.Judge.Model, s.Judge.BaseURL = j.Model, j.BaseURL
	if err := s.Validate(); err != nil {
		return Settings{}, err
	}
	return s, nil
}

// Validate checks the settings with the messages the Ready condition shows.
func (s Settings) Validate() error {
	if errs := nameErrors(s.DefaultSandboxProfile); len(errs) > 0 {
		return fmt.Errorf("defaultSandboxProfile %q is not a valid object name: %s", s.DefaultSandboxProfile, strings.Join(errs, "; "))
	}
	seen := map[string]bool{}
	for _, m := range s.Network.Allowed {
		if seen[m] {
			return fmt.Errorf("network.modes lists %q twice", m)
		}
		seen[m] = true
	}
	for _, hosts := range [][]string{s.Network.AlwaysAllow, s.Network.InstallHosts} {
		if len(hosts) > network.MaxAllowEntries {
			return fmt.Errorf("network host lists hold at most %d hosts, got %d", network.MaxAllowEntries, len(hosts))
		}
	}
	for name, hosts := range s.Network.Bundles {
		if len(hosts) == 0 {
			return fmt.Errorf("network bundle @%s has no hosts", name)
		}
	}
	if err := s.Network.Validate(); err != nil {
		return fmt.Errorf("invalid network settings: %w", err)
	}
	if err := ValidateHubbleRelay(s.HubbleRelay); err != nil {
		return err
	}
	if err := s.Judge.Validate(); err != nil {
		return fmt.Errorf("invalid judge: %w", err)
	}
	return nil
}

// maxHubbleRelay is the longest observability.hubbleRelay the CRD allows.
const maxHubbleRelay = 255

// ValidateHubbleRelay checks observability.hubbleRelay: empty, or host:port
// with a hostname (lowercase RFC 1123) or an IP (IPv6 in brackets), a port
// from 1 to 65535 and no scheme, at most 255 characters.
func ValidateHubbleRelay(addr string) error {
	if addr == "" {
		return nil
	}
	bad := fmt.Errorf("observability.hubbleRelay %q must be host:port (a hostname or IP and a port from 1 to 65535, no scheme, at most %d characters)", addr, maxHubbleRelay)
	if len(addr) > maxHubbleRelay {
		return bad
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return bad
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
		return bad
	}
	if ip := net.ParseIP(host); ip != nil {
		// An IPv6 address must be in brackets, which SplitHostPort removed.
		if strings.Contains(host, ":") != strings.HasPrefix(addr, "[") {
			return bad
		}
		return nil
	}
	if strings.HasPrefix(addr, "[") || len(nameErrors(host)) > 0 {
		return bad
	}
	return nil
}

// nameErrors checks a DNS subdomain name, the name of a SandboxProfile.
func nameErrors(name string) []string {
	if name == "" {
		return []string{"must not be empty"}
	}
	if len(name) > 253 {
		return []string{"must be at most 253 characters"}
	}
	for label := range strings.SplitSeq(name, ".") {
		if !dnsLabel(label) {
			return []string{"must consist of lowercase letters, digits, '-' and '.', and start and end with a letter or digit"}
		}
	}
	return nil
}

func dnsLabel(l string) bool {
	if l == "" || l[0] == '-' || l[len(l)-1] == '-' {
		return false
	}
	for _, c := range l {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

// State is the outcome of a lookup: the settings in force and where they come from.
type State struct {
	// Settings are the settings in force.
	Settings Settings
	// Source is one of v1alpha1.ConfigSourceSkillCIConfig, ConfigSourceLastValid, ConfigSourceBuiltIn.
	Source string
	// Generation is the generation of the spec in force; zero for BuiltIn.
	Generation int64
	// Found reports whether the SkillCIConfig default exists.
	Found bool
	// Invalid is the validation error of the current spec, nil when it is valid.
	Invalid error
}

// lastValid is the last valid spec seen of one object.
type lastValid struct {
	uid        types.UID
	generation int64
	settings   Settings
}

// Source looks up the platform settings. It reads the SkillCIConfig default
// on every call, so a change applies to the next run without a restart. A
// nil Source, or one without a Reader, returns its static settings.
type Source struct {
	// Reader reads the SkillCIConfig; the manager's API reader keeps every
	// lookup current. Nil disables the lookup (no SkillCIConfig CRD).
	Reader client.Reader
	// Namespace is where the SkillCIConfig lives.
	Namespace string
	// Preinstalled are the agent@version pins of the sandbox image (--preinstalled-agents).
	Preinstalled []string

	static *Settings

	mu   sync.Mutex
	last *lastValid
}

// Static returns a Source that always returns s, for tests and for an
// install without the SkillCIConfig CRD.
func Static(s Settings) *Source {
	return &Source{static: &s, Preinstalled: s.Network.Preinstalled}
}

// builtIn returns the settings used when there is no valid SkillCIConfig.
func (src *Source) builtIn() Settings {
	if src == nil {
		return Defaults(network.DefaultPreinstalledAgents)
	}
	if src.static != nil {
		return *src.static
	}
	return Defaults(src.Preinstalled)
}

// Current returns the settings in force.
func (src *Source) Current(ctx context.Context) (Settings, error) {
	st, err := src.Lookup(ctx)
	return st.Settings, err
}

// Lookup reads the SkillCIConfig default and returns the settings in force.
// A missing object, or a cluster without the CRD, gives the built-in
// defaults; a read error is returned with the built-in defaults.
func (src *Source) Lookup(ctx context.Context) (State, error) {
	if src == nil || src.Reader == nil {
		return State{Settings: src.builtIn(), Source: v1alpha1.ConfigSourceBuiltIn}, nil
	}
	var cfg v1alpha1.SkillCIConfig
	err := src.Reader.Get(ctx, types.NamespacedName{Namespace: src.Namespace, Name: v1alpha1.SkillCIConfigName}, &cfg)
	switch {
	case apierrors.IsNotFound(err) || meta.IsNoMatchError(err):
		return src.Evaluate(nil), nil
	case err != nil:
		return State{Settings: src.builtIn(), Source: v1alpha1.ConfigSourceBuiltIn},
			fmt.Errorf("reading SkillCIConfig %s/%s: %w", src.Namespace, v1alpha1.SkillCIConfigName, err)
	}
	return src.Evaluate(&cfg), nil
}

// Evaluate returns the settings in force given the SkillCIConfig default
// (nil when it does not exist) and remembers a valid spec as the last valid
// one. An invalid spec keeps the last valid spec of the same object, else the
// built-in defaults.
func (src *Source) Evaluate(cfg *v1alpha1.SkillCIConfig) State {
	if src == nil || cfg == nil {
		if src != nil {
			src.mu.Lock()
			src.last = nil
			src.mu.Unlock()
		}
		return State{Settings: src.builtIn(), Source: v1alpha1.ConfigSourceBuiltIn}
	}
	s, err := FromSpec(cfg.Spec, src.builtIn().Network.Preinstalled)
	src.mu.Lock()
	defer src.mu.Unlock()
	if err == nil {
		// A stale copy (an older generation from a cache) never replaces a newer valid spec.
		if src.last == nil || src.last.uid != cfg.UID || src.last.generation <= cfg.Generation {
			src.last = &lastValid{uid: cfg.UID, generation: cfg.Generation, settings: s}
		}
		return State{Settings: s, Source: v1alpha1.ConfigSourceSkillCIConfig, Generation: cfg.Generation, Found: true}
	}
	if src.last != nil && src.last.uid == cfg.UID {
		return State{Settings: src.last.settings, Source: v1alpha1.ConfigSourceLastValid, Generation: src.last.generation, Found: true, Invalid: err}
	}
	return State{Settings: src.builtIn(), Source: v1alpha1.ConfigSourceBuiltIn, Found: true, Invalid: err}
}

// Effective summarises a state for status.effective. enforcedBy is cilium or
// none, what the enforcement setting resolves to on the cluster.
func Effective(st State, enforcedBy string) *v1alpha1.EffectiveConfig {
	p := st.Settings.Network
	return &v1alpha1.EffectiveConfig{
		Source:                st.Source,
		Generation:            st.Generation,
		DefaultSandboxProfile: st.Settings.DefaultSandboxProfile,
		Network: v1alpha1.EffectiveNetwork{
			Default:           p.DefaultMode(),
			Modes:             slices.Clone(p.AllowedModes()),
			Enforcement:       string(p.EnforcementMode()),
			EnforcedBy:        enforcedBy,
			AlwaysAllow:       slices.Clone(p.AlwaysAllow),
			Bundles:           BundleNames(p.Bundles),
			AgentInstallHosts: slices.Clone(p.InstallHosts),
		},
		Observability: v1alpha1.EffectiveObservability{HubbleRelay: st.Settings.HubbleRelay},
		Judge:         EffectiveJudge(st.Settings.Judge),
	}
}

// EffectiveJudge summarises the judge for status.effective.judge. Its host
// is the one allowed in none and allowlist; an openai judge without a
// baseURL shows api.openai.com, which llm-keys openai-base-url replaces when
// a run is resolved.
func EffectiveJudge(j network.Judge) v1alpha1.EffectiveJudge {
	host, err := j.Host("")
	if err != nil {
		host = "" // an invalid judge never gets here: Validate rejects it
	}
	return v1alpha1.EffectiveJudge{
		Provider: j.ProviderName(), Model: j.Model, BaseURL: j.BaseURL, SecretKey: j.Key(), Host: host,
	}
}

// EnforcedBy says what enforces network modes: cilium or none, given
// whether the CiliumNetworkPolicy CRD exists.
func EnforcedBy(p network.Policy, ciliumCRD bool) string {
	if p.Cilium(ciliumCRD) {
		return v1alpha1.EnforcedByCilium
	}
	return v1alpha1.EnforcedByNone
}

// BundleNames returns the bundle names, sorted.
func BundleNames(b map[string][]string) []string {
	names := slices.Collect(maps.Keys(b))
	sort.Strings(names)
	return names
}

func cloneBundles(b map[string][]string) map[string][]string {
	out := make(map[string][]string, len(b))
	for k, v := range b {
		out[k] = slices.Clone(v)
	}
	return out
}
