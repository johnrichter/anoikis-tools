package boundary_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/johnrichter/anoikis-tools/internal/dispatch/boundary"
)

// Simulates each roster agent's declared return shape (per its brief's "## Return" section)
// against the bounded-manifest contract, both control-plane (reviewer) and deliverable
// (architect/planner/builder/writer/tester) classes.
func TestRosterReturnsValidateAgainstBoundary(t *testing.T) {
	cases := []struct {
		agent string
		class boundary.ReturnClass
		raw   string
	}{
		{"anoikis-architect", boundary.ClassDeliverable, `{"status":"pass","artifact_paths":["effort/design.md"],"facts":["drafted 8 sections"],"next_action":"hand off to anoikis-planner"}`},
		{"anoikis-planner", boundary.ClassDeliverable, `{"status":"pass","artifact_paths":["effort/project.json","effort/gates.json"],"facts":["3 gates, 9 nodes"],"next_action":"hand off to engine validate"}`},
		{"anoikis-builder", boundary.ClassDeliverable, `{"status":"pass","artifact_paths":["pkg/foo.go"],"facts":["implemented node N1"],"next_action":"hand off to test stage"}`},
		{"anoikis-writer", boundary.ClassDeliverable, `{"status":"pass","artifact_paths":["docs/foo.md"],"facts":["drafted node N2 docs"],"next_action":"hand off to test stage"}`},
		{"anoikis-tester", boundary.ClassDeliverable, `{"status":"fail","artifact_paths":["run-results/N1.json"],"facts":["2 criteria unmet"],"next_action":"hand off to dispatched review"}`},
		{"anoikis-reviewer", boundary.ClassControlPlane, `{"status":"fix","artifact_paths":["findings/gate1.json"],"facts":["1 finding: missing edge case"],"next_action":"engine grafts fix node"}`},
	}
	for _, c := range cases {
		t.Run(c.agent, func(t *testing.T) {
			m, err := boundary.Validate(c.class, []byte(c.raw))
			if err != nil {
				t.Fatalf("%s: trial return rejected by boundary.Validate: %v", c.agent, err)
			}
			if m.Status == "" || m.NextAction == "" {
				t.Fatalf("%s: manifest missing required field: %+v", c.agent, m)
			}
		})
	}
}

// Adversarial: an over-length or unknown-field return from any roster class must be refused,
// not silently accepted or degraded.
func TestRosterReturnsRejectAdversarialInputs(t *testing.T) {
	over := make([]byte, 0, 5000)
	over = append(over, []byte(`{"status":"pass","next_action":"x","facts":["`)...)
	for i := 0; i < 4900; i++ {
		over = append(over, 'a')
	}
	over = append(over, []byte(`"]}`)...)
	if _, err := boundary.Validate(boundary.ClassDeliverable, over); err == nil {
		t.Fatal("over-length deliverable return was accepted, expected ErrOverLength")
	}

	unknown := []byte(`{"status":"pass","next_action":"x","design_prose":"leaked content"}`)
	if _, err := boundary.Validate(boundary.ClassDeliverable, unknown); err == nil {
		t.Fatal("return with unknown field was accepted, expected ErrNonConforming (DisallowUnknownFields)")
	}

	missingStatus := []byte(`{"next_action":"x"}`)
	if _, err := boundary.Validate(boundary.ClassControlPlane, missingStatus); err == nil {
		t.Fatal("return with no status was accepted, expected ErrNonConforming")
	}
}

var frontmatterToolsLine = regexp.MustCompile(`(?m)^tools:\s*(.+)$`)

// Confirms the granted-tools half of AC3: each brief's declared tools cover what its role
// actually needs and nothing it is barred from. A deliverable-producing role needs Write for
// its artifact; builder and writer additionally modify an already-admitted node's existing
// files and so need Edit; the reviewer is control-plane-only — Write is scoped to its own
// findings artifact and `no-deliverable-edit` bars it from Edit outright.
func TestRosterBriefsGrantExactlyTheToolsTheirRoleNeeds(t *testing.T) {
	want := map[string][]string{
		"anoikis-architect": {"Read", "Write", "Bash"},
		"anoikis-planner":   {"Read", "Write", "Bash"},
		"anoikis-builder":   {"Read", "Write", "Edit", "Bash"},
		"anoikis-writer":    {"Read", "Write", "Edit", "Bash"},
		"anoikis-tester":    {"Read", "Write", "Bash"},
		"anoikis-reviewer":  {"Read", "Write", "Bash"},
	}
	for agent, tools := range want {
		t.Run(agent, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("..", "..", "..", "policy", "agents", agent+".md"))
			if err != nil {
				t.Fatalf("%s: brief not found: %v", agent, err)
			}
			m := frontmatterToolsLine.FindSubmatch(raw)
			if m == nil {
				t.Fatalf("%s: no tools: line in frontmatter", agent)
			}
			got := strings.Split(string(m[1]), ",")
			for i := range got {
				got[i] = strings.TrimSpace(got[i])
			}
			if strings.Join(got, ",") != strings.Join(tools, ",") {
				t.Fatalf("%s: declared tools %v, want %v (role needs exactly these to complete its brief without over-granting)", agent, got, tools)
			}
		})
	}
}
