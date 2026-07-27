package schemas_test

import (
	"encoding/json"
	"os"
	"slices"
	"testing"

	"github.com/johnrichter/anoikis-tools/schemas"
	"github.com/johnrichter/claude-shared-tooling/go/clikit"
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

func TestPolicyValidationRefusesADispatchRouteForAGate(t *testing.T) {
	raw, err := os.ReadFile("../examples/harness-policy.json")
	if err != nil {
		t.Fatalf("read the example policy: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse the example policy: %v", err)
	}
	diags, err := schemas.HarnessPolicy.Validate(doc)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(diags) > 0 {
		t.Fatalf("the example policy does not satisfy its own contract: %v", diags)
	}

	routes, ok := doc["routes"].(map[string]any)
	if !ok {
		t.Fatal("the example policy declares no routes to extend")
	}
	routes["gate"] = map[string]any{"stages": []any{map[string]any{"stage": "build", "role": "builder"}}}
	diags, err = schemas.HarnessPolicy.Validate(doc)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(diags) == 0 {
		t.Fatal("a policy declaring stages for an operator-confirmed kind was accepted")
	}
}

// gateNode is a well-formed operator gate: it declares the signal it requires
// and the record that satisfies it, and nothing a dispatch would produce.
const gateNode = `{
  "schema_version": "1.0.0",
  "id": "prework",
  "intent": "Confirm the release repo exists and is clonable before anything is tagged.",
  "deliverable_kind": "gate",
  "acceptance": ["An operator has confirmed the signal, and the record says who, when, and against what."],
  "precondition": {
    "signal": "the release repo exists and is clonable",
    "confirmation": { "by": "operator", "at": "2026-07-27T09:15:00Z", "against": "the release repo exists and is clonable" }
  }
}`

func validateNode(t *testing.T, raw string) []clikit.Diagnostic {
	t.Helper()
	var doc any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	diags, err := schemas.Node.Validate(doc)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	return diags
}

func TestAGateNodeValidates(t *testing.T) {
	if diags := validateNode(t, gateNode); len(diags) > 0 {
		t.Fatalf("a well-formed gate was refused: %v", diags)
	}
}

func TestNodeValidationRefusesAMalformedGate(t *testing.T) {
	cases := map[string]string{
		"a gate declaring a produced artifact": `{
		  "schema_version": "1.0.0", "id": "g", "intent": "confirm it", "deliverable_kind": "gate",
		  "acceptance": ["confirmed"], "precondition": { "signal": "s" },
		  "result": { "artifact_refs": ["docs/out.md"] }
		}`,
		"a gate declaring stages": `{
		  "schema_version": "1.0.0", "id": "g", "intent": "confirm it", "deliverable_kind": "gate",
		  "acceptance": ["confirmed"], "precondition": { "signal": "s" },
		  "stages": [{ "stage": "build", "role": "builder", "model": "test-model" }]
		}`,
		"a gate with no precondition": `{
		  "schema_version": "1.0.0", "id": "g", "intent": "confirm it", "deliverable_kind": "gate",
		  "acceptance": ["confirmed"]
		}`,
		"a code node claiming a precondition": `{
		  "schema_version": "1.0.0", "id": "n", "intent": "build it", "deliverable_kind": "code",
		  "acceptance": ["built"], "stages": [{ "stage": "build", "role": "builder", "model": "test-model" }],
		  "precondition": { "signal": "s" }
		}`,
		"a confirmation missing what it was made against": `{
		  "schema_version": "1.0.0", "id": "g", "intent": "confirm it", "deliverable_kind": "gate",
		  "acceptance": ["confirmed"],
		  "precondition": { "signal": "s", "confirmation": { "by": "operator", "at": "2026-07-27T09:15:00Z" } }
		}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if diags := validateNode(t, raw); len(diags) == 0 {
				t.Fatalf("%s was accepted", name)
			}
		})
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
