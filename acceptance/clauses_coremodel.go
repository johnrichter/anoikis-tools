package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/johnrichter/anoikis-tools/internal/admission"
	"github.com/johnrichter/anoikis-tools/internal/dag"
	"github.com/johnrichter/anoikis-tools/internal/effort"
	"github.com/johnrichter/anoikis-tools/internal/engine"
	"github.com/johnrichter/anoikis-tools/internal/ids"
	"github.com/johnrichter/anoikis-tools/internal/policy"
	"github.com/johnrichter/anoikis-tools/internal/usage"
	"github.com/johnrichter/anoikis-tools/internal/vcs"
	"github.com/johnrichter/claude-shared-tooling/go/graph"
)

// coreModelClauses are the invariants of the work-graph core model: how the
// build loop is driven, what the artifacts guarantee, and which facts have
// exactly one home.
func coreModelClauses() []Clause {
	return []Clause{{
		ID:       "core-model/one-next-directive",
		Source:   SourceCoreModel,
		Bar:      BarBuild,
		Requires: "The driver is handed exactly one next action, drawn from a closed set of five, with the judgement already made in code.",
		Asserts:  "Every fixture state yields an action in the closed set; the five actions are each reachable; a directive never carries more than one action's payload.",
		check:    oneNextDirective,
	}, {
		ID:       "core-model/driver-composes-no-version-control",
		Source:   SourceCoreModel,
		Bar:      BarBuild,
		Requires: "Every command a directive emits is an invocation of this engine, never a version-control command the driver assembles itself.",
		Asserts:  "Every command emitted across every fixture state names this engine as its program; the version-control binary is executed from exactly one package.",
		check:    driverComposesNoVersionControl,
	}, {
		ID:       "core-model/admission-proves-disjointness",
		Source:   SourceCoreModel,
		Bar:      BarBuild,
		Requires: "Two nodes share a batch only when the dependency graph proves them independent and their declared surfaces are proven disjoint; anything unproven runs alone.",
		Asserts:  "Nodes with disjoint claims are admitted together; nodes claiming the same directory are not; a node claiming nothing is admitted alone, and each exclusion is reported with the reason it was held back.",
		check:    admissionProvesDisjointness,
	}, {
		ID:       "core-model/post-merge-backstop-is-always-on",
		Source:   SourceCoreModel,
		Bar:      BarBuild,
		Requires: "Every layer merge is followed by building the merged result and re-asserting that every changed path was declared; the check is not optional.",
		Asserts:  "The shipped harness declares a backstop command; a harness policy declaring none is refused when it loads; the backstop refuses to run with no command; the surface re-assertion reports a changed path no merged node declared and accepts one that a declared pattern covers.",
		check:    backstopAlwaysOn,
	}, {
		ID:       "core-model/two-merges-kept-apart",
		Source:   SourceCoreModel,
		Bar:      BarBuild,
		Requires: "A layer merge is autonomous and unsigned; the merge onto the declared main branch is the only one that re-signs every commit, signs the merge commit and requires an operator-approved message.",
		Asserts:  "A gate targeting the harness's main branch is marked as such and its emitted merge asks for an operator confirmation; a gate targeting any other branch is not and does not; the gate merge refuses a main-branch plan carrying no approved message, and one carrying no explicit re-signing range, before it touches the repository.",
		check:    twoMergesKeptApart,
	}, {
		ID:       "core-model/done-means-merged",
		Source:   SourceCoreModel,
		Bar:      BarBuild,
		Requires: "A node is done only once its work is merged onto the build branch, so a later node branches from a base containing its dependencies' work.",
		Asserts:  "Recording a passing run leaves the node unsettled and merely mergeable; only settling the merge marks it done and journals a merged transition; a node whose dependency is unmerged is not ready.",
		check:    doneMeansMerged,
	}, {
		ID:       "core-model/run-log-is-append-only-and-atomic",
		Source:   SourceCoreModel,
		Bar:      BarBuild,
		Requires: "The run log is append-only, one line per transition, each line small enough to land in a single atomic append, with one writer.",
		Asserts:  "The line bound is at most the single-append size; an event that would exceed it is refused rather than truncated; appending never rewrites what is already there; a line damaged by a kill is skipped as damage rather than read as an event.",
		check:    runLogAppendOnly,
	}, {
		ID:       "core-model/resume-reads-the-tail",
		Source:   SourceCoreModel,
		Bar:      BarBuild,
		Requires: "A resume reads the run log from the cursor onward, never the whole history.",
		Asserts:  "A scan from a saved cursor returns only the events after it and reports the offset past the last well-formed one; the cursor round-trips through its own store.",
		check:    resumeReadsTheTail,
	}, {
		ID:       "core-model/archived-node-leaves-a-tombstone",
		Source:   SourceCoreModel,
		Bar:      BarBuild,
		Requires: "A closed node's detail moves to the archive by rename and leaves a tombstone in its shard, so a later node's archived dependency still resolves.",
		Asserts:  "Archiving moves the detail record out of the hot path and it stays readable; retiring a node leaves it archived with a tombstone in the shard; a live node depending on it is ready.",
		check:    archivedNodeLeavesATombstone,
	}, {
		ID:       "core-model/one-mechanical-graph-mutation",
		Source:   SourceCoreModel,
		Bar:      BarBuild,
		Requires: "The only build-time change to the graph is the fix node a review's fix verdict calls for: it depends on exactly the nodes reviewed, claims the union of their surfaces, and is seeded by the review's findings.",
		Asserts:  "A planned graft depends on exactly the reviewed nodes, claims every claim they declared and nothing else, carries the findings reference as its input, and lands in the gate the reviewed nodes belong to.",
		check:    oneMechanicalGraphMutation,
	}, {
		ID:       "core-model/one-home-per-fact",
		Source:   SourceCoreModel,
		Bar:      BarBuild,
		Requires: "Gate membership lives on the shard a node is in; gate policy carries no member list; the signing policy lives only on the manifest.",
		Asserts:  "The gate contract declares no member list; the shard contract carries the gate identity; the signing policy appears in the manifest contract only, and a gate carries nothing but whether it inherits it.",
		check:    oneHomePerFact,
	}, {
		ID:       "core-model/spend-is-priced-never-self-reported",
		Source:   SourceCoreModel,
		Bar:      BarBuild,
		Requires: "What a run cost comes from a spend provider, never from the run: a run reports only where it executed. An unpriced run is unknown with a reason, never zero, and a total holding one says so.",
		Asserts:  "The run-result contract carries attribution and no cost or token field; a provider with no source answers unknown with a reason; folding an unknown into a total makes the total unknown; an effort holding an unpriced run reports its spend as a floor; all four token classes are carried.",
		check:    spendIsPriced,
	}, {
		ID:       "core-model/undispatchable-nodes-are-refused",
		Source:   SourceCoreModel,
		Bar:      BarBuild,
		Requires: "A node marked as never dispatched is structurally refused to a subagent; it is work the engine performs itself.",
		Asserts:  "Such a node is never returned as ready and never admitted to a batch, even with every dependency settled.",
		check:    undispatchableNodesRefused,
	}, {
		ID:       "core-model/review-is-selected-by-tier-not-staged",
		Source:   SourceCoreModel,
		Bar:      BarBuild,
		Requires: "Review is chosen solely by a node's verification tier and is never also a stage; a review role never authors artifacts.",
		Asserts:  "The node contract requires a verification tier from the closed set; no stage in the shipped harness policy is a review stage; a policy naming a builder as its review role is refused.",
		check:    reviewSelectedByTier,
	}, {
		ID:       "core-model/halt-causes-are-a-closed-set",
		Source:   SourceCoreModel,
		Bar:      BarBuild,
		Requires: "A pause or halt names its cause from a closed vocabulary covering every condition the model enumerates, so a driver branches on the cause rather than parsing prose.",
		Asserts:  "Every fixture pause or halt carries a cause and a reason; a dependency cycle, the spend ceiling, an unready plan and a blockage each yield the cause that names them; the two merge-time causes, a surface overlap and a failed backstop, are raised somewhere and not merely declared.",
		check:    haltCausesClosed,
	}, {
		ID:       "core-model/one-exclusion-constant",
		Source:   SourceCoreModel,
		Bar:      BarBuild,
		Requires: "One constant names every uncommitted class of effort file, and every enumerator that must agree about it reads that constant.",
		Asserts:  "Every ephemeral directory the constant names is ignored by the repository's ignore file and is created when an effort is created; no committed artifact path falls inside one.",
		check:    oneExclusionConstant,
	}, {
		ID:       "core-model/results-are-durable-with-no-parallel-store",
		Source:   SourceCoreModel,
		Bar:      BarBuild,
		Requires: "A run's durable record carries its structured diagnostics and the failing excerpt, because the raw log dies with the worktree; there is no second store for errors.",
		Asserts:  "The run-result contract carries structured diagnostics, an overflow count and an excerpt; the effort layout declares no separate error store; the raw log directory is ephemeral while results are not.",
		check:    resultsAreDurable,
	}, {
		ID:       "core-model/surface-claims-are-typed-and-checked-both-ways",
		Source:   SourceCoreModel,
		Bar:      BarBuild,
		Requires: "A surface claim is typed, and the same dialect decides both the disjointness proof made before a run and the re-assertion made against what the merge landed.",
		Asserts:  "The claim contract carries a domain, a kind and a value, and the shipped harness registers the domain that decides them; the post-merge re-assertion accepts a path covered by a declared directory, file or recursive pattern, reports one no node declared, and treats an untyped claim as covering nothing.",
		check:    surfaceClaimsTyped,
	}, {
		ID:       "core-model/findings-split-blocking-from-deferred",
		Source:   SourceCoreModel,
		Bar:      BarBuild,
		Requires: "A finding severe enough to block halts the build; anything less is deferred and carried forward, and nothing judges which is which at build time.",
		Asserts:  "An open finding at or above the harness's threshold halts the build and names the finding; one below it does not; the threshold comes from the injected policy.",
		check:    findingsSplit,
	}}
}

// oneNextDirective checks the directive set is closed and singular.
func oneNextDirective(t *Tree) []string {
	var out []string
	closed := []engine.Action{
		engine.ActionLaunch, engine.ActionGate, engine.ActionPause,
		engine.ActionHalt, engine.ActionStop,
	}
	seen := map[engine.Action]bool{}
	for name, st := range fixtureStates() {
		d, err := stepOf(t, st)
		if err != nil {
			out = append(out, note("%s: %v", name, err))
			continue
		}
		if !slices.Contains(closed, d.Action) {
			out = append(out, note("%s: yielded action %q, which is outside the closed set", name, d.Action))
		}
		seen[d.Action] = true
		payloads := 0
		for _, carried := range []bool{d.Launch != nil, d.Gate != nil, d.Summary != nil} {
			if carried {
				payloads++
			}
		}
		if payloads > 1 {
			out = append(out, note("%s: one directive carries %d action payloads", name, payloads))
		}
	}
	for _, action := range closed {
		if !seen[action] {
			out = append(out, note("no fixture state reaches the %q action, so it is unproven", action))
		}
	}
	slices.Sort(out)
	return out
}

// driverComposesNoVersionControl checks the driver only ever runs this engine.
func driverComposesNoVersionControl(t *Tree) []string {
	var out []string
	for name, st := range fixtureStates() {
		d, err := stepOf(t, st)
		if err != nil {
			out = append(out, note("%s: %v", name, err))
			continue
		}
		for _, c := range d.Commands {
			if len(c.Argv) == 0 {
				out = append(out, note("%s: emitted a command with no program", name))
				continue
			}
			if c.Argv[0] != "anoikis" {
				out = append(out, note("%s: emitted %q, which is not an invocation of this engine", name, c.Argv[0]))
			}
			if c.Purpose == "" {
				out = append(out, note("%s: emitted %v with no stated purpose", name, c.Argv))
			}
		}
	}
	out = append(out, executesVersionControl(t, "internal/vcs")...)
	slices.Sort(out)
	return slices.Compact(out)
}

// admissionProvesDisjointness checks a batch is only ever provably safe.
func admissionProvesDisjointness(t *Tree) []string {
	var out []string
	h, prover, err := harnessOf(t)
	if err != nil {
		return []string{note("%v", err)}
	}

	disjoint := fixtureState([]dag.Node{
		fixtureNode("a", "svc/a", dag.StatusReady),
		fixtureNode("b", "svc/b", dag.StatusReady),
	}, dag.GatePending)
	if members, err := admitted(disjoint, h, prover); err != nil {
		out = append(out, note("%v", err))
	} else if len(members) != 2 {
		out = append(out, note("two nodes with disjoint claims were not admitted together: %v", members))
	}

	overlapping := fixtureState([]dag.Node{
		fixtureNode("a", "svc/shared", dag.StatusReady),
		fixtureNode("b", "svc/shared", dag.StatusReady),
	}, dag.GatePending)
	if members, err := admitted(overlapping, h, prover); err != nil {
		out = append(out, note("%v", err))
	} else if len(members) != 1 {
		out = append(out, note("two nodes claiming the same directory shared a batch: %v", members))
	}

	unclaimed := fixtureNode("a", "svc/a", dag.StatusReady)
	unclaimed.Surface = nil
	unproven := fixtureState([]dag.Node{unclaimed, fixtureNode("b", "svc/b", dag.StatusReady)}, dag.GatePending)
	if members, err := admitted(unproven, h, prover); err != nil {
		out = append(out, note("%v", err))
	} else if len(members) != 1 {
		out = append(out, note("a node claiming nothing was co-batched rather than run alone: %v", members))
	}

	d, err := stepOf(t, overlapping)
	if err != nil {
		out = append(out, note("%v", err))
	} else if d.Launch == nil || len(d.Launch.Deferred) == 0 {
		out = append(out, note("a node held back from a batch was not reported with the reason it was held back"))
	}
	slices.Sort(out)
	return out
}

// admitted returns the batch a state's ready nodes are admitted as.
func admitted(st dag.State, h *policy.Harness, prover *graph.Prover) ([]string, error) {
	g, err := st.Graph(ids.Opaque{})
	if err != nil {
		return nil, err
	}
	batch, err := admission.Admit(g, st.Ready(g), prover, h.MaxGroupSize())
	if err != nil {
		return nil, err
	}
	return batch.Members, nil
}

// backstopAlwaysOn checks the post-merge check cannot be skipped.
func backstopAlwaysOn(t *Tree) []string {
	var out []string
	if h, _, err := harnessOf(t); err != nil {
		out = append(out, note("%v", err))
	} else if len(h.Backstop.Command) == 0 {
		out = append(out, note("%s: declares no post-merge backstop command", examplePolicy))
	}
	stripped, err := policyWithout(t, "backstop")
	if err != nil {
		out = append(out, note("%v", err))
	} else if _, err := policy.Load(stripped); err == nil {
		out = append(out, note("a harness policy declaring no backstop command was accepted"))
	}

	repo := &vcs.Repo{}
	if _, err := repo.Backstop(context.Background(), nil, 0); err == nil {
		out = append(out, note("the backstop ran with no command instead of refusing"))
	}

	declared := map[string][]dag.Claim{
		"a": {{Domain: "path", Kind: "dir", Value: "svc/a"}, {Domain: "path", Kind: "glob", Value: "docs/**/*.md"}},
	}
	drift := vcs.AssertSurfaces(
		[]string{"svc/a/main.go", "docs/guide/intro.md", "svc/b/other.go"},
		declared, []string{"path"})
	if !slices.Equal(drift, []string{"svc/b/other.go"}) {
		out = append(out, note("the surface re-assertion reported %v, want only the path no node declared", drift))
	}
	slices.Sort(out)
	return out
}

// policyWithout writes the shipped harness policy to a temporary file with one
// top-level member removed, so a refusal can be observed on real policy bytes
// rather than a hand-built stub.
func policyWithout(t *Tree, member string) (string, error) {
	var doc map[string]any
	if err := t.JSON(examplePolicy, &doc); err != nil {
		return "", err
	}
	delete(doc, member)
	raw, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("render the stripped policy: %w", err)
	}
	dir, err := os.MkdirTemp("", "acceptance-policy")
	if err != nil {
		return "", fmt.Errorf("create a temporary directory: %w", err)
	}
	file := filepath.Join(dir, "harness-policy.json")
	if err := os.WriteFile(file, raw, 0o600); err != nil {
		return "", fmt.Errorf("write the stripped policy: %w", err)
	}
	return file, nil
}

// twoMergesKeptApart checks only the main-branch merge signs and pauses.
func twoMergesKeptApart(t *Tree) []string {
	var out []string
	main, err := stepOf(t, fixtureStateTargeting(
		[]dag.Node{fixtureNode("a", "svc/a", dag.StatusDone)}, dag.GatePassed, "main"))
	if err != nil {
		return []string{note("%v", err)}
	}
	if main.Gate == nil || !main.Gate.TargetsMain {
		out = append(out, note("a gate merging onto the harness's main branch is not marked as the one merge that signs"))
	} else if !slices.ContainsFunc(main.Commands, func(c engine.Command) bool {
		return slices.Contains(c.Argv, "--confirm")
	}) {
		out = append(out, note("the main-branch merge is emitted without asking for an operator-approved message"))
	}

	other, err := stepOf(t, fixtureStateTargeting(
		[]dag.Node{fixtureNode("a", "svc/a", dag.StatusDone)}, dag.GatePassed, "integration"))
	if err != nil {
		return append(out, note("%v", err))
	}
	if other.Gate == nil || other.Gate.TargetsMain {
		out = append(out, note("a gate merging onto a branch other than main is marked as signing"))
	} else if slices.ContainsFunc(other.Commands, func(c engine.Command) bool {
		return slices.Contains(c.Argv, "--confirm")
	}) {
		out = append(out, note("a merge below the main branch asks for an operator confirmation it does not need"))
	}

	// A zero repository is deliberate: the assertion is that both refusals
	// happen before any repository work, so there is nothing for them to touch.
	repo := &vcs.Repo{}
	if _, err := repo.MergeGate(context.Background(), vcs.GatePlan{
		BuildBranch: "build", Target: "main", TargetsMain: true, ResignBase: "abc",
	}); err == nil {
		out = append(out, note("a main-branch merge with no operator-approved message was accepted"))
	}
	if _, err := repo.MergeGate(context.Background(), vcs.GatePlan{
		BuildBranch: "build", Target: "main", TargetsMain: true, Message: "approved",
	}); err == nil {
		out = append(out, note("a main-branch merge with no explicit re-signing range was accepted"))
	}
	slices.Sort(out)
	return out
}

// doneMeansMerged checks a node settles at the merge and not before it.
func doneMeansMerged(t *Tree) []string {
	var out []string
	h, _, err := harnessOf(t)
	if err != nil {
		return []string{note("%v", err)}
	}
	st := fixtureState([]dag.Node{
		fixtureNode("a", "svc/a", dag.StatusRunning),
		fixtureNode("b", "svc/b", dag.StatusBlocked, "a"),
	}, dag.GatePending)

	rec, err := engine.Apply(st, h, []engine.Outcome{{
		Result: dag.RunResult{NodeID: "a", RunID: "r-a", Status: dag.RunPass},
		Usage:  dag.Usage{Known: true, CostUSD: 1},
	}}, 0, "2026-01-01T00:00:00Z")
	if err != nil {
		return append(out, note("recording a passing run: %v", err))
	}
	applied := st
	applied.Shards = rec.Shards
	if node, ok := applied.Node("a"); ok && node.Status.Settled() {
		out = append(out, note("a node was marked done by recording its run, before anything was merged"))
	}
	if !slices.Contains(rec.Mergeable, "a") {
		out = append(out, note("a passing run was not reported as mergeable: %v", rec.Mergeable))
	}
	g, err := applied.Graph(ids.Opaque{})
	if err != nil {
		return append(out, note("%v", err))
	}
	if slices.Contains(applied.Ready(g), "b") {
		out = append(out, note("a node whose dependency is unmerged was reported ready"))
	}

	shards, events := engine.Settle(rec.Shards, rec.Mergeable, rec.Runs, 0, "2026-01-01T00:00:01Z")
	settled := applied
	settled.Shards = shards
	node, ok := settled.Node("a")
	if !ok || node.Status != dag.StatusDone {
		out = append(out, note("settling the merge did not mark the node done: %+v", node))
	}
	if !slices.ContainsFunc(events, func(e dag.LogEvent) bool { return e.Event == dag.EventMerged && e.NodeID == "a" }) {
		out = append(out, note("the merge journalled no merged transition"))
	}
	slices.Sort(out)
	return out
}

// runLogAppendOnly checks the log's durability guarantees.
func runLogAppendOnly(t *Tree) []string {
	var out []string
	if effort.MaxLineBytes > 4096 || effort.MaxLineBytes <= 0 {
		out = append(out, note("the run-log line bound is %d bytes, outside the single atomic append", effort.MaxLineBytes))
	}
	store, layout, err := scratchStore()
	if err != nil {
		return append(out, note("%v", err))
	}

	first := logEvent("r-1", "a", dag.EventDispatched)
	if err := store.AppendEvent(first); err != nil {
		return append(out, note("append the first transition: %v", err))
	}
	if err := store.AppendEvent(logEvent("r-1", "a", dag.EventMerged)); err != nil {
		return append(out, note("append the second transition: %v", err))
	}
	scan, err := store.ScanRunLog(0)
	if err != nil {
		return append(out, note("read the run log: %v", err))
	}
	if len(scan.Events) != 2 || scan.Events[0].Event != dag.EventDispatched {
		out = append(out, note("the log holds %d events and does not read back as one line per transition", len(scan.Events)))
	}

	oversize := logEvent("r-2", "b", dag.EventComplete)
	oversize.Detail = strings.Repeat("x", effort.MaxLineBytes)
	if err := store.AppendEvent(oversize); err == nil {
		out = append(out, note("an event too large for one atomic append was written instead of refused"))
	}

	damaged := "{ this line was cut short by a kill"
	if err := appendRaw(layout.RunLog(), damaged); err != nil {
		return append(out, note("%v", err))
	}
	scan, err = store.ScanRunLog(0)
	if err != nil {
		out = append(out, note("a damaged final line failed the read instead of being carried as damage: %v", err))
	} else if scan.Damaged != 1 || len(scan.Events) != 2 {
		out = append(out, note("a damaged final line was read as %d events with %d damage, want 2 and 1", len(scan.Events), scan.Damaged))
	}
	slices.Sort(out)
	return out
}

// resumeReadsTheTail checks a resume reads from the cursor onward.
func resumeReadsTheTail(t *Tree) []string {
	var out []string
	store, _, err := scratchStore()
	if err != nil {
		return []string{note("%v", err)}
	}
	for _, id := range []string{"r-1", "r-2"} {
		if err := store.AppendEvent(logEvent(id, "a", dag.EventDispatched)); err != nil {
			return append(out, note("append %s: %v", id, err))
		}
	}
	sealed, err := store.ScanRunLog(0)
	if err != nil {
		return append(out, note("%v", err))
	}
	if err := store.SaveCursor(effort.Cursor{Offset: sealed.Offset, NextLayer: 2}); err != nil {
		return append(out, note("save the cursor: %v", err))
	}
	if err := store.AppendEvent(logEvent("r-3", "b", dag.EventDispatched)); err != nil {
		return append(out, note("append after the cursor: %v", err))
	}
	cursor, err := store.LoadCursor()
	if err != nil {
		return append(out, note("read the cursor back: %v", err))
	}
	if cursor.Offset != sealed.Offset || cursor.NextLayer != 2 {
		out = append(out, note("the cursor read back as %+v, not what was saved", cursor))
	}
	tail, err := store.ScanRunLog(cursor.Offset)
	if err != nil {
		return append(out, note("%v", err))
	}
	if len(tail.Events) != 1 || tail.Events[0].RunID != "r-3" {
		out = append(out, note("a scan from the cursor returned %d events, want only the one appended after it", len(tail.Events)))
	}
	slices.Sort(out)
	return out
}

// archivedNodeLeavesATombstone checks a closed node stays resolvable.
func archivedNodeLeavesATombstone(t *Tree) []string {
	var out []string
	store, layout, err := scratchStore()
	if err != nil {
		return []string{note("%v", err)}
	}
	detail := dag.Detail{
		SchemaVersion: dag.SchemaVersion, ID: "a", Intent: "do the work",
		DeliverableKind: dag.KindCode, Acceptance: []string{"it works"},
		Stages: []dag.Stage{{Stage: "build", Role: "builder", Model: "claude-sonnet-5"}},
	}
	if err := store.SaveDetail(detail); err != nil {
		return append(out, note("write the node detail: %v", err))
	}
	ref, err := store.ArchiveNode("a")
	if err != nil {
		return append(out, note("archive the node: %v", err))
	}
	if !strings.HasPrefix(ref, "archive/") {
		out = append(out, note("an archived detail record is referenced as %q, not from the archive", ref))
	}
	if _, err := os.Stat(filepath.Join(layout.NodeDir(), "a.json")); err == nil {
		out = append(out, note("the hot-path detail record survived archival"))
	}
	if _, err := store.LoadDetail("a"); err != nil {
		out = append(out, note("an archived node is no longer readable: %v", err))
	}

	shards := []dag.Shard{{SchemaVersion: dag.SchemaVersion, GateID: "g1", Nodes: []dag.Node{
		fixtureNode("a", "svc/a", dag.StatusDone),
		fixtureNode("b", "svc/b", dag.StatusReady, "a"),
	}}}
	retired := engine.Retire(shards, []engine.Closure{{NodeID: "a", DetailRef: ref, Tombstone: dag.Tombstone{Summary: "did the work"}}})
	st := fixtureState(retired[0].Nodes, dag.GatePending)
	node, ok := st.Node("a")
	if !ok || node.Status != dag.StatusArchived || node.Tombstone == nil {
		out = append(out, note("a retired node left no tombstone in its shard: %+v", node))
	}
	g, err := st.Graph(ids.Opaque{})
	if err != nil {
		return append(out, note("%v", err))
	}
	if !slices.Contains(st.Ready(g), "b") {
		out = append(out, note("a live node depending on an archived one stalled instead of resolving"))
	}
	slices.Sort(out)
	return out
}

// oneMechanicalGraphMutation checks the only build-time graph change is the
// fix node a review calls for.
func oneMechanicalGraphMutation(t *Tree) []string {
	var out []string
	h, _, err := harnessOf(t)
	if err != nil {
		return []string{note("%v", err)}
	}
	reviewed := []string{"a", "b"}
	st := fixtureState([]dag.Node{
		fixtureNode("a", "svc/a", dag.StatusDone),
		fixtureNode("b", "svc/b", dag.StatusDone),
	}, dag.GatePending)
	details := map[string]dag.Detail{
		"a": {ID: "a", DeliverableKind: dag.KindCode},
		"b": {ID: "b", DeliverableKind: dag.KindCode},
	}
	graft, err := engine.PlanGraft(st, h, ids.Opaque{}, details, reviewed, "findings.json#f-1", "2026-01-01T00:00:00Z")
	if err != nil {
		return append(out, note("plan the fix node: %v", err))
	}
	if !slices.Equal(graft.Node.Deps, reviewed) {
		out = append(out, note("the fix node depends on %v, not on exactly the nodes reviewed", graft.Node.Deps))
	}
	claimed := map[string]bool{}
	for _, c := range graft.Node.Surface {
		claimed[c.Value] = true
	}
	if len(graft.Node.Surface) != 2 || !claimed["svc/a"] || !claimed["svc/b"] {
		out = append(out, note("the fix node claims %+v, not the union of the reviewed nodes' surfaces", graft.Node.Surface))
	}
	if graft.Detail.Inputs == nil || !slices.Contains(graft.Detail.Inputs.SpecRefs, "findings.json#f-1") {
		out = append(out, note("the fix node is not seeded by the review's findings"))
	}
	if graft.GateID != "g1" {
		out = append(out, note("the fix node landed in gate %q, not the one the reviewed nodes belong to", graft.GateID))
	}
	if graft.Event.Event != dag.EventGrafted {
		out = append(out, note("the graft journalled %q rather than a grafted transition", graft.Event.Event))
	}
	slices.Sort(out)
	return out
}

// oneHomePerFact checks each fact has exactly one contractual home.
func oneHomePerFact(t *Tree) []string {
	var out []string
	gates := t.Text("schemas/anoikis/gates.schema.json")
	for _, forbidden := range []string{`"units"`, `"members"`, `"nodes"`} {
		if strings.Contains(gates, forbidden) {
			out = append(out, note("gates.schema.json: carries %s; membership belongs to the shard a node lives in", forbidden))
		}
	}
	if !strings.Contains(t.Text("schemas/anoikis/graph-shard.schema.json"), `"gate_id"`) {
		out = append(out, note("graph-shard.schema.json: carries no gate identity, so membership has no home"))
	}
	if !strings.Contains(t.Text("schemas/anoikis/project.schema.json"), `"signing"`) {
		out = append(out, note("project.schema.json: carries no signing policy"))
	}
	if strings.Contains(gates, `"signing"`) {
		out = append(out, note("gates.schema.json: carries a signing policy of its own, which the manifest already owns"))
	}
	if !strings.Contains(gates, `"sign"`) {
		out = append(out, note("gates.schema.json: a gate cannot say whether it inherits the signing policy"))
	}
	slices.Sort(out)
	return out
}

// spendIsPriced checks spend comes from a provider and unknown stays unknown.
func spendIsPriced(t *Tree) []string {
	var out []string
	result := t.Text("schemas/anoikis/run-result.schema.json")
	for _, forbidden := range []string{"cost_usd", "input_tokens", "output_tokens", "cache_read_tokens", "cache_creation_tokens"} {
		if strings.Contains(result, forbidden) {
			out = append(out, note("run-result.schema.json: carries %s, which a run cannot measure about itself", forbidden))
		}
	}
	if !strings.Contains(result, "attribution") {
		out = append(out, note("run-result.schema.json: carries no attribution, so a provider cannot find the run's turns"))
	}
	event := t.Text("schemas/anoikis/run-log-event.schema.json")
	for _, class := range []string{"input_tokens", "cache_creation_tokens", "cache_read_tokens", "output_tokens"} {
		if !strings.Contains(event, class) {
			out = append(out, note("run-log-event.schema.json: does not carry %s", class))
		}
	}

	unknown, err := usage.Unavailable{}.RunUsage(context.Background(), usage.Run{Project: "p"})
	if err != nil {
		out = append(out, note("a provider with no source returned an error rather than an unknown: %v", err))
	}
	if unknown.Known || unknown.Reason == "" {
		out = append(out, note("a provider with no source answered %+v rather than unknown with a reason", unknown))
	}
	folded := dag.Usage{Known: true, CostUSD: 5}.Fold(unknown)
	if folded.Known || folded.Reason == "" {
		out = append(out, note("folding an unpriced run into a total left the total looking measured: %+v", folded))
	}
	floor := dag.Budget{SpentUSD: 5, UnpricedRuns: 1}.Spend()
	if floor.Known {
		out = append(out, note("an effort holding an unpriced run reported its spend as a total rather than a floor"))
	}
	slices.Sort(out)
	return out
}

// undispatchableNodesRefused checks such a node is never handed out.
func undispatchableNodesRefused(t *Tree) []string {
	var out []string
	h, prover, err := harnessOf(t)
	if err != nil {
		return []string{note("%v", err)}
	}
	never := fixtureNode("a", "svc/a", dag.StatusReady)
	never.NeverDispatch = true
	st := fixtureState([]dag.Node{never, fixtureNode("b", "svc/b", dag.StatusReady)}, dag.GatePending)
	g, err := st.Graph(ids.Opaque{})
	if err != nil {
		return append(out, note("%v", err))
	}
	ready := st.Ready(g)
	if slices.Contains(ready, "a") {
		out = append(out, note("a node marked as never dispatched was reported ready"))
	}
	batch, err := admission.Admit(g, ready, prover, h.MaxGroupSize())
	if err != nil {
		return append(out, note("%v", err))
	}
	if slices.Contains(batch.Members, "a") {
		out = append(out, note("a node marked as never dispatched was admitted to a batch"))
	}
	slices.Sort(out)
	return out
}

// reviewSelectedByTier checks review is a tier, never a stage.
func reviewSelectedByTier(t *Tree) []string {
	var out []string
	node := t.Text("schemas/anoikis/graph-shard.schema.json")
	for _, tier := range []string{string(dag.VerifyCheap), string(dag.VerifyGate), string(dag.VerifyImmediateDeep)} {
		if !strings.Contains(node, tier) {
			out = append(out, note("graph-shard.schema.json: the verification tier %q is not in the closed set", tier))
		}
	}
	h, _, err := harnessOf(t)
	if err != nil {
		return append(out, note("%v", err))
	}
	stages := slices.Clone(h.Workflow.Stages)
	for _, route := range h.Routes {
		stages = append(stages, route.Stages...)
	}
	for _, s := range stages {
		if strings.Contains(strings.ToLower(s.Stage), "review") {
			out = append(out, note("%s: review is declared as a stage; it is selected by the verification tier alone", examplePolicy))
		}
	}
	builder := ""
	for name, role := range h.Roles {
		if role.Builder {
			builder = name
			break
		}
	}
	if builder == "" {
		out = append(out, note("%s: declares no builder role, so a review role cannot be told apart from one", examplePolicy))
	} else {
		mutated := *h
		mutated.Gates.ReviewRole = builder
		if err := mutated.Validate(); err == nil {
			out = append(out, note("a harness naming a builder as its review role was accepted"))
		}
	}
	slices.Sort(out)
	return out
}

// haltCausesClosed checks a stop names a cause a driver can branch on.
func haltCausesClosed(t *Tree) []string {
	var out []string
	for name, st := range fixtureStates() {
		d, err := stepOf(t, st)
		if err != nil {
			out = append(out, note("%s: %v", name, err))
			continue
		}
		if d.Action != engine.ActionHalt && d.Action != engine.ActionPause {
			continue
		}
		if d.Cause == "" || d.Reason == "" {
			out = append(out, note("%s: stopped with cause %q and reason %q", name, d.Cause, d.Reason))
		}
	}
	// Each state below is the one condition its cause names, so a cause that
	// stopped being reachable — or started answering for the wrong condition —
	// shows up here rather than in a build.
	for name, want := range map[string]struct {
		cause engine.Cause
		state dag.State
	}{
		"a dependency cycle": {engine.CauseGraphCycle, cyclicState()},
		"the spend ceiling":  {engine.CauseBudget, overBudgetState()},
		"an unready plan":    {engine.CauseNotReady, draftState()},
		"a blockage":         {engine.CauseBlocked, blockedState()},
	} {
		d, err := stepOf(t, want.state)
		if err != nil {
			out = append(out, note("%s: %v", name, err))
			continue
		}
		if d.Cause != want.cause {
			out = append(out, note("%s yielded cause %q, want %q", name, d.Cause, want.cause))
		}
	}
	// A merge-time cause cannot be reached from a directive, so it is checked
	// where it is raised: a cause that is declared and never raised is a
	// vocabulary entry no driver will ever see.
	for _, name := range []string{"CauseSurfaceOverlap", "CauseBackstopFailed"} {
		if raises(t, "internal/engine", name) == 0 {
			out = append(out, note("the cause %s is declared and never raised", name))
		}
	}
	slices.Sort(out)
	return out
}

// raises counts the places in a package, outside its declaration, that use an
// identifier.
func raises(t *Tree, pkg, name string) int {
	uses := 0
	for _, rel := range t.GoSource(false) {
		if path.Dir(rel) != pkg {
			continue
		}
		uses += strings.Count(t.Text(rel), name)
	}
	return max(uses-1, 0)
}

// cyclicState is a graph that cannot be ordered at all.
func cyclicState() dag.State {
	return fixtureState([]dag.Node{
		fixtureNode("a", "svc/a", dag.StatusReady, "b"),
		fixtureNode("b", "svc/b", dag.StatusReady, "a"),
	}, dag.GatePending)
}

// overBudgetState is an effort that has spent past its ceiling.
func overBudgetState() dag.State {
	st := fixtureState([]dag.Node{fixtureNode("a", "svc/a", dag.StatusReady)}, dag.GatePending)
	st.Project.Budget = dag.Budget{CeilingUSD: 1, SpentUSD: 2, EnforcedAt: "layer"}
	return st
}

// draftState is an effort whose plan has not been marked ready.
func draftState() dag.State {
	st := fixtureState([]dag.Node{fixtureNode("a", "svc/a", dag.StatusReady)}, dag.GatePending)
	st.Project.Status = dag.ProjectDraft
	return st
}

// blockedState is outstanding work with nothing dispatchable: the only node
// left waits on one that failed with no attempt left, and that failure is
// already settled as a tombstoned closure rather than a live failure.
func blockedState() dag.State {
	waiting := fixtureNode("b", "svc/b", dag.StatusBlocked, "a")
	held := fixtureNode("a", "svc/a", dag.StatusReady)
	held.NeverDispatch = true
	return fixtureState([]dag.Node{held, waiting}, dag.GatePending)
}

// oneExclusionConstant checks every enumerator reads the one constant.
func oneExclusionConstant(t *Tree) []string {
	var out []string
	ignore := t.Text(".gitignore")
	if ignore == "" {
		return []string{note(".gitignore: is missing, so nothing enumerates the uncommitted classes")}
	}
	if len(effort.Ephemeral) == 0 {
		return []string{note("the exclusion constant names nothing")}
	}
	root, err := os.MkdirTemp("", "acceptance-effort")
	if err != nil {
		return []string{note("create a temporary directory: %v", err)}
	}
	layout, err := effort.Create(root, "e")
	if err != nil {
		return []string{note("create an effort: %v", err)}
	}
	for _, name := range effort.Ephemeral {
		pattern := effort.DirName + "/*/" + name + "/"
		if !strings.Contains(ignore, pattern) {
			out = append(out, note(".gitignore: does not ignore %q, so the enumerators have drifted", pattern))
		}
		if _, err := os.Stat(filepath.Join(layout.Dir(), name)); err != nil {
			out = append(out, note("creating an effort did not create its %s directory", name))
		}
	}
	for _, committed := range []string{layout.ResultDir(), layout.PromptDir(), layout.ArchiveDir(), layout.NodeDir()} {
		for _, name := range effort.Ephemeral {
			if strings.Contains(committed, string(filepath.Separator)+name+string(filepath.Separator)) {
				out = append(out, note("%s is a committed artifact path inside the ephemeral directory %s", committed, name))
			}
		}
	}
	slices.Sort(out)
	return out
}

// resultsAreDurable checks the durable record carries what the log cannot.
func resultsAreDurable(t *Tree) []string {
	var out []string
	result := t.Text("schemas/anoikis/run-result.schema.json")
	for _, field := range []string{"diagnostics", "overflow_count", "excerpt"} {
		if !strings.Contains(result, field) {
			out = append(out, note("run-result.schema.json: carries no %s, so a failure is not durable", field))
		}
	}
	layout := effort.Layout{Root: "/root", Slug: "e"}
	for _, p := range []string{layout.ResultDir(), layout.PromptDir(), layout.ArchiveDir()} {
		if strings.Contains(p, "errors") {
			out = append(out, note("%s: a second store for errors duplicates the durable result", p))
		}
	}
	if !slices.Contains(effort.Ephemeral, path.Base(layout.LogDir())) {
		out = append(out, note("the raw log directory is not ephemeral, so it competes with the durable result"))
	}
	if slices.Contains(effort.Ephemeral, path.Base(layout.ResultDir())) {
		out = append(out, note("the durable result directory is ephemeral, so a failure dies with the worktree"))
	}
	slices.Sort(out)
	return out
}

// surfaceClaimsTyped checks a claim is typed and read the same way twice.
func surfaceClaimsTyped(t *Tree) []string {
	var out []string
	// A claim's kind vocabulary belongs to the domain the harness registers,
	// not to the artifact contract, so the contract is checked by what it
	// refuses of a claim and the harness by the domain it registers.
	typed := map[string]any{"domain": "path", "kind": "dir", "value": "svc/a"}
	if diags, err := validateAgainstTree(t, shardContract, shardWith(typed)); err != nil {
		out = append(out, note("%v", err))
	} else if len(diags) > 0 {
		out = append(out, note("a fully typed claim was refused: %v", diags[0].Message))
	}
	// A claim is nothing without the domain that decides how it is matched and
	// the value being claimed; the kind is optional in the contract because a
	// domain may decide it, and a claim that leaves it unset simply covers
	// nothing — which the re-assertion below proves.
	for _, member := range []string{"domain", "value"} {
		partial := map[string]any{}
		for k, v := range typed {
			if k != member {
				partial[k] = v
			}
		}
		diags, err := validateAgainstTree(t, shardContract, shardWith(partial))
		if err != nil {
			out = append(out, note("%v", err))
			continue
		}
		if len(diags) == 0 {
			out = append(out, note("a claim declaring no %s was accepted", member))
		}
	}
	undeclared := map[string]any{"domain": "path", "kind": "dir", "value": "svc/a", "scope": "everything"}
	if diags, err := validateAgainstTree(t, shardContract, shardWith(undeclared)); err != nil {
		out = append(out, note("%v", err))
	} else if len(diags) == 0 {
		out = append(out, note("a claim carrying an undeclared member was accepted, so a claim's shape is not closed"))
	}
	if h, _, err := harnessOf(t); err != nil {
		out = append(out, note("%v", err))
	} else {
		if len(h.Surfaces) == 0 {
			out = append(out, note("%s: registers no resource domain, so no claim can be proven disjoint", examplePolicy))
		}
		for _, d := range h.Surfaces {
			if d.Kind == "" {
				out = append(out, note("%s: domain %q declares no kind, so nothing decides how its claims are matched", examplePolicy, d.Name))
			}
		}
	}
	declared := map[string][]dag.Claim{"a": {
		{Domain: "path", Kind: "dir", Value: "svc/a"},
		{Domain: "path", Kind: "file", Value: "go.mod"},
		{Domain: "path", Kind: "glob", Value: "docs/**/*.md"},
		{Domain: "path", Value: "svc/untyped"},
	}}
	covered := []string{"svc/a/main.go", "go.mod", "docs/deep/guide.md"}
	if drift := vcs.AssertSurfaces(covered, declared, []string{"path"}); len(drift) != 0 {
		out = append(out, note("paths covered by a declared claim were reported as undeclared: %v", drift))
	}
	uncovered := []string{"svc/untyped/file.go", "svc/b/other.go"}
	drift := vcs.AssertSurfaces(uncovered, declared, []string{"path"})
	if !slices.Equal(drift, uncovered) {
		out = append(out, note("an untyped claim covered a path, or an undeclared path went unreported: %v", drift))
	}
	slices.Sort(out)
	return out
}

// findingsSplit checks a blocking finding stops the build and a deferred one
// does not.
func findingsSplit(t *Tree) []string {
	var out []string
	h, _, err := harnessOf(t)
	if err != nil {
		return []string{note("%v", err)}
	}
	st := fixtureState([]dag.Node{fixtureNode("a", "svc/a", dag.StatusReady)}, dag.GatePending)
	blocking := engine.Finding{ID: "f-1", Statement: "the merge loses work", Criticality: h.BlockingThreshold()}
	d, err := stepOf(t, st, blocking)
	if err != nil {
		return append(out, note("%v", err))
	}
	if d.Action != engine.ActionHalt || d.Cause != engine.CauseBlockingFinding {
		out = append(out, note("an open finding at the threshold yielded %q/%q rather than halting", d.Action, d.Cause))
	}
	if !slices.Contains(d.Subjects, "f-1") {
		out = append(out, note("the halt did not name the finding that caused it: %v", d.Subjects))
	}
	deferred := engine.Finding{ID: "f-2", Statement: "the wording could be clearer", Criticality: h.BlockingThreshold() - 1}
	d, err = stepOf(t, st, deferred)
	if err != nil {
		return append(out, note("%v", err))
	}
	if d.Action == engine.ActionHalt {
		out = append(out, note("a finding below the threshold halted the build: %s", d.Reason))
	}
	slices.Sort(out)
	return out
}

// shardContract is the graph shard's contract file.
const shardContract = "schemas/anoikis/graph-shard.schema.json"

// shardWith renders the smallest shard document carrying one claim, so a
// contract can be probed with exactly the claim under test.
func shardWith(claim map[string]any) any {
	return map[string]any{
		"schema_version": dag.SchemaVersion,
		"gate_id":        "g1",
		"nodes": []any{map[string]any{
			"id": "a", "title": "a", "status": string(dag.StatusReady),
			"surface":     []any{claim},
			"verify_tier": string(dag.VerifyCheap),
			"detail_ref":  "nodes/a.json",
		}},
	}
}

// logEvent is one well-formed transition for a scratch run log.
func logEvent(runID, nodeID string, e dag.Event) dag.LogEvent {
	return dag.LogEvent{
		SchemaVersion: dag.SchemaVersion, TS: "2026-01-01T00:00:00Z",
		RunID: runID, NodeID: nodeID, Event: e,
	}
}

// scratchStore opens a store over a temporary effort, so a durability clause
// exercises the real read and write path rather than a stub.
func scratchStore() (*effort.Store, effort.Layout, error) {
	root, err := os.MkdirTemp("", "acceptance-store")
	if err != nil {
		return nil, effort.Layout{}, fmt.Errorf("create a temporary directory: %w", err)
	}
	layout, err := effort.Create(root, "e")
	if err != nil {
		return nil, effort.Layout{}, fmt.Errorf("create an effort: %w", err)
	}
	return effort.New(layout, nil), layout, nil
}

// appendRaw writes a line to a file exactly as a killed process would leave
// it: no terminator, nothing rewritten.
func appendRaw(path, line string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(line); err != nil {
		return fmt.Errorf("append to %s: %w", path, err)
	}
	return nil
}
