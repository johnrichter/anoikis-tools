package policy

import (
	"fmt"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/johnrichter/claude-shared-tooling/go/graph"
)

// Domain kinds a policy may declare a surface in.
const (
	DomainPath      = "path"
	DomainNamespace = "namespace"
)

// Prover builds the disjointness prover from the domains this harness
// declares. Only a claim in a declared domain can ever be proven disjoint;
// anything else leaves its node in a batch of one, which is the safe answer.
//
// Path domains are given the same globstar matcher the work itself resolves
// surfaces with, so a pattern like "svc/**/*.go" is decided against a real
// path rather than by its literal prefix — the difference between one batch
// and several. A domain whose glob dialect differed from the runtime's would
// make the proof unsound, which is why the matcher is pinned here rather than
// left to each policy.
func (h *Harness) Prover() (*graph.Prover, error) {
	if len(h.Surfaces) == 0 {
		return nil, fmt.Errorf("policy: at least one resource domain is required")
	}
	domains := make([]graph.Domain, 0, len(h.Surfaces))
	for _, spec := range h.Surfaces {
		d, err := spec.domain()
		if err != nil {
			return nil, err
		}
		domains = append(domains, d)
	}
	return graph.NewProver(domains...)
}

// domain builds one declared domain.
func (d DomainSpec) domain() (graph.Domain, error) {
	switch d.Kind {
	case DomainPath:
		opts := []graph.PathOption{graph.WithPathMatcher(doublestar.Match)}
		if !d.CaseInsensitive {
			opts = append(opts, graph.WithPathFold(nil))
		}
		return graph.PathDomain(d.Name, opts...), nil
	case DomainNamespace:
		opts := []graph.NamespaceOption{}
		if d.Separator != "" {
			opts = append(opts, graph.WithNamespaceSeparator(d.Separator))
		}
		if d.CaseInsensitive {
			opts = append(opts, graph.WithNamespaceFold(strings.ToLower))
		}
		return graph.NamespaceDomain(d.Name, opts...), nil
	default:
		return graph.Domain{}, fmt.Errorf("policy: surface %q declares unknown domain kind %q", d.Name, d.Kind)
	}
}

// DeclaresDomain reports whether the policy registered a domain by that name.
func (h *Harness) DeclaresDomain(name string) bool {
	_, ok := h.DomainKind(name)
	return ok
}

// DomainKind returns the kind of the named domain, and false when the policy
// declares no such domain.
func (h *Harness) DomainKind(name string) (string, bool) {
	for _, s := range h.Surfaces {
		if s.Name == name {
			return s.Kind, true
		}
	}
	return "", false
}

// PathDomains names every declared domain over file paths — the only domains
// a change set landed by a merge can be re-asserted against, since a
// namespace claim has no counterpart in a diff.
func (h *Harness) PathDomains() []string {
	var out []string
	for _, s := range h.Surfaces {
		if s.Kind == DomainPath {
			out = append(out, s.Name)
		}
	}
	return out
}

// PathClaimKinds are the claim kinds a path domain decides. A claim of any
// other kind neither proves disjointness nor covers a changed path.
var PathClaimKinds = []string{graph.PathFile, graph.PathDir, graph.PathGlob}
