package schemas_test

import (
	"encoding/json"
	"os"
	"slices"
	"testing"

	"github.com/johnrichter/anoikis-tools/schemas"
)

// named is every artifact the package exports a constant for. All() is
// derived from the files on disk, so comparing the two catches a schema file
// added without a constant and a constant with no file behind it.
var named = []schemas.Artifact{
	schemas.HarnessPolicy, schemas.Project, schemas.GraphIndex, schemas.GraphShard,
	schemas.Node, schemas.Gates, schemas.RunLogEvent, schemas.RunResult,
	schemas.BoundaryManifest,
}

func TestEveryOwnedSchemaCompiles(t *testing.T) {
	all := schemas.All()
	if len(all) == 0 {
		t.Fatal("no schemas are embedded")
	}
	for _, a := range all {
		if _, err := a.Compiled(); err != nil {
			t.Errorf("%s does not compile: %v", a, err)
		}
	}
}

func TestConstantsAndFilesAgree(t *testing.T) {
	all := schemas.All()
	for _, a := range named {
		if !slices.Contains(all, a) {
			t.Errorf("constant %s has no schema file at %s", a, a.Path())
		}
	}
	for _, a := range all {
		if !slices.Contains(named, a) {
			t.Errorf("schema file %s has no exported constant", a.Path())
		}
	}
}

func TestEmbeddedBytesMatchTheFilesOnDisk(t *testing.T) {
	for _, a := range schemas.All() {
		embedded, err := a.Bytes()
		if err != nil {
			t.Fatalf("read embedded %s: %v", a, err)
		}
		onDisk, err := os.ReadFile(a.Path())
		if err != nil {
			t.Fatalf("read %s from disk: %v", a.Path(), err)
		}
		if string(embedded) != string(onDisk) {
			t.Errorf("%s: the embedded contract differs from the file another tool would read", a.Path())
		}
	}
}

func TestValidationRefusesAnUnknownMember(t *testing.T) {
	doc := map[string]any{
		"schema_version": "1.0.0",
		"gate_id":        "g1",
		"nodes":          []any{},
		"surprise":       true,
	}
	diags, err := schemas.GraphShard.Validate(doc)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(diags) == 0 {
		t.Fatal("a document carrying an undeclared member was accepted")
	}
}

func TestValidationRefusesAnUnknownStatus(t *testing.T) {
	raw := `{"schema_version":"1.0.0","gate_id":"g1","nodes":[
	  {"id":"a","title":"a","status":"invented","surface":[],"verify_tier":"cheap","detail_ref":"nodes/a.json"}]}`
	var doc any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	diags, err := schemas.GraphShard.Validate(doc)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(diags) == 0 {
		t.Fatal("a node with a status outside the closed set was accepted")
	}
}
