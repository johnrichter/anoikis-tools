// Package schemas is the home of the Anoikis artifact contracts: the JSON
// Schemas this engine owns, and the only place they are compiled.
//
// The schema files under anoikis/ are the canonical bytes. They are embedded
// so a released binary enforces exactly the contract it shipped with, and
// they stay ordinary files on disk so any other tool — or a dispatch payload
// that must carry a contract verbatim — can read the same bytes rather than
// transcribe them.
//
// Compiled schemas are built once on first use and shared thereafter; a
// schema that fails to compile is a packaging defect and is reported as one,
// never treated as "nothing to validate".
package schemas

import (
	"embed"
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strings"
	"sync"

	"github.com/johnrichter/claude-shared-tooling/go/clikit"
	"github.com/johnrichter/claude-shared-tooling/go/schema"
)

//go:embed anoikis/*.schema.json
var files embed.FS

// dir is the embedded location of the canonical schema files, and the path
// they occupy in the repository.
const dir = "anoikis"

// Artifact names the contract a document is validated against. The value is
// the schema file's base name without its extension, so a name maps to a
// file with no lookup table to drift.
type Artifact string

// The artifacts this engine owns. All but BoundaryManifest are enforced by compiling this
// file through Compiled/Validate below; BoundaryManifest is enforced at runtime by
// boundary.Validate's own Go decode, and is canonical here only so a roster brief's
// output_schema reference has exactly one file to resolve against.
const (
	HarnessPolicy    Artifact = "harness-policy"
	Project          Artifact = "project"
	GraphIndex       Artifact = "graph-index"
	GraphShard       Artifact = "graph-shard"
	Node             Artifact = "node"
	Gates            Artifact = "gates"
	RunLogEvent      Artifact = "run-log-event"
	RunResult        Artifact = "run-result"
	BoundaryManifest Artifact = "boundary-manifest"
	FindingsRegister Artifact = "findings-register"
	ReviewFindings   Artifact = "review-findings"
)

// All returns every owned artifact, sorted, so a completeness check can
// enumerate the contract set without restating it.
func All() []Artifact {
	entries, err := files.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make([]Artifact, 0, len(entries))
	for _, e := range entries {
		out = append(out, Artifact(strings.TrimSuffix(e.Name(), ".schema.json")))
	}
	slices.Sort(out)
	return out
}

// Path returns a's canonical repository-relative file path.
func (a Artifact) Path() string { return path.Join(dir, string(a)+".schema.json") }

// Bytes returns a's canonical schema bytes exactly as they are stored.
func (a Artifact) Bytes() ([]byte, error) {
	b, err := files.ReadFile(a.Path())
	if err != nil {
		return nil, fmt.Errorf("schemas: read %s: %w", a.Path(), err)
	}
	return b, nil
}

var (
	compiledOnce sync.Mutex
	compiled     = map[Artifact]*schema.Schema{}
)

// Compiled returns a's compiled schema, compiling it on first use.
func (a Artifact) Compiled() (*schema.Schema, error) {
	compiledOnce.Lock()
	defer compiledOnce.Unlock()
	if s, ok := compiled[a]; ok {
		return s, nil
	}
	raw, err := a.Bytes()
	if err != nil {
		return nil, err
	}
	s, err := schema.Compile(a.Path(), raw)
	if err != nil {
		return nil, err
	}
	compiled[a] = s
	return s, nil
}

// Validate checks doc against a. doc is marshalled and re-decoded first, so a
// Go struct and a document read from disk are held to the identical contract
// — there is no looser in-memory path.
//
// The returned diagnostics are the contract violations, one per failing
// constraint; err is non-nil only when validation could not be performed at
// all.
func (a Artifact) Validate(doc any) ([]clikit.Diagnostic, error) {
	s, err := a.Compiled()
	if err != nil {
		return nil, err
	}
	tree, err := asTree(doc)
	if err != nil {
		return nil, fmt.Errorf("schemas: %s: %w", a, err)
	}
	return schema.Validate(s, tree)
}

// asTree renders doc as the decoded JSON tree a JSON Schema validator works
// over.
func asTree(doc any) (any, error) {
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("marshal document: %w", err)
	}
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		return nil, fmt.Errorf("decode document: %w", err)
	}
	return tree, nil
}
