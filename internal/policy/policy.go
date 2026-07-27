// Package policy is the harness seam.
//
// Everything about a particular harness — which stages a node runs, which
// role each stage dispatches to, the gate and review vocabulary, the document
// mirrors, the resource domains a surface may claim, the post-merge backstop
// command — arrives as an injected file and is validated here. The engine
// core holds none of it, so the same binary drives a harness it was never
// built against.
package policy

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"text/template"

	"github.com/johnrichter/anoikis-tools/internal/dag"
	"github.com/johnrichter/anoikis-tools/internal/ids"
	"github.com/johnrichter/anoikis-tools/schemas"
	"github.com/johnrichter/claude-shared-tooling/go/clikit"
	"github.com/johnrichter/claude-shared-tooling/go/docmirror"
)

// SchemaVersion is the policy shape this package reads.
const SchemaVersion = "1.0.0"

// DefaultMaxGroupSize bounds a parallel batch when a policy declares no
// limit of its own.
const DefaultMaxGroupSize = 8

// DefaultBlockingThreshold is the criticality at or above which a finding
// blocks the build when a policy declares no threshold.
const DefaultBlockingThreshold = 15

// Stage is one step of a node's run and the tier it runs at.
type Stage struct {
	Stage         string `json:"stage"`
	Role          string `json:"role"`
	Model         string `json:"model,omitempty"`
	ContextWindow string `json:"context_window,omitempty"`
	Effort        string `json:"effort,omitempty"`
}

// Workflow is the ordered stage list every node runs.
type Workflow struct {
	Stages []Stage `json:"stages"`
}

// Role is one dispatchable agent and the brief it is dispatched with.
type Role struct {
	Agent   string `json:"agent"`
	Brief   string `json:"brief"`
	Builder bool   `json:"builder,omitempty"`
}

// Route is the stage set one deliverable kind runs, and the role that
// reviews it.
type Route struct {
	Stages     []Stage `json:"stages"`
	ReviewRole string  `json:"review_role,omitempty"`
}

// GateTaxonomy is the harness's merge and review vocabulary.
type GateTaxonomy struct {
	MainBranch      string   `json:"main_branch"`
	BuildBranch     string   `json:"build_branch"`
	DeepReviewModes []string `json:"deep_review_modes,omitempty"`
	Verdicts        []string `json:"verdicts"`
	FixVerdict      string   `json:"fix_verdict,omitempty"`
	ReviewRole      string   `json:"review_role,omitempty"`
}

// Mirror is the Markdown view generated beside one canonical JSON artifact.
type Mirror struct {
	Template string `json:"template"`
}

// DomainSpec declares one resource domain a node surface may claim in.
type DomainSpec struct {
	Name            string `json:"name"`
	Kind            string `json:"kind"`
	Separator       string `json:"separator,omitempty"`
	CaseInsensitive bool   `json:"case_insensitive,omitempty"`
}

// Admission bounds how wide a parallel batch may get.
type Admission struct {
	MaxGroupSize int `json:"max_group_size,omitempty"`
}

// Backstop is the post-merge compile check. It runs after every layer merge;
// a policy that declares no command is refused rather than skipping it.
type Backstop struct {
	Command        []string `json:"command"`
	TimeoutSeconds int      `json:"timeout_seconds,omitempty"`
}

// Findings sets where the ranked register splits act-now from deferred.
type Findings struct {
	BlockingThreshold int `json:"blocking_threshold,omitempty"`
}

// Tier is the model/effort band the driving session self-checks against.
type Tier struct {
	FloorModel    string `json:"floor_model"`
	FloorEffort   string `json:"floor_effort,omitempty"`
	CeilingModel  string `json:"ceiling_model"`
	CeilingEffort string `json:"ceiling_effort,omitempty"`
}

// UsageSource is where the spend seam reads session transcripts from.
// Absent, spend reports unknown rather than zero.
type UsageSource struct {
	TranscriptRoot string `json:"transcript_root,omitempty"`
	Scope          string `json:"scope,omitempty"`
	IndexPath      string `json:"index_path,omitempty"`
}

// Harness is one harness's whole injected policy.
type Harness struct {
	SchemaVersion string            `json:"schema_version"`
	Name          string            `json:"name"`
	IDScheme      string            `json:"id_scheme"`
	Workflow      Workflow          `json:"workflow"`
	Roles         map[string]Role   `json:"roles"`
	Routes        map[string]Route  `json:"routes"`
	Gates         GateTaxonomy      `json:"gates"`
	Mirrors       map[string]Mirror `json:"mirrors,omitempty"`
	Surfaces      []DomainSpec      `json:"surfaces"`
	Admission     Admission         `json:"admission,omitempty"`
	Backstop      Backstop          `json:"backstop"`
	Findings      Findings          `json:"findings,omitempty"`
	Tier          *Tier             `json:"tier,omitempty"`
	Usage         *UsageSource      `json:"usage,omitempty"`
}

// Load reads and validates the harness policy at path.
//
// The file is checked against the owned harness-policy schema first, then
// against the cross-field rules a schema cannot express: routing exhaustive
// over every deliverable kind, every referenced role declared, the fix
// verdict inside the declared verdict vocabulary, and every mirror template
// parseable. A policy that fails any of these is refused — the engine never
// runs on a partially understood harness.
func Load(path string) (*Harness, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("policy: read %s: %w", path, err)
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("policy: parse %s: %w", path, err)
	}
	diags, err := schemas.HarnessPolicy.Validate(doc)
	if err != nil {
		return nil, fmt.Errorf("policy: validate %s: %w", path, err)
	}
	if len(diags) > 0 {
		return nil, &ContractError{Path: path, Diagnostics: diags}
	}
	var h Harness
	if err := json.Unmarshal(raw, &h); err != nil {
		return nil, fmt.Errorf("policy: decode %s: %w", path, err)
	}
	if err := h.Validate(); err != nil {
		return nil, fmt.Errorf("policy: %s: %w", path, err)
	}
	return &h, nil
}

// ContractError reports a policy file that violates the harness-policy
// schema, carrying every violation so one load names every problem.
type ContractError struct {
	Path        string
	Diagnostics []clikit.Diagnostic
}

func (e *ContractError) Error() string {
	first := "unspecified violation"
	if len(e.Diagnostics) > 0 {
		first = e.Diagnostics[0].Message
	}
	return fmt.Sprintf("policy: %s violates the harness-policy contract (%d violations; first: %s)", e.Path, len(e.Diagnostics), first)
}

// Validate checks the cross-field rules the schema cannot express.
func (h *Harness) Validate() error {
	if h.SchemaVersion != SchemaVersion {
		return fmt.Errorf("declares schema_version %q, this engine reads %q", h.SchemaVersion, SchemaVersion)
	}
	if _, err := ids.Lookup(h.IDScheme); err != nil {
		return err
	}
	for _, kind := range dag.AllKinds {
		route, ok := h.Routes[string(kind)]
		if !ok {
			return fmt.Errorf("routes are not exhaustive: no route for deliverable kind %q", kind)
		}
		if err := h.checkStages(fmt.Sprintf("routes.%s", kind), route.Stages); err != nil {
			return err
		}
		if route.ReviewRole != "" {
			if err := h.checkReviewRole(fmt.Sprintf("routes.%s.review_role", kind), route.ReviewRole); err != nil {
				return err
			}
		}
	}
	if err := h.checkStages("workflow", h.Workflow.Stages); err != nil {
		return err
	}
	if h.Gates.ReviewRole != "" {
		if err := h.checkReviewRole("gates.review_role", h.Gates.ReviewRole); err != nil {
			return err
		}
	}
	if h.Gates.FixVerdict != "" && !slices.Contains(h.Gates.Verdicts, h.Gates.FixVerdict) {
		return fmt.Errorf("gates.fix_verdict %q is not one of the declared verdicts %s", h.Gates.FixVerdict, strings.Join(h.Gates.Verdicts, ", "))
	}
	if len(h.Backstop.Command) == 0 {
		return fmt.Errorf("backstop.command is required: the post-merge compile check is always on")
	}
	for kind := range h.Mirrors {
		if _, err := h.MirrorTemplate(kind); err != nil {
			return err
		}
	}
	return nil
}

// checkStages verifies every stage names a declared role.
func (h *Harness) checkStages(where string, stages []Stage) error {
	for _, s := range stages {
		if _, ok := h.Roles[s.Role]; !ok {
			return fmt.Errorf("%s stage %q names undeclared role %q", where, s.Stage, s.Role)
		}
	}
	return nil
}

// checkReviewRole verifies a review role is declared and is not a builder: a
// role that authors artifacts would produce one instead of returning a
// verdict.
func (h *Harness) checkReviewRole(where, role string) error {
	r, ok := h.Roles[role]
	if !ok {
		return fmt.Errorf("%s names undeclared role %q", where, role)
	}
	if r.Builder {
		return fmt.Errorf("%s names builder role %q; a review role must not author artifacts", where, role)
	}
	return nil
}

// StagesFor returns the stages a node of the given deliverable kind runs.
// Routing is exhaustive by construction — Validate refuses a policy missing
// any kind — so an unrouted kind is a caller error, never a fallback to a
// builder.
func (h *Harness) StagesFor(kind dag.DeliverableKind) ([]Stage, error) {
	route, ok := h.Routes[string(kind)]
	if !ok {
		return nil, fmt.Errorf("policy: no route for deliverable kind %q", kind)
	}
	if len(route.Stages) > 0 {
		return route.Stages, nil
	}
	return h.Workflow.Stages, nil
}

// ReviewRoleFor returns the role that reviews a node of the given kind,
// falling back to the gate-wide review role.
func (h *Harness) ReviewRoleFor(kind dag.DeliverableKind) string {
	if route, ok := h.Routes[string(kind)]; ok && route.ReviewRole != "" {
		return route.ReviewRole
	}
	return h.Gates.ReviewRole
}

// KnownVerdict reports whether v is in the declared verdict vocabulary.
func (h *Harness) KnownVerdict(v string) bool { return slices.Contains(h.Gates.Verdicts, v) }

// MaxGroupSize is the batch bound, defaulted when the policy declares none.
func (h *Harness) MaxGroupSize() int {
	if h.Admission.MaxGroupSize > 0 {
		return h.Admission.MaxGroupSize
	}
	return DefaultMaxGroupSize
}

// BlockingThreshold is the finding criticality that blocks the build.
func (h *Harness) BlockingThreshold() int {
	if h.Findings.BlockingThreshold > 0 {
		return h.Findings.BlockingThreshold
	}
	return DefaultBlockingThreshold
}

// TargetsMain reports whether a merge target is the one branch that receives
// a reviewed, fully signed merge and nothing else.
func (h *Harness) TargetsMain(target string) bool { return target == h.Gates.MainBranch }

// MirrorTemplate parses the Markdown mirror template declared for an
// artifact kind. ok is false when the policy declares no mirror for it, which
// simply means that artifact has no generated view.
func (h *Harness) MirrorTemplate(kind string) (*template.Template, error) {
	m, ok := h.Mirrors[kind]
	if !ok {
		return nil, nil
	}
	tmpl, err := docmirror.Parse(kind, m.Template)
	if err != nil {
		return nil, fmt.Errorf("mirrors.%s: %w", kind, err)
	}
	return tmpl, nil
}

// Scheme resolves the id scheme this harness names.
func (h *Harness) Scheme() (ids.Scheme, error) { return ids.Lookup(h.IDScheme) }
