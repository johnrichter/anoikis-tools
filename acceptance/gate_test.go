package acceptance_test

import (
	"flag"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/johnrichter/anoikis-tools/acceptance"
)

// update regenerates the committed report instead of comparing against it.
var update = flag.Bool("update", false, "rewrite the committed acceptance report from this run")

// repoRoot returns the checkout this test file lives in, derived from its own
// source path rather than the directory the test happens to run from.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve this test file's own path")
	}
	return filepath.Dir(filepath.Dir(file))
}

// TestGate is the gate itself: every clause of the known-good target,
// evaluated against this checkout. A clause that does not hold fails here with
// its violations, and the report says the same thing in a form an operator can
// read.
func TestGate(t *testing.T) {
	report, err := acceptance.Run(repoRoot(t))
	if err != nil {
		t.Fatalf("run the gate: %v", err)
	}
	writeReport(t, report)

	for _, c := range report.Unmet() {
		t.Errorf("%s does not hold\n  requires: %s\n  asserted: %s\n  violations:\n    %s",
			c.ID, c.Requires, c.Asserts, strings.Join(c.Violations, "\n    "))
	}
	if report.Verdict != acceptance.VerdictGreen {
		t.Errorf("verdict = %s (%d of %d clauses hold); this pauses the switchover",
			report.Verdict, report.Counts.Met, report.Counts.Clauses)
	}
}

// writeReport compares the rendered report against the committed one, or
// rewrites it when asked. The report is deterministic, so a difference means
// the committed copy is stale and no longer evidence of anything.
func writeReport(t *testing.T, report acceptance.Report) {
	t.Helper()
	raw, err := report.JSON()
	if err != nil {
		t.Fatalf("render the report: %v", err)
	}
	rendered := map[string][]byte{
		acceptance.ReportJSON:     raw,
		acceptance.ReportMarkdown: report.Markdown(),
	}
	dir := filepath.Join(repoRoot(t), "acceptance")
	for name, want := range rendered {
		path := filepath.Join(dir, name)
		if *update {
			if err := os.WriteFile(path, want, 0o644); err != nil {
				t.Fatalf("write %s: %v", name, err)
			}
			continue
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v; run `go test ./acceptance -run TestGate -update` to write it", name, err)
		}
		if string(got) != string(want) {
			t.Errorf("%s is stale; run `go test ./acceptance -run TestGate -update`", name)
		}
	}
}

// TestEveryClauseIsWellFormed checks the registry itself, since a clause with
// no check or no stated assertion would report a pass that means nothing.
func TestEveryClauseIsWellFormed(t *testing.T) {
	report, err := acceptance.Run(repoRoot(t))
	if err != nil {
		t.Fatalf("run the gate: %v", err)
	}
	if report.Counts.Clauses == 0 {
		t.Fatal("the registry holds no clause")
	}
	seen := map[string]bool{}
	prefix := map[acceptance.Source]string{
		acceptance.SourcePriorSpec: "prior-spec/",
		acceptance.SourceCoreModel: "core-model/",
		acceptance.SourceV1Gates:   "v1/",
	}
	for _, c := range report.Clauses {
		if seen[c.ID] {
			t.Errorf("clause %s is registered twice", c.ID)
		}
		seen[c.ID] = true
		if want, ok := prefix[c.Source]; !ok || !strings.HasPrefix(c.ID, want) {
			t.Errorf("clause %s does not carry the prefix of its source %q", c.ID, c.Source)
		}
		if strings.TrimSpace(c.Requires) == "" || strings.TrimSpace(c.Asserts) == "" {
			t.Errorf("clause %s does not state both what it requires and what was asserted", c.ID)
		}
		switch c.Bar {
		case acceptance.BarBuild:
			if c.Measured != "" {
				t.Errorf("clause %s is held to the build bar yet defers a measurement", c.ID)
			}
		case acceptance.BarMechanism:
			if c.Measured == "" {
				t.Errorf("clause %s is held to its mechanism yet names no measurement that settles it", c.ID)
			}
		default:
			t.Errorf("clause %s declares an unknown bar %q", c.ID, c.Bar)
		}
	}
	for _, s := range []acceptance.Source{acceptance.SourcePriorSpec, acceptance.SourceCoreModel, acceptance.SourceV1Gates} {
		if report.BySource[s].Clauses == 0 {
			t.Errorf("no clause covers %s", s)
		}
	}
	if len(report.OpenConditions) == 0 {
		t.Error("no open condition is recorded, so the report claims the live measurements were made here")
	}
}

// TestReportIsDeterministic checks the committed report is evidence rather
// than a snapshot: two runs over one checkout must render identically.
func TestReportIsDeterministic(t *testing.T) {
	first, err := acceptance.Run(repoRoot(t))
	if err != nil {
		t.Fatalf("run the gate: %v", err)
	}
	second, err := acceptance.Run(repoRoot(t))
	if err != nil {
		t.Fatalf("run the gate again: %v", err)
	}
	a, err := first.JSON()
	if err != nil {
		t.Fatalf("render the first report: %v", err)
	}
	b, err := second.JSON()
	if err != nil {
		t.Fatalf("render the second report: %v", err)
	}
	if string(a) != string(b) {
		t.Error("two runs over one checkout rendered different reports")
	}
	if string(first.Markdown()) != string(second.Markdown()) {
		t.Error("two runs over one checkout rendered different readable reports")
	}
}
