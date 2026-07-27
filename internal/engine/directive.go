// Package engine is the deterministic brain: given an effort's state and its
// harness policy, it returns exactly one next action.
//
// The driver that runs a build is deliberately unintelligent. It calls Step,
// executes the one named action verbatim, and calls Step again. It never
// computes readiness, never detects a gate, never chooses a merge, and never
// composes a git command — every one of those is decided here, in code, from
// state that is entirely on disk. That is what makes a build repeatable:
// identical state yields an identical directive, so identical inputs produce
// identical dispatches.
//
// Everything in this package is a pure function of the values passed to it.
// Reading state, running commands and touching git all live outside it.
package engine

import (
	"github.com/johnrichter/anoikis-tools/internal/admission"
	"github.com/johnrichter/anoikis-tools/internal/dag"
)

// Action is the one thing a driver does next.
type Action string

// The closed directive set. A driver that handles these five handles every
// state the build can be in.
const (
	// ActionLaunch dispatches a batch of nodes that may safely run together.
	ActionLaunch Action = "launch"
	// ActionGate has reached a boundary: review, merge, or both.
	ActionGate Action = "gate"
	// ActionPause is a safe stopping point with work still outstanding.
	ActionPause Action = "pause"
	// ActionHalt is a condition no mechanical rule can resolve; it needs an
	// operator.
	ActionHalt Action = "halt"
	// ActionStop is completion.
	ActionStop Action = "stop"
)

// Cause names why a build paused or halted. It is a closed vocabulary so a
// caller can branch on the cause without parsing the human-readable reason.
type Cause string

// The closed pause/halt cause set.
const (
	// CauseGraphCycle is a dependency cycle: the plan cannot be ordered.
	CauseGraphCycle Cause = "graph-cycle"
	// CauseFailedNode is a node that failed with no attempt left.
	CauseFailedNode Cause = "failed-node"
	// CauseBlockingFinding is an open finding severe enough that further
	// work would build on top of it.
	CauseBlockingFinding Cause = "blocking-finding"
	// CauseBudget is the spend ceiling.
	CauseBudget Cause = "budget"
	// CauseBlocked is outstanding work with nothing dispatchable: every
	// remaining node waits on something that will never complete.
	CauseBlocked Cause = "blocked"
	// CauseRunsInFlight is a safe pause while dispatched runs finish.
	CauseRunsInFlight Cause = "runs-in-flight"
	// CauseNotReady is an effort whose plan has not been marked ready.
	CauseNotReady Cause = "not-ready"
	// CauseSurfaceOverlap is a merge refused because two nodes touched the
	// same resource after all.
	CauseSurfaceOverlap Cause = "surface-overlap"
	// CauseBackstopFailed is a merged layer that does not build, or a node
	// whose actual changes exceeded the surface it declared.
	CauseBackstopFailed Cause = "backstop-failed"
)

// Command is an invocation the driver runs verbatim. Every command names this
// CLI, never git or a shell: the driver executes it, the CLI performs the real
// operation, and no model ever composes a git argument.
type Command struct {
	// Purpose says what the command accomplishes, for the driver's log.
	Purpose string `json:"purpose"`
	// Argv is the exact argument vector to run.
	Argv []string `json:"argv"`
}

// Launch is the payload of a launch directive: which nodes run together now,
// which ready nodes were held back and why, and the commit their worktrees
// branch from.
type Launch struct {
	LayerSeq     int                  `json:"layer_seq"`
	BaseRef      string               `json:"base_ref"`
	Members      []string             `json:"members"`
	Deferred     []admission.Deferral `json:"deferred,omitempty"`
	SoloFallback bool                 `json:"solo_fallback,omitempty"`
}

// GateStep is the payload of a gate directive.
type GateStep struct {
	GateID string `json:"gate_id"`
	Name   string `json:"name"`
	// Status is how far through the boundary the build already is, so a
	// driver resuming mid-gate can tell a review it still owes from a merge.
	Status dag.GateStatus `json:"status"`
	// Pause is whether the gate stops for an operator before merging.
	Pause bool `json:"pause"`
	// DeepReview is the review mode the gate's accumulated diff gets.
	DeepReview string `json:"deep_review"`
	// ReviewRole is the role that review dispatches to. It is never a
	// builder: a role that authors artifacts would produce one instead of
	// returning a verdict.
	ReviewRole string `json:"review_role,omitempty"`
	// MergeTarget is the branch the build branch merges onto, or "none".
	MergeTarget string `json:"merge_target"`
	// TargetsMain marks the one merge that re-signs every commit, signs the
	// merge commit, and requires an operator-approved message. Every other
	// merge is autonomous and unsigned.
	TargetsMain bool `json:"targets_main"`
}

// Summary is the payload of a stop directive.
type Summary struct {
	Nodes int       `json:"nodes"`
	Gates int       `json:"gates"`
	Spend dag.Usage `json:"spend"`
}

// Directive is exactly one next action.
type Directive struct {
	Action Action `json:"action"`
	// Cause is set on pause and halt.
	Cause Cause `json:"cause,omitempty"`
	// Reason is the operator-facing sentence behind Cause.
	Reason string `json:"reason,omitempty"`
	// Subjects names the nodes, gates or findings the cause points at, so an
	// operator does not have to search for them.
	Subjects []string `json:"subjects,omitempty"`
	// Commands are run verbatim, in order.
	Commands []Command `json:"commands,omitempty"`
	Launch   *Launch   `json:"launch,omitempty"`
	Gate     *GateStep `json:"gate,omitempty"`
	Summary  *Summary  `json:"summary,omitempty"`
}

// halt builds a halt directive.
func halt(cause Cause, reason string, subjects ...string) Directive {
	return Directive{Action: ActionHalt, Cause: cause, Reason: reason, Subjects: subjects}
}

// pause builds a pause directive.
func pause(cause Cause, reason string, subjects ...string) Directive {
	return Directive{Action: ActionPause, Cause: cause, Reason: reason, Subjects: subjects}
}
