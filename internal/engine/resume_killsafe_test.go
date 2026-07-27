package engine_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/johnrichter/anoikis-tools/internal/dag"
	"github.com/johnrichter/anoikis-tools/internal/effort"
	"github.com/johnrichter/anoikis-tools/internal/engine"
)

// This test exercises kill-safe resume end to end through real files, rather
// than through synthetic in-memory events: real AppendEvent writes, a real
// OS-level byte truncation mid-line simulating a hard kill, a real
// ScanRunLog read of the damaged file, and Resume's classification of the
// result. Every layer the unit tests exercise separately is wired together
// here to prove the seam between them holds.
func TestKillSafeResumeSurvivesARealTruncatedRunLog(t *testing.T) {
	root := t.TempDir()
	store := effort.New(effort.Layout{Root: root, Slug: "eff"}, nil)

	now := func() string { return time.Now().UTC().Format(time.RFC3339Nano) }

	// Three prior transitions settle cleanly: one run merges, one completes
	// (finished, not yet merged), and a third is dispatched and then hard
	// killed mid-append before any completion event lands.
	events := []dag.LogEvent{
		{SchemaVersion: dag.SchemaVersion, TS: now(), RunID: "r-merged", NodeID: "a", Event: dag.EventDispatched},
		{SchemaVersion: dag.SchemaVersion, TS: now(), RunID: "r-merged", NodeID: "a", Event: dag.EventComplete},
		{SchemaVersion: dag.SchemaVersion, TS: now(), RunID: "r-merged", NodeID: "a", Event: dag.EventMerged},
		{SchemaVersion: dag.SchemaVersion, TS: now(), RunID: "r-finished", NodeID: "b", Event: dag.EventDispatched},
		{SchemaVersion: dag.SchemaVersion, TS: now(), RunID: "r-finished", NodeID: "b", Event: dag.EventComplete},
		{SchemaVersion: dag.SchemaVersion, TS: now(), RunID: "r-killed", NodeID: "c", Event: dag.EventDispatched,
			BaseRef: "deadbeef", WorktreeRef: "wt/c", PromptRef: "prompts/c.md", PromptDigest: effort.Digest("do the thing")},
	}
	for _, e := range events {
		if err := store.AppendEvent(e); err != nil {
			t.Fatalf("append %s/%s: %v", e.RunID, e.Event, err)
		}
	}

	// Append one more event for r-killed, then sever the file mid-write: no
	// trailing newline, exactly what a SIGKILL between the write and the
	// fsync (or between two writes) leaves behind.
	logPath := store.L.RunLog()
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read run log: %v", err)
	}
	killed := append(append([]byte{}, raw...), []byte(`{"schema_version":"`+dag.SchemaVersion+`","ts":"`+now()+`","run_id":"r-killed","node_id":"c","event":"complete"`)...)
	if err := os.WriteFile(logPath, killed, 0o644); err != nil {
		t.Fatalf("simulate hard kill: %v", err)
	}

	scan, err := store.ScanRunLog(0)
	if err != nil {
		t.Fatalf("scan truncated run log: %v", err)
	}
	if scan.Damaged != 1 {
		t.Fatalf("damaged = %d, want exactly 1 (the severed final line)", scan.Damaged)
	}
	if len(scan.Events) != len(events) {
		t.Fatalf("events recovered = %d, want %d — undamaged history must survive the kill", len(scan.Events), len(events))
	}

	st := dag.State{Events: scan.Events}
	env := engine.Env{Tool: "anoikis", Effort: "eff"}
	plan := engine.Resume(st, scan.Damaged, scan.DamageDetail, env)

	byRun := map[string]engine.ResumeItem{}
	for _, it := range plan.Items {
		byRun[it.RunID] = it
	}

	if got := byRun["r-merged"].Action; got != engine.ResumeSkip {
		t.Errorf("r-merged action = %q, want skip (already merged)", got)
	}
	if got := byRun["r-finished"].Action; got != engine.ResumeRerecord {
		t.Errorf("r-finished action = %q, want rerecord (finished but never merged)", got)
	}
	killedItem := byRun["r-killed"]
	if killedItem.Action != engine.ResumeReissue {
		t.Fatalf("r-killed action = %q, want reissue — the complete event was severed, so its latest survived event is still dispatched", killedItem.Action)
	}
	// The severed complete event must not have been silently accepted as if
	// it were real: reissue must still carry the original dispatch's own
	// worktree/prompt identity forward, proving the classification came from
	// the dispatched event and not from a half-read complete.
	if killedItem.BaseRef != "deadbeef" || killedItem.WorktreeRef != "wt/c" || killedItem.PromptRef != "prompts/c.md" {
		t.Errorf("reissue lost the interrupted run's identity: %+v", killedItem)
	}
	if plan.Damaged != 1 || plan.DamageDetail == "" {
		t.Errorf("resume plan did not surface the kill as a caveat: damaged=%d detail=%q", plan.Damaged, plan.DamageDetail)
	}

	// A second, independent scan from offset 0 (a fresh process reopening
	// the same log) must reach an identical, deterministic verdict.
	scan2, err := store.ScanRunLog(0)
	if err != nil {
		t.Fatalf("second scan: %v", err)
	}
	if scan2.Damaged != scan.Damaged || len(scan2.Events) != len(scan.Events) {
		t.Fatalf("a repeated scan of the same damaged file disagreed with the first: %+v vs %+v", scan2, scan)
	}

	// Now simulate the surviving process appending the real completion for
	// r-killed after the crash — the log must not have been rewritten, the
	// damaged bytes must not block a further append, and the file must still
	// end up well-formed.
	if err := store.AppendEvent(dag.LogEvent{SchemaVersion: dag.SchemaVersion, TS: now(), RunID: "r-killed", NodeID: "c", Event: dag.EventComplete}); err != nil {
		t.Fatalf("append after damage: %v", err)
	}
	finalScan, err := store.ScanRunLog(0)
	if err != nil {
		t.Fatalf("scan after recovery append: %v", err)
	}
	if finalScan.Damaged != 1 {
		t.Fatalf("damaged after recovery append = %d, want the original 1 damaged line to remain the only damage", finalScan.Damaged)
	}
	if len(finalScan.Events) != len(events)+1 {
		t.Fatalf("events after recovery = %d, want %d (original history plus the new completion)", len(finalScan.Events), len(events)+1)
	}
	if finalScan.Events[len(finalScan.Events)-1].Event != dag.EventComplete {
		t.Fatalf("last recovered event = %q, want complete", finalScan.Events[len(finalScan.Events)-1].Event)
	}
	_ = filepath.Base(logPath)
}
