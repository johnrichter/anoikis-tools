package main

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// This engine is greenfield and harness-agnostic. Two properties keep it that
// way, and neither survives on intent alone, so both are asserted here:
//
//   - Nothing in the import graph reaches the prior harness this engine
//     replaces. Its plan and execution contracts are a separate, frozen
//     surface; importing any of it would re-couple the two.
//   - No name anywhere in the tree comes from that harness. A borrowed name
//     is how borrowed semantics get back in.

// foreignImports are import-path fragments that would mean this engine depends
// on the harness it replaces.
var foreignImports = []string{
	"build-helpers",
	"buildhelpers",
	"buildshim",
	"delivery-agent-team",
	"build-with-team",
}

// foreignNames are tokens from that harness's vocabulary. Each is matched
// whole-word so an unrelated identifier that merely contains one is not
// flagged.
var foreignNames = []string{
	`bh`,
	`buildhelpers`,
	`build-helpers`,
	`buildshim`,
	`build-with-team`,
	`delivery-agent-team`,
	`pbwt`,
}

// foreignArtifacts are the prior harness's own artifact contracts. This engine
// owns its artifact set outright; naming one of these would mean reading or
// writing a contract it does not own.
var foreignArtifacts = []string{
	"plan.json",
	"execution.json",
}

// idGrammar is the id shape the prior harness baked into its tooling. Node ids
// here go through a pluggable scheme, so this grammar must appear nowhere.
var idGrammar = regexp.MustCompile(`\bM[0-9]+\.P[0-9]+\.T[0-9]+\b`)

// isGate reports whether path is this gate itself, which necessarily spells
// out every name and artifact it forbids elsewhere.
func isGate(path string) bool { return filepath.Base(path) == "decoupling_test.go" }

// sourceFiles returns every Go file and owned JSON contract in the tree,
// excluding anything version control or a build put there.
func sourceFiles(t *testing.T) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".claude", "dist", "node_modules":
				return fs.SkipDir
			}
			return nil
		}
		switch filepath.Ext(path) {
		case ".go", ".json":
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk tree: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("found no source files to check")
	}
	return out
}

func TestImportGraphReachesNoPriorHarness(t *testing.T) {
	fset := token.NewFileSet()
	for _, path := range sourceFiles(t) {
		if filepath.Ext(path) != ".go" {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, spec := range file.Imports {
			imported, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatalf("%s: unquote import %s: %v", path, spec.Path.Value, err)
			}
			for _, foreign := range foreignImports {
				if strings.Contains(imported, foreign) {
					t.Errorf("%s imports %q, which reaches the harness this engine replaces", path, imported)
				}
			}
		}
	}
}

func TestNoPriorHarnessNaming(t *testing.T) {
	patterns := make([]*regexp.Regexp, 0, len(foreignNames))
	for _, name := range foreignNames {
		patterns = append(patterns, regexp.MustCompile(`(?i)(^|[^A-Za-z0-9])`+regexp.QuoteMeta(name)+`($|[^A-Za-z0-9])`))
	}
	for _, path := range sourceFiles(t) {
		if isGate(path) {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		body := string(raw)
		for i, re := range patterns {
			if re.MatchString(body) {
				t.Errorf("%s uses the name %q, which belongs to the harness this engine replaces", path, foreignNames[i])
			}
		}
	}
}

func TestNoPriorHarnessArtifactNames(t *testing.T) {
	for _, path := range sourceFiles(t) {
		if isGate(path) {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, artifact := range foreignArtifacts {
			if strings.Contains(string(raw), artifact) {
				t.Errorf("%s names %q, an artifact contract this engine does not own", path, artifact)
			}
		}
	}
}

func TestNoHardCodedIDGrammar(t *testing.T) {
	for _, path := range sourceFiles(t) {
		if isGate(path) {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if loc := idGrammar.FindString(string(raw)); loc != "" {
			t.Errorf("%s hard-codes the id %q; node ids resolve through a pluggable scheme", path, loc)
		}
	}
}
