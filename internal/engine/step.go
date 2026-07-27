package engine

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/johnrichter/anoikis-tools/internal/admission"
	"github.com/johnrichter/anoikis-tools/internal/dag"
	"github.com/johnrichter/anoikis-tools/internal/ids"
	"github.com/johnrichter/anoikis-tools/internal/policy"
	"github.com/johnrichter/claude-shared-tooling/go/gate"
	"github.com/johnrichter/claude-shared-tooling/go/graph"
)

// Finding is the engine's view of an open finding: enough to decide whether
// it blocks and to name it if it does, and nothing about where the register
// keeps it.
type Finding struct {
	ID          string
	Statement   string
	Criticality int
}

// Env is the world outside the effort's own artifacts that a directive needs:
// what this CLI is called, which effort is being driven, and where the build
// branch currently points.
type Env struct {
	// Tool is this CLI's own name, so every emitted command is a
	// self-invocation the driver can run without composing anything.
	Tool string
	// Effort is the effort slug every emitted command carries.
	Effort string
	// BaseRef is the build branch's current head — the commit a newly
	// launched layer's worktrees branch from.
	BaseRef string
}

// Step returns the one next action for an effort.
//
// The order of the checks is the safety policy, not an implementation
// detail: every condition that means "an operator must look at this" is
// settled before any condition that would dispatch more work, so a build
// never widens a problem it has already detected.
//
//  1. A cyclic graph cannot be ordered at all.
//  2. A node that failed with no attempt left needs a replan.
//  3. An open blocking finding means further work builds on a known defect.
//  4. The spend ceiling is a hard stop.
//  5. Dispatched runs still in flight: pause rather than launch alongside.
//  6. A gate whose nodes are all merged is a boundary, and boundaries come
//     before more work.
//  7. Everything settled and every gate passed is completion.
//  8. Outstanding work with nothing dispatchable is a blockage.
//  9. Otherwise, launch the widest provably safe batch.
func Step(st dag.State, h *policy.Harness, scheme ids.Scheme, prover *graph.Prover, open []Finding, env Env) (Directive, error) {
	g, err := st.Graph(scheme)
	if err != nil {
		return Directive{}, err
	}
	if err := g.DetectCycles(); err != nil {
		return halt(CauseGraphCycle, err.Error()), nil
	}
	if exhausted := st.Exhausted(); len(exhausted) > 0 {
		return halt(CauseFailedNode,
			fmt.Sprintf("%d node(s) failed with no attempt left; the graph needs an operator replan", len(exhausted)),
			exhausted...), nil
	}
	if blocking := blockingFindings(open, h.BlockingThreshold()); len(blocking) > 0 {
		return halt(CauseBlockingFinding,
			fmt.Sprintf("%d open finding(s) at or above criticality %d; further work would build on top of them", len(blocking), h.BlockingThreshold()),
			blocking...), nil
	}
	if over, reason := budgetExceeded(st.Project.Budget); over {
		return halt(CauseBudget, reason), nil
	}
	if running := st.Running(); len(running) > 0 {
		return pause(CauseRunsInFlight,
			fmt.Sprintf("%d run(s) are still in flight; let them finish, then record their results", len(running)),
			running...), nil
	}
	if gate, ok := st.ReachedGate(); ok {
		return gateDirective(gate, h, env), nil
	}
	if st.Complete() {
		return Directive{
			Action:  ActionStop,
			Summary: &Summary{Nodes: len(st.Nodes()), Gates: len(st.Gates.Gates), Spend: st.Project.Budget.Spend()},
		}, nil
	}
	if st.Project.Status == dag.ProjectDraft {
		return pause(CauseNotReady, "the effort is still a draft; validate it and mark it ready before building"), nil
	}

	ready := st.Ready(g)
	if len(ready) == 0 {
		return halt(CauseBlocked,
			"work remains but nothing is dispatchable: every outstanding node waits on a dependency that will not complete"), nil
	}
	batch, err := admission.Admit(g, ready, prover, h.MaxGroupSize())
	if err != nil {
		return Directive{}, err
	}
	return launchDirective(st, batch, env), nil
}

// launchDirective renders an admitted batch as the directive that dispatches
// it. The follow-up command is emitted with it so the driver's whole
// obligation for this layer is on screen at once.
func launchDirective(st dag.State, batch admission.Batch, env Env) Directive {
	layer := st.NextLayerSeq()
	return Directive{
		Action: ActionLaunch,
		Launch: &Launch{
			LayerSeq:     layer,
			BaseRef:      env.BaseRef,
			Members:      batch.Members,
			Deferred:     batch.Deferred,
			SoloFallback: batch.SoloFallback,
		},
		Commands: []Command{
			{
				Purpose: "create each node's worktree, render and journal its prompt, then return the dispatches to launch",
				Argv:    []string{env.Tool, "dispatch", "--effort", env.Effort, "--layer", strconv.Itoa(layer)},
			},
			{
				Purpose: "after every launched run returns, record the results and merge the layer",
				Argv:    []string{env.Tool, "record", "--effort", env.Effort, "--results", "<results-file>"},
			},
		},
	}
}

// gateDirective renders a reached gate as everything still outstanding at it,
// in the order those steps must happen: the review that closes the gate, then
// the merge that moves the build branch on.
//
// A merge onto the harness's declared main branch is the only one that
// re-signs history and signs the merge commit, and the only one that asks an
// operator to approve its message. That distinction is enforced here and in
// the merge itself, so it holds regardless of what any external guidance says.
func gateDirective(gate dag.Gate, h *policy.Harness, env Env) Directive {
	targetsMain := gate.Policy.MergeTarget != dag.MergeTargetNone && h.TargetsMain(gate.Policy.MergeTarget)
	d := Directive{Action: ActionGate, Gate: &GateStep{
		GateID:      gate.ID,
		Name:        gate.Name,
		Status:      gate.Status,
		Pause:       gate.Policy.Pause,
		DeepReview:  gate.Policy.DeepReview,
		ReviewRole:  h.Gates.ReviewRole,
		MergeTarget: gate.Policy.MergeTarget,
		TargetsMain: targetsMain,
	}}

	if gate.Status == dag.GatePending || gate.Status == dag.GateReviewing {
		argv := []string{env.Tool, "close-gate", "--effort", env.Effort, "--gate", gate.ID}
		purpose := "close this gate; it declares no review"
		if gate.NeedsReview() {
			argv = append(argv, "--verdict", "<"+strings.Join(h.Gates.Verdicts, "|")+">")
			purpose = fmt.Sprintf("dispatch the %s review over this gate's accumulated diff, then feed its verdict back", gate.Policy.DeepReview)
		}
		d.Commands = append(d.Commands, Command{Purpose: purpose, Argv: argv})
	}
	if gate.NeedsMerge() {
		argv := []string{env.Tool, "merge-gate", "--effort", env.Effort, "--gate", gate.ID}
		purpose := "merge the build branch onto this gate's target"
		if targetsMain {
			argv = append(argv, "--confirm", "<operator-approved merge message>")
			purpose = "merge onto the main branch: re-sign every commit, sign the merge commit, and use the operator-approved message"
		}
		d.Commands = append(d.Commands, Command{Purpose: purpose, Argv: argv})
	}
	return d
}

// blockingFindings returns the ids of open findings at or above threshold.
func blockingFindings(open []Finding, threshold int) []string {
	var out []string
	for _, f := range open {
		if f.Criticality >= threshold {
			out = append(out, f.ID)
		}
	}
	return out
}

// microUSD converts dollars to the integer unit the band comparison works in,
// so a ceiling check never turns on floating-point rounding.
func microUSD(usd float64) int { return int(math.Round(usd * 1e6)) }

// budgetExceeded reports whether the effort has spent past its ceiling.
//
// The comparison is the shared band primitive with the ceiling as its
// parameter — there is no second implementation of "compare a figure to a
// range" here, and no bound is written into this package.
//
// Spend that could not be measured never satisfies the check: an unknown
// total is unenforceable and is reported as such, rather than being read as
// "under budget".
func budgetExceeded(b dag.Budget) (bool, string) {
	if b.EnforcedAt == "never" || b.CeilingUSD <= 0 || !b.Spend().Known {
		return false, ""
	}
	if gate.Band(microUSD(b.SpentUSD), 0, microUSD(b.CeilingUSD)) != gate.VerdictWarn {
		return false, ""
	}
	return true, fmt.Sprintf("spend %.2f USD has passed the %.2f USD ceiling enforced at each %s boundary", b.SpentUSD, b.CeilingUSD, b.EnforcedAt)
}
