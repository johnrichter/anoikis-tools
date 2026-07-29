package findings_test

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/johnrichter/anoikis-tools/internal/dag"
	"github.com/johnrichter/anoikis-tools/internal/effort"
	"github.com/johnrichter/anoikis-tools/internal/findings"
)

// clobberFixture is the review-verdict artifact that once overwrote an
// effort's findings register during a dogfood run, preserved verbatim as the
// evidence for this refusal.
const clobberFixture = "../../dogfood/evidence/reviews/gate-review-1-fix-verdict-findings.json"

func TestOpenRefusesARegisterPathCarryingTheReviewVerdictShape(t *testing.T) {
	l, err := effort.Create(t.TempDir(), "e")
	if err != nil {
		t.Fatalf("create effort: %v", err)
	}
	clobber, err := os.ReadFile(clobberFixture)
	if err != nil {
		t.Fatalf("read the clobber fixture: %v", err)
	}
	if err := os.WriteFile(l.Findings(), clobber, 0o600); err != nil {
		t.Fatalf("seed the register path with the clobber fixture: %v", err)
	}

	_, err = findings.Open(l, 15)
	if err == nil {
		t.Fatal("a review-verdict artifact at the register's path was read as the register")
	}
	var wrong *findings.WrongArtifactError
	if !errors.As(err, &wrong) {
		t.Fatalf("error = %v, want a *findings.WrongArtifactError", err)
	}
	for _, contract := range []string{"findings-register.schema.json", "review-findings.schema.json"} {
		if !strings.Contains(err.Error(), contract) {
			t.Errorf("error %q does not name %s", err.Error(), contract)
		}
	}
}

func TestOpenReadsAValidRegisterUnchanged(t *testing.T) {
	l, err := effort.Create(t.TempDir(), "e")
	if err != nil {
		t.Fatalf("create effort: %v", err)
	}
	reg, err := findings.Open(l, 15)
	if err != nil {
		t.Fatalf("open a fresh register: %v", err)
	}
	if _, err := reg.Add(dag.FindingSeed{Statement: "a real finding", Impact: 4, Urgency: 4}); err != nil {
		t.Fatalf("add: %v", err)
	}

	reopened, err := findings.Open(l, 15)
	if err != nil {
		t.Fatalf("reopen a valid register: %v", err)
	}
	if got := len(reopened.List()); got != 1 {
		t.Fatalf("reopened register has %d entries, want 1", got)
	}
}
