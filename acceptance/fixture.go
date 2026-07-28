package acceptance

import (
	"fmt"
	"path/filepath"

	"github.com/johnrichter/anoikis-tools/internal/cliout"
	"github.com/johnrichter/anoikis-tools/internal/dag"
	"github.com/johnrichter/anoikis-tools/internal/engine"
	"github.com/johnrichter/anoikis-tools/internal/ids"
	"github.com/johnrichter/anoikis-tools/internal/policy"
	"github.com/johnrichter/claude-shared-tooling/go/graph"
)

// A behavioural clause drives the compiled engine over a fixture effort rather
// than reading source: what the engine does is the requirement, and only
// running it settles that. The fixture is deliberately the smallest graph that
// can reach the state under test, and the harness behind it is the policy file
// the checkout ships — so a clause is never satisfied by a policy written to
// satisfy it.

// examplePolicy is the shipped harness policy, relative to the checkout root.
const examplePolicy = "examples/harness-policy.json"

// fixtureEffort is the effort slug every fixture directive is built for.
const fixtureEffort = "fixture"

// fixtureBase is the commit a fixture layer's worktrees would branch from.
const fixtureBase = "0000000000000000000000000000000000000000"

// harnessOf loads the shipped harness policy from the tree and the surface
// prover it declares.
func harnessOf(t *Tree) (*policy.Harness, *graph.Prover, error) {
	h, err := policy.Load(filepath.Join(t.Root(), filepath.FromSlash(examplePolicy)))
	if err != nil {
		return nil, nil, fmt.Errorf("load %s: %w", examplePolicy, err)
	}
	p, err := h.Prover()
	if err != nil {
		return nil, nil, fmt.Errorf("build the surface prover: %w", err)
	}
	return h, p, nil
}

// fixtureNode is one node claiming a directory of its own.
func fixtureNode(id, dir string, status dag.Status, deps ...string) dag.Node {
	return dag.Node{
		ID: id, Title: id, Status: status, Deps: deps,
		Surface:     []dag.Claim{{Domain: "path", Kind: graph.PathDir, Value: dir}},
		VerifyTier:  dag.VerifyCheap,
		DetailRef:   "nodes/" + id + ".json",
		MaxAttempts: 1,
	}
}

// fixtureState is a one-gate effort around the given nodes.
func fixtureState(nodes []dag.Node, gateStatus dag.GateStatus) dag.State {
	return fixtureStateTargeting(nodes, gateStatus, "main")
}

// fixtureStateTargeting is a one-gate effort whose gate merges onto the named
// branch, so a clause can compare the one merge that signs against every other.
func fixtureStateTargeting(nodes []dag.Node, gateStatus dag.GateStatus, target string) dag.State {
	return dag.State{
		Project: dag.Project{
			SchemaVersion: dag.SchemaVersion, ID: fixtureEffort, Name: "Fixture", Version: "1.0.0",
			Status:  dag.ProjectBuilding,
			Budget:  dag.Budget{CeilingUSD: 100, EnforcedAt: "layer"},
			Signing: dag.Signing{BelowMain: "never", MainMerge: "resign-all+sign-merge-commit"},
		},
		Shards: []dag.Shard{{SchemaVersion: dag.SchemaVersion, GateID: "g1", Nodes: nodes}},
		Gates: dag.Gates{SchemaVersion: dag.SchemaVersion, Gates: []dag.Gate{{
			ID: "g1", Name: "Gate one", Status: gateStatus,
			Policy: dag.GatePolicy{Pause: true, DeepReview: "batched", MergeTarget: target, Sign: "inherit"},
		}}},
	}
}

// stepOf asks the engine for the one next action over a fixture state.
func stepOf(t *Tree, st dag.State, open ...engine.Finding) (engine.Directive, error) {
	h, prover, err := harnessOf(t)
	if err != nil {
		return engine.Directive{}, err
	}
	return engine.Step(st, h, ids.Opaque{}, prover, open, engine.Env{
		Tool: cliout.Tool, Effort: fixtureEffort, BaseRef: fixtureBase,
	})
}

// fixtureStates are the states that reach each of the five actions, named so a
// violation says which state produced it.
func fixtureStates() map[string]dag.State {
	failed := fixtureNode("a", "svc/a", dag.StatusFailed)
	failed.Attempts = 1
	merged := fixtureState([]dag.Node{fixtureNode("a", "svc/a", dag.StatusDone)}, dag.GateMerged)
	return map[string]dag.State{
		"two independent ready nodes": fixtureState([]dag.Node{
			fixtureNode("a", "svc/a", dag.StatusReady),
			fixtureNode("b", "svc/b", dag.StatusReady),
		}, dag.GatePending),
		"a run still in flight": fixtureState([]dag.Node{
			fixtureNode("a", "svc/a", dag.StatusRunning),
			fixtureNode("b", "svc/b", dag.StatusReady),
		}, dag.GatePending),
		"every node merged at an open gate": fixtureState([]dag.Node{
			fixtureNode("a", "svc/a", dag.StatusDone),
		}, dag.GatePending),
		"a node that failed with no attempt left": fixtureState([]dag.Node{failed}, dag.GatePending),
		"every node merged and every gate closed": merged,
	}
}
