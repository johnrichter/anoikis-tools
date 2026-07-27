package effort_test

import (
	"os"
	"strings"
	"testing"

	"github.com/johnrichter/anoikis-tools/internal/dag"
	"github.com/johnrichter/anoikis-tools/internal/effort"
)

// store builds an empty effort in a temp directory.
func store(t *testing.T) *effort.Store {
	t.Helper()
	l, err := effort.Create(t.TempDir(), "e")
	if err != nil {
		t.Fatalf("create effort: %v", err)
	}
	return effort.New(l, nil)
}

// dispatched is a well-formed dispatch transition for one run.
func dispatched(runID, nodeID string) dag.LogEvent {
	return dag.LogEvent{
		TS: "2026-01-01T00:00:00Z", RunID: runID, NodeID: nodeID, Event: dag.EventDispatched,
		LayerSeq: 0, WorktreeRef: "e/" + nodeID + "-l0", BaseRef: "abc123",
		PromptRef: "prompts/" + runID + ".txt", PromptDigest: effort.Digest("prompt for " + nodeID),
	}
}

func TestAppendAndScanRoundTrip(t *testing.T) {
	s := store(t)
	for _, e := range []dag.LogEvent{dispatched("r1", "a"), dispatched("r2", "b")} {
		if err := s.AppendEvent(e); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	scan, err := s.ScanRunLog(0)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(scan.Events) != 2 || scan.Damaged != 0 {
		t.Fatalf("scan read %d events with %d damaged, want 2 and 0", len(scan.Events), scan.Damaged)
	}
	if scan.Events[0].PromptDigest == "" {
		t.Error("the prompt digest a replay proves itself with was not persisted")
	}
}

func TestMissingRunLogIsNotAnError(t *testing.T) {
	scan, err := store(t).ScanRunLog(0)
	if err != nil {
		t.Fatalf("scanning a run log that does not exist yet failed: %v", err)
	}
	if len(scan.Events) != 0 {
		t.Errorf("an absent run log produced %d events", len(scan.Events))
	}
}

func TestOversizedEventIsRefusedNotTruncated(t *testing.T) {
	s := store(t)
	e := dispatched("r1", "a")
	e.Detail = strings.Repeat("x", 500)
	e.WorktreeRef = strings.Repeat("y", 4000)
	err := s.AppendEvent(e)
	if err == nil {
		t.Fatal("an event too large for one atomic append was accepted")
	}
	scan, scanErr := s.ScanRunLog(0)
	if scanErr != nil {
		t.Fatalf("scan: %v", scanErr)
	}
	if len(scan.Events) != 0 {
		t.Errorf("a refused event still reached the log: %d events", len(scan.Events))
	}
}

func TestSchemaViolatingEventIsRefused(t *testing.T) {
	s := store(t)
	e := dispatched("r1", "a")
	e.Event = "invented"
	if err := s.AppendEvent(e); err == nil {
		t.Fatal("an event outside the declared transition vocabulary was accepted")
	}
}

// truncate cuts the run log short at n bytes, standing in for a process
// killed part-way through an append.
func truncate(t *testing.T, s *effort.Store, n int64) {
	t.Helper()
	if err := os.Truncate(s.L.RunLog(), n); err != nil {
		t.Fatalf("truncate run log: %v", err)
	}
}

func TestTruncatedFinalLineIsSurvivable(t *testing.T) {
	s := store(t)
	for _, e := range []dag.LogEvent{dispatched("r1", "a"), dispatched("r2", "b"), dispatched("r3", "c")} {
		if err := s.AppendEvent(e); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	info, err := os.Stat(s.L.RunLog())
	if err != nil {
		t.Fatalf("stat run log: %v", err)
	}
	truncate(t, s, info.Size()-20)

	scan, err := s.ScanRunLog(0)
	if err != nil {
		t.Fatalf("scanning a truncated run log failed instead of degrading: %v", err)
	}
	if len(scan.Events) != 2 {
		t.Fatalf("events before the damage = %d, want 2", len(scan.Events))
	}
	if scan.Damaged != 1 || scan.DamageDetail == "" {
		t.Fatalf("damage was not reported: damaged=%d detail=%q", scan.Damaged, scan.DamageDetail)
	}

	// The offset must stop short of the damaged bytes, so a later complete
	// append is read from there rather than skipped.
	if scan.Offset >= info.Size()-20 {
		t.Errorf("offset %d did not stop before the damaged tail at %d", scan.Offset, info.Size()-20)
	}
	if err := s.AppendEvent(dispatched("r4", "d")); err != nil {
		t.Fatalf("appending after damage: %v", err)
	}
	next, err := s.ScanRunLog(scan.Offset)
	if err != nil {
		t.Fatalf("resuming from the offset: %v", err)
	}
	if len(next.Events) != 1 || next.Events[0].RunID != "r4" {
		t.Fatalf("resuming from the offset read %d events, want just r4", len(next.Events))
	}
	if next.Damaged != 1 {
		t.Errorf("damaged = %d, want the one unreadable line the kill left behind", next.Damaged)
	}
}

func TestCorruptLineIsSkippedNotFatal(t *testing.T) {
	s := store(t)
	if err := s.AppendEvent(dispatched("r1", "a")); err != nil {
		t.Fatalf("append: %v", err)
	}
	f, err := os.OpenFile(s.L.RunLog(), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open run log: %v", err)
	}
	if _, err := f.WriteString("{not json at all}\n"); err != nil {
		t.Fatalf("write corrupt line: %v", err)
	}
	_ = f.Close()
	if err := s.AppendEvent(dispatched("r2", "b")); err != nil {
		t.Fatalf("append after corruption: %v", err)
	}

	scan, err := s.ScanRunLog(0)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(scan.Events) != 2 {
		t.Fatalf("events = %d, want the two well-formed ones either side of the corruption", len(scan.Events))
	}
	if scan.Damaged != 1 {
		t.Errorf("damaged = %d, want 1", scan.Damaged)
	}
}

func TestCursorRoundTrip(t *testing.T) {
	s := store(t)
	if c, err := s.LoadCursor(); err != nil || c.Offset != 0 {
		t.Fatalf("a fresh effort's cursor = %+v, %v; want a zero cursor and no error", c, err)
	}
	if err := s.SaveCursor(effort.Cursor{Offset: 512}); err != nil {
		t.Fatalf("save cursor: %v", err)
	}
	c, err := s.LoadCursor()
	if err != nil || c.Offset != 512 {
		t.Fatalf("cursor = %+v, %v; want offset 512", c, err)
	}
}

func TestFileKeyIsStableAndCollisionResistant(t *testing.T) {
	if got := effort.FileKey("node-one.v2"); got != "node-one.v2" {
		t.Errorf("an already-safe id was rewritten: %q", got)
	}
	a := effort.FileKey("svc/one")
	b := effort.FileKey("svc:one")
	if a == b {
		t.Errorf("two different ids transliterated to the same file key: %q", a)
	}
	if a != effort.FileKey("svc/one") {
		t.Error("file keys are not stable across calls")
	}
	if strings.ContainsAny(a, "/\\:") {
		t.Errorf("file key %q still carries a path separator", a)
	}
}
