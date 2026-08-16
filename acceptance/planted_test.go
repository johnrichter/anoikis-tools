package acceptance_test

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/johnrichter/anoikis-tools/acceptance"
)

// A gate nobody has ever seen fail proves nothing. Each case below copies the
// checkout, plants one conformance violation in the copy, and requires the
// clause that owns it to name the violation and the verdict to pause. The
// plants are the realistic ways each clause is broken — including emptying the
// vocabulary a guard enforces, which is how a guard is defeated without
// touching anything it guards.

// planted is one violation and the clause that must catch it.
type planted struct {
	clause string
	plant  func(t *testing.T, root string)
}

func TestPlantedViolationsFailTheGate(t *testing.T) {
	cases := map[string]planted{
		"an unformatted source file": {
			clause: "prior-spec/production-bar",
			plant: func(t *testing.T, root string) {
				write(t, root, "internal/dag/planted.go", "package dag\n\n// Planted is unformatted.\nfunc  Planted( ) {}\n")
			},
		},
		"a link that resolves to nothing": {
			clause: "prior-spec/documentation-bar",
			plant: func(t *testing.T, root string) {
				write(t, root, "docs/planted.md", "# Planted\n\nSee [the missing page](./nowhere.md).\n")
			},
		},
		"an artifact contract that stops requiring its schema version": {
			clause: "prior-spec/artifact-contracts",
			plant: func(t *testing.T, root string) {
				const contract = "schemas/anoikis/project.schema.json"
				body := read(t, root, contract)
				// The first mention with a trailing comma is the entry in the
				// list of members the contract demands; the later one declares
				// the member's own shape and is left alone.
				stripped := strings.Replace(body, `"schema_version", `, "", 1)
				if stripped == body {
					t.Fatalf("%s no longer demands the member this case removes", contract)
				}
				write(t, root, contract, stripped)
			},
		},
		"a decoupling guard whose vocabulary has been emptied": {
			clause: "prior-spec/owes-nothing-to-the-prior-harness",
			plant: func(t *testing.T, root string) {
				guard := read(t, root, "decoupling_test.go")
				start := strings.Index(guard, "var foreignNames = []string{")
				if start < 0 {
					t.Fatal("the guard no longer declares the vocabulary this case empties")
				}
				end := strings.Index(guard[start:], "}\n")
				if end < 0 {
					t.Fatal("the guard's vocabulary declaration is unterminated")
				}
				write(t, root, "decoupling_test.go",
					guard[:start]+"var foreignNames = []string{}\n"+guard[start+end+2:])
			},
		},
		"a gate policy that keeps its own member list": {
			clause: "core-model/one-home-per-fact",
			plant: func(t *testing.T, root string) {
				edit(t, root, "schemas/anoikis/gates.schema.json", func(doc map[string]any) {
					props := doc["properties"].(map[string]any)
					props["members"] = map[string]any{"type": "array"}
				})
			},
		},
		"a harness policy with no post-merge backstop": {
			clause: "core-model/post-merge-backstop-is-always-on",
			plant: func(t *testing.T, root string) {
				edit(t, root, "examples/harness-policy.json", func(doc map[string]any) {
					delete(doc, "backstop")
				})
			},
		},
		"a stage pinned off the default context window": {
			clause: "v1/every-node-runs-at-the-default-window",
			plant: func(t *testing.T, root string) {
				edit(t, root, "examples/harness-policy.json", func(doc map[string]any) {
					stages := doc["workflow"].(map[string]any)["stages"].([]any)
					stages[0].(map[string]any)["context_window"] = "1m"
				})
			},
		},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			root := copyTree(t, repoRoot(t))
			c.plant(t, root)
			report, err := acceptance.Run(root)
			if err != nil {
				t.Fatalf("run the gate over the planted tree: %v", err)
			}
			result, ok := clause(report, c.clause)
			if !ok {
				t.Fatalf("%s is not a registered clause", c.clause)
			}
			if result.Met {
				t.Errorf("%s still holds with the violation planted; the clause does not catch it", c.clause)
			}
			if len(result.Violations) == 0 {
				t.Errorf("%s failed without naming a violation, so the report is not auditable", c.clause)
			}
			if report.Verdict != acceptance.VerdictPause || !report.ForcesPause {
				t.Errorf("verdict = %s, forces pause = %t; a planted violation must pause", report.Verdict, report.ForcesPause)
			}
		})
	}
}

// TestAnUntouchedCopyReportsIdentically checks the gate is a function of the
// tree and nothing else: the same checkout, copied, must produce the same
// report — otherwise a planted case proves nothing about what changed it.
func TestAnUntouchedCopyReportsIdentically(t *testing.T) {
	original, err := acceptance.Run(repoRoot(t))
	if err != nil {
		t.Fatalf("run the gate: %v", err)
	}
	copied, err := acceptance.Run(copyTree(t, repoRoot(t)))
	if err != nil {
		t.Fatalf("run the gate over the copy: %v", err)
	}
	a, err := original.JSON()
	if err != nil {
		t.Fatalf("render the report: %v", err)
	}
	b, err := copied.JSON()
	if err != nil {
		t.Fatalf("render the copy's report: %v", err)
	}
	if string(a) != string(b) {
		t.Error("an untouched copy of the checkout produced a different report")
	}
}

// clause returns one clause's result from a report.
func clause(report acceptance.Report, id string) (acceptance.Result, bool) {
	for _, c := range report.Clauses {
		if c.ID == id {
			return c, true
		}
	}
	return acceptance.Result{}, false
}

// copyTree copies the checkout into a temporary directory, skipping what the
// gate never reads, and preserving file modes so an executability check means
// the same thing in the copy.
func copyTree(t *testing.T, root string) string {
	t.Helper()
	dst := t.TempDir()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "dist", "node_modules":
				return fs.SkipDir
			}
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), raw, info.Mode().Perm())
	})
	if err != nil {
		t.Fatalf("copy the checkout: %v", err)
	}
	return dst
}

// read returns a file from the copied checkout.
func read(t *testing.T, root, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(raw)
}

// write replaces a file in the copied checkout, creating its directory.
func write(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create the directory for %s: %v", rel, err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// edit rewrites a JSON file in the copied checkout through mutate.
func edit(t *testing.T, root, rel string, mutate func(doc map[string]any)) {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(read(t, root, rel)), &doc); err != nil {
		t.Fatalf("parse %s: %v", rel, err)
	}
	mutate(doc)
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("render %s: %v", rel, err)
	}
	write(t, root, rel, string(raw)+"\n")
}
