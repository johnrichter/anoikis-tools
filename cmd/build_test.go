package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/johnrichter/anoikis-tools/internal/dag"
	"github.com/johnrichter/anoikis-tools/internal/effort"
	"github.com/johnrichter/claude-shared-tooling/go/clikit"
)

// examplePolicy is the harness policy every fixture below loads, so a record
// or step test runs against the same declared vocabulary the real CLI does.
const examplePolicy = "../examples/harness-policy.json"

// fixtureBaseRef stands in for a build-branch head. Passing it explicitly
// keeps these tests from needing a real git repository: the code paths under
// test never reach a merge, so no commit ever needs to exist behind it.
const fixtureBaseRef = "0000000000000000000000000000000000000000"

// dogfoodEvidenceDir holds byte-for-byte copies of the run log and result
// that reproduced this task's corruption in a real dogfood run — see its
// README. Reading them here, rather than fabricating equivalent JSON, ties
// the reproduction below to the actual bytes the corruption happened against.
const dogfoodEvidenceDir = "testdata/dogfood-evidence"

// dogfoodDispatch parses the dispatched event that opened the run log line
// the corrupting failure followed, straight out of the evidence copy.
func dogfoodDispatch(t *testing.T) dag.LogEvent {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dogfoodEvidenceDir, "run-log-head.jsonl"))
	if err != nil {
		t.Fatalf("read dogfood run-log-head.jsonl: %v", err)
	}
	line, _, _ := bytes.Cut(raw, []byte("\n"))
	var e dag.LogEvent
	if err := json.Unmarshal(line, &e); err != nil {
		t.Fatalf("parse dogfood dispatched event: %v", err)
	}
	e.SchemaVersion = dag.SchemaVersion
	return e
}

// dogfoodFailureExcerpt returns the real test-stage failure diagnostic the
// evidence bundle recorded for the run this fixture replays, truncated to
// the run-result schema's excerpt limit.
func dogfoodFailureExcerpt(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dogfoodEvidenceDir, "cli-core-test-result.json"))
	if err != nil {
		t.Fatalf("read dogfood cli-core-test-result.json: %v", err)
	}
	const excerptMax = 8000
	if len(raw) > excerptMax {
		raw = raw[:excerptMax]
	}
	return string(raw)
}

// recordFixture lays out a minimal effort on disk: one gate, one node
// dispatched into layer 0 and still running, and the harness policy at its
// default location. Node, gate, and run identity, and the dispatched event
// already on the log, are the ones the dogfood evidence names, so this is
// the smallest state that reaches the exact batch this task's defects
// corrupted: one dispatched run whose only outcome fails.
func recordFixture(t *testing.T) (repo string, store *effort.Store, layout effort.Layout, nodeID, runID string) {
	t.Helper()
	dispatch := dogfoodDispatch(t)
	repo = t.TempDir()
	var err error
	layout, err = effort.Create(repo, "e")
	if err != nil {
		t.Fatalf("create effort: %v", err)
	}
	store = effort.New(layout, nil)

	if err := store.SaveProject(dag.Project{
		ID: "e", Name: "e", Version: "1.0.0", Status: dag.ProjectBuilding,
		Budget:  dag.Budget{CeilingUSD: 100, EnforcedAt: "layer"},
		Signing: dag.Signing{BelowMain: "never", MainMerge: "resign-all+sign-merge-commit"},
		Refs: dag.Refs{
			Graph: "graph.json", RunLog: "run-log.jsonl", Gates: "gates.json", Findings: "findings.json",
			Nodes: "nodes", Results: "results", Prompts: "prompts", Archive: "archive", Cursor: "resume-cursor.json",
		},
	}); err != nil {
		t.Fatalf("save project: %v", err)
	}
	if err := store.SaveGates(dag.Gates{Gates: []dag.Gate{{
		ID: "cli-and-docs", Name: "CLI and docs", Status: dag.GatePending,
		Policy: dag.GatePolicy{Pause: true, DeepReview: "none", MergeTarget: "none", Sign: "inherit"},
	}}}); err != nil {
		t.Fatalf("save gates: %v", err)
	}
	node := dag.Node{
		ID: dispatch.NodeID, Title: dispatch.NodeID, Status: dag.StatusRunning,
		Surface:    []dag.Claim{{Domain: "path", Kind: "dir", Value: "svc/a"}},
		VerifyTier: dag.VerifyCheap, DetailRef: "nodes/" + dispatch.NodeID + ".json", MaxAttempts: 1,
	}
	if err := store.SaveShards([]dag.Shard{{GateID: "cli-and-docs", Nodes: []dag.Node{node}}}, "2026-01-01T00:00:00Z"); err != nil {
		t.Fatalf("save shards: %v", err)
	}
	if err := store.AppendEvent(dispatch); err != nil {
		t.Fatalf("append dispatched: %v", err)
	}

	policy, err := os.ReadFile(examplePolicy)
	if err != nil {
		t.Fatalf("read example policy: %v", err)
	}
	if err := os.WriteFile(filepath.Join(layout.Dir(), "harness-policy.json"), policy, 0o644); err != nil {
		t.Fatalf("write harness policy: %v", err)
	}

	return repo, store, layout, dispatch.NodeID, dispatch.RunID
}

// dogfoodResultsFile writes a results document reporting the fixture's node
// failed, carrying the real dogfood diagnostic as its excerpt — the same
// content the evidence bundle recorded for this run's actual failure.
func dogfoodResultsFile(t *testing.T, nodeID, runID string) string {
	t.Helper()
	result := dag.RunResult{
		SchemaVersion: dag.SchemaVersion, NodeID: nodeID, RunID: runID,
		Status: dag.RunFail, Excerpt: dogfoodFailureExcerpt(t),
	}
	doc := map[string][]dag.RunResult{"results": {result}}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal dogfood results: %v", err)
	}
	path := filepath.Join(t.TempDir(), "results.json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write dogfood results file: %v", err)
	}
	return path
}

// runCLI executes the command tree with args and returns the decoded record.
// The run's own error (an exit code the record already carries) is not
// itself a test failure; callers check the record.
func runCLI(t *testing.T, args ...string) clikit.Result {
	t.Helper()
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	_ = root.Execute()

	var result clikit.Result
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("decode CLI output %q: %v", out.String(), err)
	}
	return result
}

// TestRecordFailureOnlyBatchPersistsCursor pins the cursor half of the
// defect cluster: a record call whose batch has nothing mergeable must seal
// the resume cursor exactly as the merge path does, since the shards and
// events it just wrote are folded either way.
func TestRecordFailureOnlyBatchPersistsCursor(t *testing.T) {
	repo, store, layout, nodeID, runID := recordFixture(t)
	results := dogfoodResultsFile(t, nodeID, runID)

	result := runCLI(t, "record", "--repo", repo, "--effort", "e", "--results", results)
	if result.Status != clikit.StatusSuccess {
		t.Fatalf("record status = %s, want %s (errors: %v)", result.Status, clikit.StatusSuccess, result.Errors)
	}

	if _, err := os.Stat(layout.Cursor()); err != nil {
		t.Fatalf("resume-cursor.json was not written after a failure-only record: %v", err)
	}
	cursor, err := store.LoadCursor()
	if err != nil {
		t.Fatalf("load cursor: %v", err)
	}
	if cursor.Offset == 0 {
		t.Errorf("cursor offset = 0, want it sealed past the dispatched-and-failed events this record just folded")
	}

	sh, err := store.LoadShard("cli-and-docs")
	if err != nil {
		t.Fatalf("load shard: %v", err)
	}
	if sh.Nodes[0].Status != dag.StatusFailed {
		t.Errorf("node status on disk = %s, want %s", sh.Nodes[0].Status, dag.StatusFailed)
	}
}

// TestRecordReplayAfterFailureStaysFailed reproduces the exact on-disk
// corruption from the dogfood run, seeded from its preserved evidence: a
// second, idempotent record call over the same failed outcome must leave the
// shard and graph index reading failed, not running.
func TestRecordReplayAfterFailureStaysFailed(t *testing.T) {
	repo, store, _, nodeID, runID := recordFixture(t)
	results := dogfoodResultsFile(t, nodeID, runID)

	for i := 0; i < 2; i++ {
		result := runCLI(t, "record", "--repo", repo, "--effort", "e", "--results", results)
		if result.Status != clikit.StatusSuccess {
			t.Fatalf("record #%d status = %s, want %s (errors: %v)", i+1, result.Status, clikit.StatusSuccess, result.Errors)
		}
	}

	sh, err := store.LoadShard("cli-and-docs")
	if err != nil {
		t.Fatalf("load shard: %v", err)
	}
	if got := sh.Nodes[0].Status; got != dag.StatusFailed {
		t.Errorf("node status after replaying a failed record = %s, want %s", got, dag.StatusFailed)
	}

	index, err := store.LoadIndex()
	if err != nil {
		t.Fatalf("load index: %v", err)
	}
	counts := index.Shards[0].Counts
	if counts.Failed != 1 || counts.Running != 0 {
		t.Errorf("graph index counts = %+v, want one failed and zero running", counts)
	}
}

// TestStepAfterFailureOnlyRecordHaltsOnFailedNode reproduces the dogfood
// sequence end to end: record a failure, replay that same record (the
// engine's own documented recovery), then step. The node has no attempt
// left, so step must halt for an operator replan, not pause on runs it
// believes are still in flight.
func TestStepAfterFailureOnlyRecordHaltsOnFailedNode(t *testing.T) {
	repo, _, _, nodeID, runID := recordFixture(t)
	results := dogfoodResultsFile(t, nodeID, runID)

	for i := 0; i < 2; i++ {
		result := runCLI(t, "record", "--repo", repo, "--effort", "e", "--results", results)
		if result.Status != clikit.StatusSuccess {
			t.Fatalf("record #%d status = %s, want %s (errors: %v)", i+1, result.Status, clikit.StatusSuccess, result.Errors)
		}
	}

	step := runCLI(t, "step", "--repo", repo, "--effort", "e", "--base-ref", fixtureBaseRef)
	if action, _ := step.Data["action"].(string); action != "halt" {
		t.Fatalf("step action = %q, want halt (errors: %v)", action, step.Errors)
	}
	if len(step.Errors) == 0 || !strings.Contains(step.Errors[0].Code, "failed_node") {
		t.Errorf("step halt code = %v, want it naming failed_node", step.Errors)
	}
}
