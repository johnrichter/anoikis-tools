package engine_test

import (
	"slices"
	"testing"

	"github.com/johnrichter/anoikis-tools/internal/dag"
	"github.com/johnrichter/anoikis-tools/internal/engine"
	"github.com/johnrichter/anoikis-tools/internal/ids"
	"github.com/johnrichter/anoikis-tools/internal/policy"
	"github.com/johnrichter/claude-shared-tooling/go/graph"
)

const examplePolicy = "../../examples/harness-policy.json"

// harness loads the policy shipped with the repo, so every directive test is
// driven by injected policy rather than anything the engine knows itself.
func harness(t *testing.T) *policy.Harness {
	t.Helper()
	h, err := policy.Load(examplePolicy)
	if err != nil {
		t.Fatalf("load example policy: %v", err)
	}
	return h
}

func prover(t *testing.T, h *policy.Harness) *graph.Prover {
	t.Helper()
	p, err := h.Prover()
	if err != nil {
		t.Fatalf("build prover: %v", err)
	}
	return p
}

func node(id, dir string, status dag.Status, deps ...string) dag.Node {
	return dag.Node{
		ID: id, Title: id, Status: status, Deps: deps,
		Surface:    []dag.Claim{{Domain: "path", Kind: graph.PathDir, Value: dir}},
		VerifyTier: dag.VerifyCheap, DetailRef: "nodes/" + id + ".json", MaxAttempts: 1,
	}
}

// state builds a one-gate effort around the given nodes.
func state(nodes []dag.Node, gateStatus dag.GateStatus) dag.State {
	return dag.State{
		Project: dag.Project{
			SchemaVersion: dag.SchemaVersion, ID: "effort", Name: "Effort", Version: "1.0.0", Status: "building",
			Budget:  dag.Budget{CeilingUSD: 100, EnforcedAt: "layer"},
			Signing: dag.Signing{BelowMain: "never", MainMerge: "resign-all+sign-merge-commit"},
		},
		Shards: []dag.Shard{{SchemaVersion: dag.SchemaVersion, GateID: "g1", Nodes: nodes}},
		Gates: dag.Gates{SchemaVersion: dag.SchemaVersion, Gates: []dag.Gate{{
			ID: "g1", Name: "Gate one", Status: gateStatus,
			Policy: dag.GatePolicy{Pause: true, DeepReview: "batched", MergeTarget: "main", Sign: "inherit"},
		}}},
	}
}

func step(t *testing.T, st dag.State, open ...engine.Finding) engine.Directive {
	t.Helper()
	h := harness(t)
	d, err := engine.Step(st, h, ids.Opaque{}, prover(t, h), open, engine.Env{Tool: "anoikis", Effort: "e", BaseRef: "abc123"})
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	return d
}

func TestReadyNodesLaunch(t *testing.T) {
	d := step(t, state([]dag.Node{node("a", "svc/a", dag.StatusReady), node("b", "svc/b", dag.StatusReady)}, dag.GatePending))
	if d.Action != engine.ActionLaunch {
		t.Fatalf("action = %s, want launch (%s: %s)", d.Action, d.Cause, d.Reason)
	}
	if len(d.Launch.Members) != 2 {
		t.Errorf("members = %v, want both disjoint nodes", d.Launch.Members)
	}
	if d.Launch.BaseRef != "abc123" {
		t.Errorf("base_ref = %q, want the injected build head", d.Launch.BaseRef)
	}
	if len(d.Commands) == 0 || d.Commands[0].Argv[0] != "anoikis" {
		t.Errorf("launch emitted no self-invocation to run: %+v", d.Commands)
	}
}

func TestRunsInFlightPause(t *testing.T) {
	d := step(t, state([]dag.Node{node("a", "svc/a", dag.StatusRunning), node("b", "svc/b", dag.StatusReady)}, dag.GatePending))
	if d.Action != engine.ActionPause || d.Cause != engine.CauseRunsInFlight {
		t.Fatalf("action = %s/%s, want pause/runs-in-flight", d.Action, d.Cause)
	}
	if !slices.Contains(d.Subjects, "a") {
		t.Errorf("pause did not name the in-flight node: %v", d.Subjects)
	}
}

func TestSettledShardReachesItsGate(t *testing.T) {
	d := step(t, state([]dag.Node{node("a", "svc/a", dag.StatusDone), node("b", "svc/b", dag.StatusDone)}, dag.GatePending))
	if d.Action != engine.ActionGate {
		t.Fatalf("action = %s, want gate", d.Action)
	}
	if !d.Gate.TargetsMain {
		t.Error("a gate targeting the policy's main branch was not marked as such")
	}
	if len(d.Commands) != 2 {
		t.Fatalf("a reviewed gate emitted %+v, want the review to close it and then the merge", d.Commands)
	}
	if !slices.Contains(d.Commands[0].Argv, "close-gate") || !slices.Contains(d.Commands[0].Argv, "--verdict") {
		t.Errorf("the gate does not ask for its review verdict first: %+v", d.Commands[0])
	}
	if !slices.Contains(d.Commands[1].Argv, "--confirm") {
		t.Errorf("a main-branch merge was emitted without an operator confirmation: %+v", d.Commands[1])
	}
}

func TestAPassedGateStillOwesItsMerge(t *testing.T) {
	st := state([]dag.Node{node("a", "svc/a", dag.StatusDone)}, dag.GatePassed)
	d := step(t, st)
	if d.Action != engine.ActionGate {
		t.Fatalf("action = %s, want gate: a reviewed-but-unmerged gate is not complete", d.Action)
	}
	if len(d.Commands) != 1 || !slices.Contains(d.Commands[0].Argv, "merge-gate") {
		t.Errorf("a passed gate emitted %+v, want only the outstanding merge", d.Commands)
	}
}

func TestAGateWithNothingLeftToDoCloses(t *testing.T) {
	st := state([]dag.Node{node("a", "svc/a", dag.StatusDone)}, dag.GatePassed)
	st.Gates.Gates[0].Policy.MergeTarget = dag.MergeTargetNone
	if d := step(t, st); d.Action != engine.ActionStop {
		t.Fatalf("action = %s/%s, want stop: a passed gate with no merge target is closed", d.Action, d.Cause)
	}
}

func TestCompletedGraphStops(t *testing.T) {
	d := step(t, state([]dag.Node{node("a", "svc/a", dag.StatusDone)}, dag.GateMerged))
	if d.Action != engine.ActionStop {
		t.Fatalf("action = %s, want stop", d.Action)
	}
	if d.Summary.Nodes != 1 {
		t.Errorf("summary counted %d nodes, want 1", d.Summary.Nodes)
	}
}

func TestExhaustedNodeHalts(t *testing.T) {
	n := node("a", "svc/a", dag.StatusFailed)
	n.Attempts = 1
	d := step(t, state([]dag.Node{n}, dag.GatePending))
	if d.Action != engine.ActionHalt || d.Cause != engine.CauseFailedNode {
		t.Fatalf("action = %s/%s, want halt/failed-node", d.Action, d.Cause)
	}
}

func TestFailedNodeWithAnAttemptLeftRelaunches(t *testing.T) {
	n := node("a", "svc/a", dag.StatusFailed)
	n.Attempts, n.MaxAttempts = 1, 2
	d := step(t, state([]dag.Node{n}, dag.GatePending))
	if d.Action != engine.ActionLaunch {
		t.Fatalf("action = %s/%s, want launch", d.Action, d.Cause)
	}
}

func TestBlockingFindingHalts(t *testing.T) {
	st := state([]dag.Node{node("a", "svc/a", dag.StatusReady)}, dag.GatePending)
	d := step(t, st, engine.Finding{ID: "ENTRY-0001", Statement: "the schema is wrong", Criticality: 20})
	if d.Action != engine.ActionHalt || d.Cause != engine.CauseBlockingFinding {
		t.Fatalf("action = %s/%s, want halt/blocking-finding", d.Action, d.Cause)
	}
	if !slices.Contains(d.Subjects, "ENTRY-0001") {
		t.Errorf("halt did not name the blocking finding: %v", d.Subjects)
	}
}

func TestFindingBelowThresholdDoesNotHalt(t *testing.T) {
	st := state([]dag.Node{node("a", "svc/a", dag.StatusReady)}, dag.GatePending)
	d := step(t, st, engine.Finding{ID: "ENTRY-0002", Statement: "a nit", Criticality: 4})
	if d.Action != engine.ActionLaunch {
		t.Fatalf("action = %s/%s, want launch", d.Action, d.Cause)
	}
}

func TestBudgetHaltsOnlyOnMeasuredSpend(t *testing.T) {
	st := state([]dag.Node{node("a", "svc/a", dag.StatusReady)}, dag.GatePending)
	st.Project.Budget = dag.Budget{CeilingUSD: 10, SpentUSD: 25, UnpricedRuns: 1, EnforcedAt: "layer"}
	if d := step(t, st); d.Action == engine.ActionHalt && d.Cause == engine.CauseBudget {
		t.Fatal("an unmeasured spend total enforced a ceiling; unknown must never read as over or under budget")
	}
	st.Project.Budget.UnpricedRuns = 0
	d := step(t, st)
	if d.Action != engine.ActionHalt || d.Cause != engine.CauseBudget {
		t.Fatalf("action = %s/%s, want halt/budget", d.Action, d.Cause)
	}
}

func TestCycleHalts(t *testing.T) {
	a := node("a", "svc/a", dag.StatusReady, "b")
	b := node("b", "svc/b", dag.StatusReady, "a")
	d := step(t, state([]dag.Node{a, b}, dag.GatePending))
	if d.Action != engine.ActionHalt || d.Cause != engine.CauseGraphCycle {
		t.Fatalf("action = %s/%s, want halt/graph-cycle", d.Action, d.Cause)
	}
}

func TestNothingDispatchableHalts(t *testing.T) {
	a := node("a", "svc/a", dag.StatusFailed)
	a.Attempts = 1
	b := node("b", "svc/b", dag.StatusReady, "a")
	st := state([]dag.Node{a, b}, dag.GatePending)
	// Clear the exhausted-node halt so the blockage itself is what is under
	// test: a node whose only dependency will never settle.
	st.Shards[0].Nodes[0].Status = dag.StatusBlocked
	st.Shards[0].Nodes[0].Surface = nil
	st.Shards[0].Nodes[0].NeverDispatch = true
	d := step(t, st)
	if d.Action != engine.ActionHalt || d.Cause != engine.CauseBlocked {
		t.Fatalf("action = %s/%s, want halt/blocked", d.Action, d.Cause)
	}
}

func TestNeverDispatchNodeIsNeverLaunched(t *testing.T) {
	a := node("a", "svc/a", dag.StatusReady)
	a.NeverDispatch = true
	b := node("b", "svc/b", dag.StatusReady)
	d := step(t, state([]dag.Node{a, b}, dag.GatePending))
	if d.Action != engine.ActionLaunch {
		t.Fatalf("action = %s/%s, want launch", d.Action, d.Cause)
	}
	if slices.Contains(d.Launch.Members, "a") {
		t.Errorf("a never-dispatch node was handed to an agent: %v", d.Launch.Members)
	}
}

func TestStepIsDeterministic(t *testing.T) {
	st := state([]dag.Node{node("a", "svc/a", dag.StatusReady), node("b", "svc/a/inner", dag.StatusReady), node("c", "svc/c", dag.StatusReady)}, dag.GatePending)
	first := step(t, st)
	for i := range 10 {
		next := step(t, st)
		if next.Action != first.Action || !slices.Equal(next.Launch.Members, first.Launch.Members) {
			t.Fatalf("run %d produced %s/%v, first run produced %s/%v", i, next.Action, next.Launch.Members, first.Action, first.Launch.Members)
		}
	}
}
