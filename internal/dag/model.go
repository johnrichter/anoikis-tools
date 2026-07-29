// Package dag holds the Anoikis work-graph domain: the artifact types the
// engine reads and writes, the closed vocabularies they range over, and the
// pure derivations over them (building the dependency graph, deciding which
// nodes are ready, rolling up counts).
//
// Everything here is a value. Nothing in this package touches the
// filesystem, git, a clock or a process — those belong to the packages that
// surround it — so every decision the engine makes is reproducible from the
// state it was handed.
package dag

import (
	"fmt"
	"slices"

	"github.com/johnrichter/claude-shared-tooling/go/graph"
)

// SchemaVersion is the semantic version stamped on every artifact this
// package writes. A file declaring a different MAJOR is refused rather than
// read with guessed semantics.
const SchemaVersion = "1.0.0"

// Status is where a node stands. done means merged onto the build branch,
// not merely agent-complete, so a later node branching from that base
// contains its dependencies' work.
type Status string

// The closed node-status set.
const (
	StatusBlocked  Status = "blocked"
	StatusReady    Status = "ready"
	StatusRunning  Status = "running"
	StatusDone     Status = "done"
	StatusFailed   Status = "failed"
	StatusArchived Status = "archived"
)

var knownStatuses = map[Status]bool{
	StatusBlocked: true, StatusReady: true, StatusRunning: true,
	StatusDone: true, StatusFailed: true, StatusArchived: true,
}

// Known reports whether s is one of the six canonical statuses.
func (s Status) Known() bool { return knownStatuses[s] }

// Settled reports whether s means the node's work is finished and merged.
// An archived node is settled: its tombstone stands in for it.
func (s Status) Settled() bool { return s == StatusDone || s == StatusArchived }

// Event is one transition recorded in the run log.
type Event string

// The closed run-log event set. grafted records the one build-time graph
// mutation the engine performs: inserting a fix node in response to a review
// verdict.
const (
	EventDispatched Event = "dispatched"
	EventComplete   Event = "complete"
	EventFailed     Event = "failed"
	EventMerged     Event = "merged"
	EventGrafted    Event = "grafted"
)

// AllEvents is the closed run-log event enumeration. FoldLog's fold is
// checked exhaustive against it, so a member added here without a folding
// decision fails that check rather than folding silently as a no-op.
var AllEvents = []Event{EventDispatched, EventComplete, EventFailed, EventMerged, EventGrafted}

// Known reports whether e is one of the canonical events.
func (e Event) Known() bool { return slices.Contains(AllEvents, e) }

// VerifyTier selects how a node is reviewed. It is the sole review selector —
// review is never also a stage.
type VerifyTier string

// The closed verification-tier set.
const (
	VerifyCheap         VerifyTier = "cheap"
	VerifyGate          VerifyTier = "gate"
	VerifyImmediateDeep VerifyTier = "immediate_deep"
)

var knownTiers = map[VerifyTier]bool{
	VerifyCheap: true, VerifyGate: true, VerifyImmediateDeep: true,
}

// Known reports whether t is one of the three canonical tiers.
func (t VerifyTier) Known() bool { return knownTiers[t] }

// DeliverableKind is what a node delivers, and therefore how it is verified.
// Routing is exhaustive over this set, so adding a member forces every route to
// account for it.
type DeliverableKind string

// The closed deliverable-kind set. code and docs name an artifact an agent
// authors. gate names none: it is an operator-precondition boundary, verified
// by a recorded confirmation of the signal it declares.
const (
	KindCode DeliverableKind = "code"
	KindDocs DeliverableKind = "docs"
	KindGate DeliverableKind = "gate"
)

// AllKinds is the enumeration routing completeness is checked against.
var AllKinds = []DeliverableKind{KindCode, KindDocs, KindGate}

// AuthoredKinds are the kinds an agent delivers by authoring an artifact, and
// so the kinds a harness policy declares a dispatch route for. It is derived
// from the one fact that separates them, so the two can never disagree.
var AuthoredKinds = slices.DeleteFunc(slices.Clone(AllKinds), DeliverableKind.OperatorConfirmed)

// Known reports whether k is one of the canonical kinds.
func (k DeliverableKind) Known() bool { return slices.Contains(AllKinds, k) }

// OperatorConfirmed reports whether k is verified by a recorded operator
// confirmation rather than by an artifact an agent authors. Such a node is
// never dispatched: there is nothing for a role to write.
func (k DeliverableKind) OperatorConfirmed() bool { return k == KindGate }

// GateStatus is where a gate stands.
type GateStatus string

// The closed gate-status set.
const (
	GatePending   GateStatus = "pending"
	GateReviewing GateStatus = "reviewing"
	GatePassed    GateStatus = "passed"
	GateMerged    GateStatus = "merged"
)

// ProjectStatus is where the effort as a whole stands.
type ProjectStatus string

// The closed project-status set.
const (
	ProjectDraft    ProjectStatus = "draft"
	ProjectReady    ProjectStatus = "ready"
	ProjectBuilding ProjectStatus = "building"
	ProjectBlocked  ProjectStatus = "blocked"
	ProjectComplete ProjectStatus = "complete"
)

// MergeTargetNone is the merge target of a gate that marks a boundary without
// moving the build branch anywhere.
const MergeTargetNone = "none"

// DeepReviewNone is the review mode of a gate that closes without a verdict.
const DeepReviewNone = "none"

// Usage is what a run cost. known is the load-bearing field: false means the
// spend provider could not price the run, and the figures beside it are
// meaningless. Nothing sums an unknown usage into a total.
type Usage struct {
	Known               bool    `json:"known"`
	Reason              string  `json:"reason,omitempty"`
	CostUSD             float64 `json:"cost_usd,omitempty"`
	InputTokens         int64   `json:"input_tokens,omitempty"`
	CacheCreationTokens int64   `json:"cache_creation_tokens,omitempty"`
	CacheReadTokens     int64   `json:"cache_read_tokens,omitempty"`
	OutputTokens        int64   `json:"output_tokens,omitempty"`
}

// Fold adds next to a running total, letting one unpriced run make the whole
// total unknown rather than quietly understating it. The first unknown
// reason is kept, since it is the one that explains the gap.
func (u Usage) Fold(next Usage) Usage {
	if !next.Known {
		u.Known = false
		if u.Reason == "" {
			u.Reason = next.Reason
		}
		return u
	}
	u.CostUSD += next.CostUSD
	u.InputTokens += next.InputTokens
	u.CacheCreationTokens += next.CacheCreationTokens
	u.CacheReadTokens += next.CacheReadTokens
	u.OutputTokens += next.OutputTokens
	return u
}

// Claim is one resource a node declares it will touch, in one domain the
// harness policy registered. Generalizing beyond file paths is deliberate:
// packages, locks, queues and topics all collide in ways a path claim cannot
// express.
type Claim struct {
	Domain   string `json:"domain"`
	Kind     string `json:"kind,omitempty"`
	Value    string `json:"value"`
	Required bool   `json:"required,omitempty"`
}

// Tombstone is what an archived node leaves in its shard so a live node's
// archived dependency still resolves to done instead of stalling.
type Tombstone struct {
	Summary   string  `json:"summary"`
	CostUSD   float64 `json:"cost_usd,omitempty"`
	CostKnown bool    `json:"cost_known,omitempty"`
}

// Node is the scheduling-hot record: what admission and the state machine
// need, and nothing else. Everything heavier lives behind DetailRef.
type Node struct {
	ID            string     `json:"id"`
	Title         string     `json:"title"`
	Status        Status     `json:"status"`
	Deps          []string   `json:"deps,omitempty"`
	NeverDispatch bool       `json:"never_dispatch,omitempty"`
	Surface       []Claim    `json:"surface"`
	VerifyTier    VerifyTier `json:"verify_tier"`
	DetailRef     string     `json:"detail_ref"`
	Attempts      int        `json:"attempts,omitempty"`
	MaxAttempts   int        `json:"max_attempts,omitempty"`
	GraftedFrom   []string   `json:"grafted_from,omitempty"`
	Tombstone     *Tombstone `json:"tombstone,omitempty"`
}

// ResourceSurface groups the node's claims by domain, the shape a
// disjointness proof consumes. A node with no claims yields an empty surface,
// which is unprovable and therefore unsafe to co-batch — the intended answer.
func (n Node) ResourceSurface() graph.Surface {
	if len(n.Surface) == 0 {
		return nil
	}
	s := graph.Surface{}
	for _, c := range n.Surface {
		s[c.Domain] = append(s[c.Domain], graph.Claim{Kind: c.Kind, Value: c.Value})
	}
	return s
}

// RetriesLeft reports whether the node may be dispatched again after a
// failure. MaxAttempts zero means one attempt.
func (n Node) RetriesLeft() bool {
	limit := n.MaxAttempts
	if limit < 1 {
		limit = 1
	}
	return n.Attempts < limit
}

// Shard is one gate's slice of the graph. A node's gate is the shard it lives
// in; that is the only membership record.
type Shard struct {
	SchemaVersion string `json:"schema_version"`
	GateID        string `json:"gate_id"`
	Nodes         []Node `json:"nodes"`
}

// Counts tallies a shard's node statuses for the graph index.
type Counts struct {
	Blocked  int `json:"blocked"`
	Ready    int `json:"ready"`
	Running  int `json:"running"`
	Done     int `json:"done"`
	Failed   int `json:"failed"`
	Archived int `json:"archived"`
}

// Tally recomputes s's status counts from its nodes.
func (s Shard) Tally() Counts {
	var c Counts
	for _, n := range s.Nodes {
		switch n.Status {
		case StatusBlocked:
			c.Blocked++
		case StatusReady:
			c.Ready++
		case StatusRunning:
			c.Running++
		case StatusDone:
			c.Done++
		case StatusFailed:
			c.Failed++
		case StatusArchived:
			c.Archived++
		}
	}
	return c
}

// ShardRef is the graph index's row for one shard.
type ShardRef struct {
	GateID string `json:"gate_id"`
	Ref    string `json:"ref"`
	Counts Counts `json:"counts"`
}

// Index is the tiny top of the sharded graph.
type Index struct {
	SchemaVersion string     `json:"schema_version"`
	Updated       string     `json:"updated,omitempty"`
	Shards        []ShardRef `json:"shards"`
}

// Stage is one step of a node's run and the tier it runs at.
type Stage struct {
	Stage              string `json:"stage"`
	Role               string `json:"role"`
	Model              string `json:"model"`
	ContextWindow      string `json:"context_window,omitempty"`
	Effort             string `json:"effort,omitempty"`
	ModelJustification string `json:"model_justification,omitempty"`
}

// Inputs names the specs a node's dispatch must carry.
type Inputs struct {
	SpecRefs []string `json:"spec_refs,omitempty"`
}

// NodeResult is what a node produced, as recorded on its detail record.
type NodeResult struct {
	ArtifactRefs  []string `json:"artifact_refs,omitempty"`
	RunResultRefs []string `json:"run_result_refs,omitempty"`
	FindingRefs   []string `json:"finding_refs,omitempty"`
	Usage         *Usage   `json:"usage,omitempty"`
}

// Precondition is a gate's contract: the operator signal it requires, and the
// confirmation that satisfies it. Only an operator-confirmed kind carries one.
type Precondition struct {
	// Signal is what an operator must confirm is true, stated so that
	// confirming it is one yes-or-no act.
	Signal string `json:"signal"`
	// Confirmation is the record that satisfies the signal. Absent until an
	// operator has confirmed it, which is the state that blocks dependents.
	Confirmation *Confirmation `json:"confirmation,omitempty"`
}

// Confirmation is one recorded operator confirmation. All three fields are
// load-bearing: a record missing any of them attests to nothing.
type Confirmation struct {
	// By is the operator who confirmed.
	By string `json:"by"`
	// At is when they confirmed, as an RFC 3339 instant.
	At string `json:"at"`
	// Against restates the signal confirmed, so the record says what was
	// confirmed without the node beside it.
	Against string `json:"against"`
}

// Detail is everything about a node that is read only on dispatch or
// inspection. Stages and Precondition are exclusive: one names the run that
// authors an artifact, the other the signal an operator confirms instead.
type Detail struct {
	SchemaVersion   string          `json:"schema_version"`
	ID              string          `json:"id"`
	Intent          string          `json:"intent"`
	DeliverableKind DeliverableKind `json:"deliverable_kind"`
	Acceptance      []string        `json:"acceptance"`
	Precondition    *Precondition   `json:"precondition,omitempty"`
	Stages          []Stage         `json:"stages,omitempty"`
	WorktreeRef     string          `json:"worktree_ref,omitempty"`
	Inputs          *Inputs         `json:"inputs,omitempty"`
	Result          *NodeResult     `json:"result,omitempty"`
}

// GatePolicy is one gate's behaviour at the boundary it marks.
type GatePolicy struct {
	Pause       bool   `json:"pause"`
	DeepReview  string `json:"deep_review"`
	MergeTarget string `json:"merge_target"`
	Sign        string `json:"sign"`
}

// Gate is a marked boundary where the build pauses to review, merge, or both.
type Gate struct {
	ID     string     `json:"id"`
	Name   string     `json:"name"`
	Policy GatePolicy `json:"policy"`
	Status GateStatus `json:"status"`
}

// NeedsReview reports whether this gate is closed by a review verdict rather
// than by reaching it.
func (g Gate) NeedsReview() bool { return g.Policy.DeepReview != DeepReviewNone }

// NeedsMerge reports whether this gate still has to move the build branch
// onto a target.
func (g Gate) NeedsMerge() bool {
	return g.Policy.MergeTarget != MergeTargetNone && g.Status != GateMerged
}

// Closed reports whether the build may proceed past this gate. A gate with a
// merge target is closed only once that merge has landed; one without is
// closed as soon as its review passes it.
func (g Gate) Closed() bool {
	if g.Policy.MergeTarget != MergeTargetNone {
		return g.Status == GateMerged
	}
	return g.Status == GatePassed || g.Status == GateMerged
}

// Gates is the gate-policy artifact.
type Gates struct {
	SchemaVersion string `json:"schema_version"`
	Gates         []Gate `json:"gates"`
}

// Find returns the gate with the given id.
func (g Gates) Find(id string) (Gate, bool) {
	for _, gt := range g.Gates {
		if gt.ID == id {
			return gt, true
		}
	}
	return Gate{}, false
}

// Budget is the effort's spend ceiling, what has been spent against it, and
// where it is enforced.
type Budget struct {
	CeilingUSD float64 `json:"ceiling_usd"`
	SpentUSD   float64 `json:"spent_usd,omitempty"`
	// UnpricedRuns counts the runs the spend provider could not price. One is
	// enough to make the recorded total a floor rather than a figure, which is
	// why it is stored rather than folded into the sum.
	UnpricedRuns int    `json:"unpriced_runs,omitempty"`
	EnforcedAt   string `json:"enforced_at"`
}

// Spend renders what the effort has cost so far, unknown as a whole while any
// run in it went unpriced.
func (b Budget) Spend() Usage {
	if b.UnpricedRuns > 0 {
		return Usage{Reason: fmt.Sprintf("%d run(s) could not be priced, so %.2f USD is a floor and not a total", b.UnpricedRuns, b.SpentUSD)}
	}
	return Usage{Known: true, CostUSD: b.SpentUSD}
}

// Fold adds a batch's spend to the effort's running total. unpriced counts the
// runs in that batch the provider could not price.
func (b Budget) Fold(spend Usage, unpriced int) Budget {
	b.SpentUSD += spend.CostUSD
	b.UnpricedRuns += unpriced
	return b
}

// Signing states the one signing rule the engine enforces in code: nothing
// below the main branch is signed, and the main merge re-signs every commit
// and signs the merge commit.
type Signing struct {
	BelowMain string `json:"below_main"`
	MainMerge string `json:"main_merge"`
}

// Refs names where each artifact lives, relative to the effort directory.
type Refs struct {
	Graph    string `json:"graph"`
	RunLog   string `json:"run_log"`
	Gates    string `json:"gates"`
	Findings string `json:"findings"`
	Nodes    string `json:"nodes"`
	Results  string `json:"results"`
	Prompts  string `json:"prompts"`
	Archive  string `json:"archive"`
	Cursor   string `json:"cursor"`
}

// Provenance records what this version of the effort was derived from.
type Provenance struct {
	FromDesign     string   `json:"from_design,omitempty"`
	FoldedFindings []string `json:"folded_findings,omitempty"`
}

// Project is the effort manifest: small, always loaded, and the only place
// carryover lives.
type Project struct {
	SchemaVersion string        `json:"schema_version"`
	ID            string        `json:"id"`
	Name          string        `json:"name"`
	Version       string        `json:"version"`
	Status        ProjectStatus `json:"status"`
	Provenance    *Provenance   `json:"provenance,omitempty"`
	Budget        Budget        `json:"budget"`
	Signing       Signing       `json:"signing"`
	Carryover     []string      `json:"carryover,omitempty"`
	BuildBranch   string        `json:"build_branch,omitempty"`
	BaseRef       string        `json:"base_ref,omitempty"`
	Refs          Refs          `json:"refs"`
}

// LogEvent is one line of the append-only run log.
type LogEvent struct {
	SchemaVersion string `json:"schema_version"`
	TS            string `json:"ts"`
	RunID         string `json:"run_id"`
	NodeID        string `json:"node_id"`
	Event         Event  `json:"event"`
	Role          string `json:"role,omitempty"`
	LayerSeq      int    `json:"layer_seq,omitempty"`
	Model         string `json:"model,omitempty"`
	ContextWindow string `json:"context_window,omitempty"`
	Effort        string `json:"effort,omitempty"`
	WorktreeRef   string `json:"worktree_ref,omitempty"`
	BaseRef       string `json:"base_ref,omitempty"`
	PromptRef     string `json:"prompt_ref,omitempty"`
	PromptDigest  string `json:"prompt_digest,omitempty"`
	RunResultRef  string `json:"run_result_ref,omitempty"`
	Detail        string `json:"detail,omitempty"`
	Usage         *Usage `json:"usage,omitempty"`
}

// Diagnostic is one structured problem a run reported.
type Diagnostic struct {
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
	Severity string `json:"severity"`
	Code     string `json:"code,omitempty"`
	Message  string `json:"message"`
}

// FindingSeed is a finding a run raised, in the form the ranked register
// accepts. Criticality is never carried here: the register derives it.
type FindingSeed struct {
	Statement string `json:"statement"`
	Impact    int    `json:"impact"`
	Urgency   int    `json:"urgency"`
}

// RunCounts tallies a run's own pass/fail/warning accounting.
type RunCounts struct {
	Passed   int `json:"passed,omitempty"`
	Failed   int `json:"failed,omitempty"`
	Warnings int `json:"warnings,omitempty"`
	Errors   int `json:"errors,omitempty"`
}

// Attribution is how the spend provider finds a run's turns. It is identity,
// never figures: an agent cannot measure its own billed tokens, so a run
// reports where it ran and the provider prices it.
type Attribution struct {
	// SessionID is the session the run executed in.
	SessionID string `json:"session_id,omitempty"`
	// Agent names the agent whose turns to attribute, empty for every turn in
	// the session.
	Agent string `json:"agent,omitempty"`
	// TranscriptRef, when set, is the transcript to read instead of resolving
	// one from SessionID.
	TranscriptRef string `json:"transcript_ref,omitempty"`
}

// RunResult is what one node's run produced, durably recorded. The failing
// excerpt is copied in because the raw log dies with the node's worktree.
type RunResult struct {
	SchemaVersion string        `json:"schema_version"`
	NodeID        string        `json:"node_id"`
	RunID         string        `json:"run_id"`
	Status        string        `json:"status"`
	Verdict       string        `json:"verdict,omitempty"`
	Attribution   *Attribution  `json:"attribution,omitempty"`
	ArtifactRefs  []string      `json:"artifact_refs,omitempty"`
	Counts        *RunCounts    `json:"counts,omitempty"`
	Diagnostics   []Diagnostic  `json:"diagnostics,omitempty"`
	OverflowCount int           `json:"overflow_count,omitempty"`
	Excerpt       string        `json:"excerpt,omitempty"`
	Findings      []FindingSeed `json:"findings,omitempty"`
	DurationMS    int64         `json:"duration_ms,omitempty"`
}

// Run outcome vocabulary for RunResult.Status.
const (
	RunPass = "pass"
	RunFail = "fail"
)

// ErrUnknownGate reports a shard naming a gate the gate policy does not
// declare.
var ErrUnknownGate = fmt.Errorf("dag: shard names an undeclared gate")
