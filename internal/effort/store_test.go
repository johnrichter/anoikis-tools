package effort_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"

	"github.com/johnrichter/anoikis-tools/internal/dag"
	"github.com/johnrichter/anoikis-tools/internal/effort"
	"github.com/johnrichter/claude-shared-tooling/go/docmirror"
)

func project() dag.Project {
	return dag.Project{
		ID: "effort", Name: "Effort", Version: "1.0.0", Status: "ready",
		Budget:  dag.Budget{CeilingUSD: 50, EnforcedAt: "layer"},
		Signing: dag.Signing{BelowMain: "never", MainMerge: "resign-all+sign-merge-commit"},
		Refs: dag.Refs{
			Graph: "graph.json", RunLog: "run-log.jsonl", Gates: "gates.json", Findings: "findings.json",
			Nodes: "nodes", Results: "results", Prompts: "prompts", Archive: "archive", Cursor: "resume-cursor.json",
		},
	}
}

func shard(nodes ...dag.Node) dag.Shard {
	return dag.Shard{GateID: "g1", Nodes: nodes}
}

func testNode(id string) dag.Node {
	return dag.Node{
		ID: id, Title: id, Status: dag.StatusReady,
		Surface:    []dag.Claim{{Domain: "path", Kind: "dir", Value: "svc/" + id}},
		VerifyTier: dag.VerifyCheap, DetailRef: "nodes/" + id + ".json",
	}
}

func TestStateRoundTrip(t *testing.T) {
	s := store(t)
	if err := s.SaveProject(project()); err != nil {
		t.Fatalf("save project: %v", err)
	}
	if err := s.SaveGates(dag.Gates{Gates: []dag.Gate{{
		ID: "g1", Name: "Gate one", Status: dag.GatePending,
		Policy: dag.GatePolicy{Pause: true, DeepReview: "batched", MergeTarget: "main", Sign: "inherit"},
	}}}); err != nil {
		t.Fatalf("save gates: %v", err)
	}
	if err := s.SaveShards([]dag.Shard{shard(testNode("a"), testNode("b"))}, "2026-01-01T00:00:00Z"); err != nil {
		t.Fatalf("save shards: %v", err)
	}

	st, err := s.LoadState()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if len(st.Nodes()) != 2 {
		t.Fatalf("loaded %d nodes, want 2", len(st.Nodes()))
	}
	if got := st.Index.Shards[0].Counts.Ready; got != 2 {
		t.Errorf("index counted %d ready nodes, want 2", got)
	}
	if gate, ok := st.GateOf("a"); !ok || gate != "g1" {
		t.Errorf("gate membership resolved to %q, %v; want g1", gate, ok)
	}
}

func TestLoadingFoldsAJournalledTransitionTheShardMissed(t *testing.T) {
	s := store(t)
	if err := s.SaveProject(project()); err != nil {
		t.Fatalf("save project: %v", err)
	}
	if err := s.SaveGates(dag.Gates{Gates: []dag.Gate{{
		ID: "g1", Name: "Gate one", Status: dag.GatePending,
		Policy: dag.GatePolicy{Pause: true, DeepReview: "batched", MergeTarget: "main", Sign: "inherit"},
	}}}); err != nil {
		t.Fatalf("save gates: %v", err)
	}
	if err := s.SaveShards([]dag.Shard{shard(testNode("a"))}, "2026-01-01T00:00:00Z"); err != nil {
		t.Fatalf("save shards: %v", err)
	}
	// The transition is journalled and the process dies before the shard is
	// rewritten — the one window in which the two records disagree.
	if err := s.AppendEvent(dag.LogEvent{
		TS: "2026-01-01T00:00:01Z", RunID: "a-l0-a0", NodeID: "a", Event: dag.EventMerged,
	}); err != nil {
		t.Fatalf("append event: %v", err)
	}

	st, err := s.LoadState()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	n, _ := st.Node("a")
	if n.Status != dag.StatusDone {
		t.Fatalf("status = %s, want done: the log records the merge the shard write lost", n.Status)
	}
}

func TestWritingRefusesAContractViolation(t *testing.T) {
	s := store(t)
	bad := project()
	bad.Status = "invented"
	err := s.SaveProject(bad)
	if err == nil {
		t.Fatal("a manifest with a status outside the closed set was written")
	}
	var contract *effort.ContractError
	if !errors.As(err, &contract) {
		t.Fatalf("error = %v, want a contract violation naming every problem", err)
	}
	if _, statErr := os.Stat(s.L.Project()); statErr == nil {
		t.Error("a refused write still created the file")
	}
}

func TestReadingRefusesACorruptedArtifact(t *testing.T) {
	s := store(t)
	if err := s.SaveProject(project()); err != nil {
		t.Fatalf("save project: %v", err)
	}
	if err := os.WriteFile(s.L.Project(), []byte(`{"schema_version":"1.0.0"}`), 0o600); err != nil {
		t.Fatalf("corrupt project: %v", err)
	}
	if _, err := s.LoadProject(); err == nil {
		t.Fatal("a manifest missing required members was read")
	}
}

func TestMirrorIsWrittenWithItsCanonicalDocument(t *testing.T) {
	l, err := effort.Create(t.TempDir(), "e")
	if err != nil {
		t.Fatalf("create effort: %v", err)
	}
	tmpl, err := docmirror.Parse("project", "# {{.name}}\n\nStatus: {{.status}}\n")
	if err != nil {
		t.Fatalf("parse mirror template: %v", err)
	}
	s := effort.New(l, map[string]*template.Template{"project": tmpl})
	if err := s.SaveProject(project()); err != nil {
		t.Fatalf("save project: %v", err)
	}
	mirror, err := os.ReadFile(l.ProjectMirror())
	if err != nil {
		t.Fatalf("read mirror: %v", err)
	}
	if !strings.Contains(string(mirror), docmirror.Marker) {
		t.Error("the mirror is not marked generated")
	}
	if !strings.Contains(string(mirror), "Status: ready") {
		t.Errorf("the mirror does not reflect the document: %s", mirror)
	}
}

func TestArchiveMovesDetailOutOfTheHotPath(t *testing.T) {
	s := store(t)
	detail := dag.Detail{
		ID: "a", Intent: "do the thing", DeliverableKind: dag.KindCode,
		Acceptance: []string{"it is done"},
		Stages:     []dag.Stage{{Stage: "build", Role: "builder", Model: "claude-sonnet-5"}},
	}
	if err := s.SaveDetail(detail); err != nil {
		t.Fatalf("save detail: %v", err)
	}
	ref, err := s.ArchiveNode("a")
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	if !strings.HasPrefix(ref, "archive/") {
		t.Errorf("archived ref = %q, want a path under archive/", ref)
	}
	if _, err := os.Stat(filepath.Join(s.L.NodeDir(), "a.json")); err == nil {
		t.Error("the hot-path detail record survived archival")
	}
	if _, err := s.LoadDetail("a"); err != nil {
		t.Errorf("an archived node is no longer readable: %v", err)
	}
}

func TestEveryEphemeralDirectoryIsIgnored(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", ".gitignore"))
	if err != nil {
		t.Fatalf("read the repository ignore file: %v", err)
	}
	for _, name := range effort.Ephemeral {
		pattern := effort.DirName + "/*/" + name + "/"
		if !strings.Contains(string(raw), pattern) {
			t.Errorf("%s is ephemeral but %q is not ignored; the enumerators have drifted", name, pattern)
		}
	}
}

func TestEveryEphemeralDirectoryIsCreated(t *testing.T) {
	root := t.TempDir()
	l, err := effort.Create(root, "e")
	if err != nil {
		t.Fatalf("create effort: %v", err)
	}
	for _, name := range effort.Ephemeral {
		if _, err := os.Stat(filepath.Join(l.Dir(), name)); err != nil {
			t.Errorf("a new effort has no %s directory: %v", name, err)
		}
	}
}

func TestAGateDetailRoundTripsWithNoDispatchArtifacts(t *testing.T) {
	s := store(t)
	signal := "the release repo exists and is clonable"
	if err := s.SaveDetail(dag.Detail{
		ID: "g", Intent: "confirm the signal before anything is tagged", DeliverableKind: dag.KindGate,
		Acceptance:   []string{"an operator confirmed it, and the record says who, when and against what"},
		Precondition: &dag.Precondition{Signal: signal},
	}); err != nil {
		t.Fatalf("save a gate detail: %v", err)
	}
	loaded, err := s.LoadDetail("g")
	if err != nil {
		t.Fatalf("load a gate detail: %v", err)
	}
	if loaded.Precondition == nil || loaded.Precondition.Signal != signal {
		t.Fatalf("the gate lost the signal it declares: %+v", loaded)
	}
	if len(loaded.Stages) != 0 {
		t.Errorf("a gate came back with stages %v", loaded.Stages)
	}
}
