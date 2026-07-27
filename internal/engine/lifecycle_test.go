package engine_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/johnrichter/anoikis-tools/internal/dag"
	"github.com/johnrichter/anoikis-tools/internal/engine"
	"github.com/johnrichter/anoikis-tools/internal/ids"
)

// env is the world outside the artifacts these tests hand the engine.
var env = engine.Env{Tool: "anoikis", Effort: "e", BaseRef: "abc123"}

// logged builds a state carrying a run log and nothing else of interest.
func logged(events ...dag.LogEvent) dag.State {
	st := state([]dag.Node{node("a", "svc/a", dag.StatusRunning)}, dag.GatePending)
	st.Events = events
	return st
}

func ev(runID, nodeID string, kind dag.Event) dag.LogEvent {
	return dag.LogEvent{
		TS: "2026-01-01T00:00:00Z", RunID: runID, NodeID: nodeID, Event: kind,
		BaseRef: "abc123", WorktreeRef: "e/" + nodeID + "-l0", PromptRef: "prompts/" + runID + ".txt",
	}
}

func itemFor(t *testing.T, plan engine.ResumePlan, runID string) engine.ResumeItem {
	t.Helper()
	for _, it := range plan.Items {
		if it.RunID == runID {
			return it
		}
	}
	t.Fatalf("resume plan has no item for run %s", runID)
	return engine.ResumeItem{}
}

func TestResumeClassifiesByLatestEvent(t *testing.T) {
	plan := engine.Resume(logged(
		ev("r1", "a", dag.EventDispatched), ev("r1", "a", dag.EventComplete), ev("r1", "a", dag.EventMerged),
		ev("r2", "b", dag.EventDispatched), ev("r2", "b", dag.EventComplete),
		ev("r3", "c", dag.EventDispatched),
	), 0, "", env)

	if got := itemFor(t, plan, "r1").Action; got != engine.ResumeSkip {
		t.Errorf("a merged run resumed as %s, want skip", got)
	}
	if got := itemFor(t, plan, "r2").Action; got != engine.ResumeRerecord {
		t.Errorf("a completed but unmerged run resumed as %s, want rerecord", got)
	}
	r3 := itemFor(t, plan, "r3")
	if r3.Action != engine.ResumeReissue {
		t.Errorf("an interrupted run resumed as %s, want reissue", r3.Action)
	}
	if r3.BaseRef != "abc123" || r3.PromptRef == "" {
		t.Errorf("a reissue lost the base ref or prompt it must replay from: %+v", r3)
	}
}

func TestResumeEmitsBothFollowUps(t *testing.T) {
	plan := engine.Resume(logged(
		ev("r1", "a", dag.EventDispatched),
		ev("r2", "b", dag.EventDispatched), ev("r2", "b", dag.EventFailed),
	), 0, "", env)
	var purposes []string
	for _, c := range plan.Commands {
		purposes = append(purposes, strings.Join(c.Argv, " "))
	}
	if len(plan.Commands) != 2 {
		t.Fatalf("commands = %v, want one for recording and one for reissuing", purposes)
	}
}

func TestResumeCarriesRunLogDamageAsACaveat(t *testing.T) {
	plan := engine.Resume(logged(ev("r1", "a", dag.EventDispatched)), 1, "final run-log line has no terminator", env)
	if plan.Damaged != 1 || plan.DamageDetail == "" {
		t.Fatalf("resume dropped the damage report: %+v", plan)
	}
	if len(plan.Reissued()) != 1 {
		t.Errorf("damage suppressed the reissue of the run that survived it")
	}
}

func TestResumeOfAnUnjournalledDispatchIsSimplyAbsent(t *testing.T) {
	plan := engine.Resume(logged(), 1, "final run-log line has no terminator", env)
	if len(plan.Items) != 0 {
		t.Fatalf("a run whose dispatch was never journalled appeared in the plan: %+v", plan.Items)
	}
}

func TestApplySplitsPassAndFail(t *testing.T) {
	st := state([]dag.Node{node("a", "svc/a", dag.StatusRunning), node("b", "svc/b", dag.StatusRunning)}, dag.GatePending)
	rec, err := engine.Apply(st, harness(t), []engine.Outcome{
		{Result: dag.RunResult{NodeID: "a", RunID: "r1", Status: dag.RunPass}, Usage: dag.Usage{Known: true, CostUSD: 1.5}},
		{Result: dag.RunResult{NodeID: "b", RunID: "r2", Status: dag.RunFail}, Usage: dag.Usage{Known: true, CostUSD: 0.5}},
	}, 0, "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !slices.Equal(rec.Mergeable, []string{"a"}) || !slices.Equal(rec.Failed, []string{"b"}) {
		t.Fatalf("mergeable=%v failed=%v, want [a] and [b]", rec.Mergeable, rec.Failed)
	}
	if !rec.Spend.Known || rec.Spend.CostUSD != 2 {
		t.Errorf("spend = %+v, want a known total of 2", rec.Spend)
	}
}

func TestOneUnpricedRunMakesTheBatchTotalUnknown(t *testing.T) {
	st := state([]dag.Node{node("a", "svc/a", dag.StatusRunning), node("b", "svc/b", dag.StatusRunning)}, dag.GatePending)
	rec, err := engine.Apply(st, harness(t), []engine.Outcome{
		{Result: dag.RunResult{NodeID: "a", RunID: "r1", Status: dag.RunPass}, Usage: dag.Usage{Known: true, CostUSD: 1.5}},
		{Result: dag.RunResult{NodeID: "b", RunID: "r2", Status: dag.RunPass}, Usage: dag.Usage{Known: false, Reason: "no provider"}},
	}, 0, "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if rec.Spend.Known {
		t.Error("an unpriced run was absorbed into a known total")
	}
	if rec.Spend.Reason == "" {
		t.Error("an unknown total carries no reason")
	}
}

func TestApplyRefusesAnUndeclaredVerdict(t *testing.T) {
	st := state([]dag.Node{node("a", "svc/a", dag.StatusRunning)}, dag.GatePending)
	_, err := engine.Apply(st, harness(t), []engine.Outcome{
		{Result: dag.RunResult{NodeID: "a", RunID: "r1", Status: dag.RunPass, Verdict: "looks-fine"}},
	}, 0, "2026-01-01T00:00:00Z")
	if err == nil {
		t.Fatal("a verdict outside the harness's declared vocabulary was accepted")
	}
}

func TestApplySurfacesTheFixVerdict(t *testing.T) {
	st := state([]dag.Node{node("a", "svc/a", dag.StatusRunning)}, dag.GatePending)
	rec, err := engine.Apply(st, harness(t), []engine.Outcome{
		{Result: dag.RunResult{NodeID: "a", RunID: "r1", Status: dag.RunPass, Verdict: "fix"}},
	}, 0, "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !slices.Equal(rec.FixVerdicts, []string{"a"}) {
		t.Fatalf("fix verdicts = %v, want [a]", rec.FixVerdicts)
	}
}

func TestOnlyAMergeMakesANodeDone(t *testing.T) {
	st := state([]dag.Node{node("a", "svc/a", dag.StatusRunning)}, dag.GatePending)
	rec, err := engine.Apply(st, harness(t), []engine.Outcome{
		{Result: dag.RunResult{NodeID: "a", RunID: "r1", Status: dag.RunPass}},
	}, 0, "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if rec.Shards[0].Nodes[0].Status == dag.StatusDone {
		t.Fatal("a node reached done on agent completion, before its work was merged")
	}
	shards, events := engine.Settle(rec.Shards, rec.Mergeable, rec.Runs, 0, "2026-01-01T00:00:00Z")
	if shards[0].Nodes[0].Status != dag.StatusDone {
		t.Fatalf("status after the merge settled = %s, want done", shards[0].Nodes[0].Status)
	}
	if len(events) != 1 || events[0].Event != dag.EventMerged {
		t.Fatalf("settling journalled %+v, want one merged event", events)
	}
	if events[0].RunID != "r1" {
		t.Errorf("the merged event names run %q, not the run that produced the work; a resume would see two runs", events[0].RunID)
	}
}

func TestRetireLeavesATombstoneAndRepointsTheDetail(t *testing.T) {
	st := state([]dag.Node{node("a", "svc/a", dag.StatusDone)}, dag.GatePending)
	shards := engine.Retire(st.Shards, []engine.Closure{{
		NodeID:    "a",
		DetailRef: "archive/nodes/a.json",
		Tombstone: dag.Tombstone{Summary: "merged in layer 0", CostUSD: 2, CostKnown: true},
	}})
	n := shards[0].Nodes[0]
	if n.Status != dag.StatusArchived || n.Tombstone == nil {
		t.Fatalf("retired node = %+v, want archived with a tombstone", n)
	}
	if n.DetailRef != "archive/nodes/a.json" {
		t.Errorf("detail ref = %q, want the archived record", n.DetailRef)
	}
	if len(n.Surface) == 0 {
		t.Error("a retired node lost its declared surface; a fix grafted onto it could no longer claim what it claimed")
	}
}

func TestGraftDependsOnEveryReviewedNodeAndUnionsTheirSurfaces(t *testing.T) {
	st := state([]dag.Node{node("a", "svc/a", dag.StatusDone), node("b", "svc/b", dag.StatusDone)}, dag.GatePending)
	details := map[string]dag.Detail{
		"a": {ID: "a", DeliverableKind: dag.KindCode},
		"b": {ID: "b", DeliverableKind: dag.KindCode},
	}
	g, err := engine.PlanGraft(st, harness(t), ids.Opaque{}, details, []string{"a", "b"}, "results/review.json", "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("plan graft: %v", err)
	}
	if !slices.Equal(g.Node.Deps, []string{"a", "b"}) {
		t.Errorf("graft deps = %v, want both reviewed nodes", g.Node.Deps)
	}
	if len(g.Node.Surface) != 2 {
		t.Errorf("graft surface = %v, want the union of both reviewed surfaces", g.Node.Surface)
	}
	if g.Node.VerifyTier != dag.VerifyImmediateDeep {
		t.Errorf("graft verify tier = %s, want immediate_deep", g.Node.VerifyTier)
	}
	if g.Event.Event != dag.EventGrafted {
		t.Errorf("graft journalled %s, want grafted", g.Event.Event)
	}
	if len(g.Detail.Stages) == 0 {
		t.Error("graft resolved no stages from the route")
	}
}

func TestGraftIsStableForTheSameReview(t *testing.T) {
	st := state([]dag.Node{node("a", "svc/a", dag.StatusDone)}, dag.GatePending)
	details := map[string]dag.Detail{"a": {ID: "a", DeliverableKind: dag.KindCode}}
	first, err := engine.PlanGraft(st, harness(t), ids.Opaque{}, details, []string{"a"}, "results/review.json", "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("plan graft: %v", err)
	}
	again, err := engine.PlanGraft(st, harness(t), ids.Opaque{}, details, []string{"a"}, "results/review.json", "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("plan graft again: %v", err)
	}
	if first.Node.ID != again.Node.ID {
		t.Errorf("the same review derived two ids: %s and %s", first.Node.ID, again.Node.ID)
	}
}

func TestGraftRefusesToSpanDeliverableKinds(t *testing.T) {
	st := state([]dag.Node{node("a", "svc/a", dag.StatusDone), node("b", "svc/b", dag.StatusDone)}, dag.GatePending)
	details := map[string]dag.Detail{
		"a": {ID: "a", DeliverableKind: dag.KindCode},
		"b": {ID: "b", DeliverableKind: dag.KindDocs},
	}
	if _, err := engine.PlanGraft(st, harness(t), ids.Opaque{}, details, []string{"a", "b"}, "results/review.json", "2026-01-01T00:00:00Z"); err == nil {
		t.Fatal("a fix node spanning two deliverable kinds was planned; its routing would have been a guess")
	}
}

func TestPlanDispatchRoutesByDeliverableKind(t *testing.T) {
	st := state([]dag.Node{node("a", "svc/a", dag.StatusReady)}, dag.GatePending)
	details := map[string]dag.Detail{"a": {
		ID: "a", Intent: "do the thing", DeliverableKind: dag.KindDocs,
		Acceptance: []string{"it is done"},
	}}
	got, err := engine.PlanDispatch(st, harness(t), details, []string{"a"}, 0, env)
	if err != nil {
		t.Fatalf("plan dispatch: %v", err)
	}
	if len(got) != 1 || len(got[0].Stages) != 1 || got[0].Stages[0].Stage != "build" {
		t.Fatalf("docs routed to %+v, want the single build stage its route declares", got)
	}
	if got[0].Stages[0].Agent == "" {
		t.Error("a stage resolved to no agent")
	}
	if !strings.Contains(got[0].Prompt, "Return contract") {
		t.Error("the rendered prompt omits the two-channel return contract")
	}
	if got[0].RunID == "" || got[0].WorktreeRef == "" || got[0].BaseRef != env.BaseRef {
		t.Errorf("dispatch is missing its durable identity: %+v", got[0])
	}
}

func TestPlanDispatchRefusesANeverDispatchNode(t *testing.T) {
	n := node("a", "svc/a", dag.StatusReady)
	n.NeverDispatch = true
	st := state([]dag.Node{n}, dag.GatePending)
	details := map[string]dag.Detail{"a": {ID: "a", DeliverableKind: dag.KindCode}}
	if _, err := engine.PlanDispatch(st, harness(t), details, []string{"a"}, 0, env); err == nil {
		t.Fatal("a never-dispatch node was planned for an agent")
	}
}

func TestApplyIsIdempotentAfterARefusedMerge(t *testing.T) {
	st := state([]dag.Node{node("a", "svc/a", dag.StatusRunning)}, dag.GatePending)
	outcomes := []engine.Outcome{{
		Result: dag.RunResult{NodeID: "a", RunID: "r1", Status: dag.RunPass,
			Findings: []dag.FindingSeed{{Statement: "worth a benchmark", Impact: 2, Urgency: 1}}},
	}}
	first, err := engine.Apply(st, harness(t), outcomes, 0, "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(first.Events) != 1 || len(first.Findings) != 1 {
		t.Fatalf("first apply produced %d events and %d findings, want 1 and 1", len(first.Events), len(first.Findings))
	}
	if first.Shards[0].Nodes[0].Attempts != 1 {
		t.Fatalf("attempts after one apply = %d, want 1", first.Shards[0].Nodes[0].Attempts)
	}

	// The merge behind the first apply did not land, so the caller runs the
	// same command again against a state that now carries the journalled
	// completion.
	st.Shards = first.Shards
	st.Events = append(st.Events, first.Events...)
	second, err := engine.Apply(st, harness(t), outcomes, 0, "2026-01-01T00:00:01Z")
	if err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	if len(second.Events) != 0 || len(second.Findings) != 0 {
		t.Errorf("re-applying journalled %d events and raised %d findings; both must be zero", len(second.Events), len(second.Findings))
	}
	if second.Shards[0].Nodes[0].Attempts != 1 {
		t.Errorf("re-applying burned a second attempt: %d", second.Shards[0].Nodes[0].Attempts)
	}
	if !slices.Equal(second.Mergeable, []string{"a"}) {
		t.Errorf("re-applying reported %v as mergeable; the merge still needs to happen", second.Mergeable)
	}
}

func TestFindingsAreAttributedToTheirNode(t *testing.T) {
	st := state([]dag.Node{node("a", "svc/a", dag.StatusRunning), node("b", "svc/b", dag.StatusRunning)}, dag.GatePending)
	rec, err := engine.Apply(st, harness(t), []engine.Outcome{
		{Result: dag.RunResult{NodeID: "a", RunID: "r1", Status: dag.RunPass,
			Findings: []dag.FindingSeed{{Statement: "same observation", Impact: 2, Urgency: 1}}}},
		{Result: dag.RunResult{NodeID: "b", RunID: "r2", Status: dag.RunPass,
			Findings: []dag.FindingSeed{{Statement: "same observation", Impact: 2, Urgency: 1}}}},
	}, 0, "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(rec.Findings) != 2 || rec.Findings[0].Seed.Statement == rec.Findings[1].Seed.Statement {
		t.Fatalf("two nodes raising one observation collapsed to %+v", rec.Findings)
	}
	if rec.Findings[0].NodeID != "a" || !strings.HasPrefix(rec.Findings[0].Seed.Statement, "a: ") {
		t.Errorf("finding is not attributed to its node: %+v", rec.Findings[0])
	}
}

func TestApplyIgnoresAnOutcomeForAlreadyMergedWork(t *testing.T) {
	st := state([]dag.Node{node("a", "svc/a", dag.StatusDone)}, dag.GatePending)
	rec, err := engine.Apply(st, harness(t), []engine.Outcome{
		{Result: dag.RunResult{NodeID: "a", RunID: "r1", Status: dag.RunPass,
			Findings: []dag.FindingSeed{{Statement: "worth a benchmark", Impact: 2, Urgency: 1}}}},
	}, 0, "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(rec.Events) != 0 || len(rec.Findings) != 0 || len(rec.Mergeable) != 0 {
		t.Fatalf("an outcome for merged work was applied: %+v", rec)
	}
	if rec.Shards[0].Nodes[0].Attempts != 0 {
		t.Errorf("an outcome for merged work burned an attempt: %d", rec.Shards[0].Nodes[0].Attempts)
	}
}
