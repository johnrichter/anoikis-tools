package admission_test

import (
	"testing"

	"github.com/johnrichter/anoikis-tools/internal/admission"
	"github.com/johnrichter/anoikis-tools/internal/dag"
	"github.com/johnrichter/anoikis-tools/internal/ids"
	"github.com/johnrichter/claude-shared-tooling/go/graph"
)

// prover registers the one path domain these tests claim in.
func prover(t *testing.T) *graph.Prover {
	t.Helper()
	p, err := graph.NewProver(graph.PathDomain("path"))
	if err != nil {
		t.Fatalf("build prover: %v", err)
	}
	return p
}

// build assembles a graph from nodes, in the order given.
func build(t *testing.T, nodes ...dag.Node) *graph.Graph[string, dag.Node] {
	t.Helper()
	g := graph.New[string, dag.Node](ids.GraphScheme(ids.Opaque{}))
	for _, n := range nodes {
		if err := g.AddNode(n.ID, n); err != nil {
			t.Fatalf("add node %s: %v", n.ID, err)
		}
	}
	for _, n := range nodes {
		for _, d := range n.Deps {
			if err := g.AddDep(n.ID, d); err != nil {
				t.Fatalf("add dep %s->%s: %v", n.ID, d, err)
			}
		}
	}
	return g
}

// node builds a ready node claiming one directory.
func node(id string, dir string) dag.Node {
	n := dag.Node{ID: id, Title: id, Status: dag.StatusReady, VerifyTier: dag.VerifyCheap, DetailRef: "nodes/" + id + ".json"}
	if dir != "" {
		n.Surface = []dag.Claim{{Domain: "path", Kind: graph.PathDir, Value: dir}}
	}
	return n
}

func TestDisjointSurfacesCoBatch(t *testing.T) {
	g := build(t, node("a", "svc/alpha"), node("b", "svc/beta"), node("c", "svc/gamma"))
	batch, err := admission.Admit(g, []string{"a", "b", "c"}, prover(t), 8)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if len(batch.Members) != 3 {
		t.Fatalf("provably disjoint nodes were not co-batched: members=%v deferred=%v", batch.Members, batch.Deferred)
	}
	if batch.SoloFallback {
		t.Error("a three-node batch reported the batch-of-one fallback")
	}
}

func TestOverlappingSurfaceIsDeferredWithItsVerdict(t *testing.T) {
	g := build(t, node("a", "svc/alpha"), node("b", "svc/alpha/inner"))
	batch, err := admission.Admit(g, []string{"a", "b"}, prover(t), 8)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if len(batch.Members) != 1 || batch.Members[0] != "a" {
		t.Fatalf("overlapping nodes were co-batched: %v", batch.Members)
	}
	if len(batch.Deferred) != 1 || batch.Deferred[0].Node != "b" || batch.Deferred[0].Blocker != "a" {
		t.Fatalf("deferral did not name the blocking pair: %+v", batch.Deferred)
	}
	if batch.Deferred[0].Verdict.Disjoint() {
		t.Error("a deferral carries a disjoint verdict, which is self-contradictory")
	}
	if !batch.SoloFallback {
		t.Error("a one-member batch alongside a deferral did not report the batch-of-one fallback")
	}
}

func TestEmptySurfaceIsNeverProvenDisjoint(t *testing.T) {
	g := build(t, node("a", "svc/alpha"), node("b", ""))
	batch, err := admission.Admit(g, []string{"a", "b"}, prover(t), 8)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if len(batch.Members) != 1 {
		t.Fatalf("a node with no declared surface was co-batched: %v", batch.Members)
	}
}

func TestUndeclaredDomainIsNeverProvenDisjoint(t *testing.T) {
	a := node("a", "svc/alpha")
	b := dag.Node{ID: "b", Title: "b", Status: dag.StatusReady, VerifyTier: dag.VerifyCheap, DetailRef: "nodes/b.json",
		Surface: []dag.Claim{{Domain: "queue", Kind: "name", Value: "jobs"}}}
	g := build(t, a, b)
	batch, err := admission.Admit(g, []string{"a", "b"}, prover(t), 8)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if len(batch.Members) != 1 {
		t.Fatalf("a claim in an unregistered domain was proven disjoint: %v", batch.Members)
	}
	if got := batch.Deferred[0].Verdict.Ground; got != graph.GroundUnregistered {
		t.Errorf("deferral ground = %q, want %q", got, graph.GroundUnregistered)
	}
}

func TestDependentNodesNeverCoBatch(t *testing.T) {
	a := node("a", "svc/alpha")
	b := node("b", "svc/beta")
	b.Deps = []string{"a"}
	g := build(t, a, b)
	batch, err := admission.Admit(g, []string{"a", "b"}, prover(t), 8)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if len(batch.Members) != 1 {
		t.Fatalf("dependent nodes with disjoint surfaces were co-batched: %v", batch.Members)
	}
	if got := batch.Deferred[0].Verdict.Ground; got != graph.GroundDependencyLinked {
		t.Errorf("deferral ground = %q, want %q", got, graph.GroundDependencyLinked)
	}
}

func TestBatchOfOneIsAlwaysAdmitted(t *testing.T) {
	g := build(t, node("a", "shared"), node("b", "shared"), node("c", "shared"))
	batch, err := admission.Admit(g, []string{"a", "b", "c"}, prover(t), 8)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if len(batch.Members) != 1 || batch.Members[0] != "a" {
		t.Fatalf("with nothing provably disjoint, the batch was not exactly the first ready node: %v", batch.Members)
	}
	if len(batch.Deferred) != 2 {
		t.Fatalf("deferrals = %d, want 2", len(batch.Deferred))
	}
}

func TestGroupSizeBoundDefersRatherThanDrops(t *testing.T) {
	g := build(t, node("a", "svc/a"), node("b", "svc/b"), node("c", "svc/c"))
	batch, err := admission.Admit(g, []string{"a", "b", "c"}, prover(t), 2)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if len(batch.Members) != 2 {
		t.Fatalf("members = %v, want the group bound of 2", batch.Members)
	}
	if len(batch.Deferred) != 1 || batch.Deferred[0].Verdict.Ground != graph.GroundGroupFull {
		t.Fatalf("a node turned away by the group bound was not deferred on that ground: %+v", batch.Deferred)
	}
}

func TestNoReadyNodesIsAnError(t *testing.T) {
	g := build(t, node("a", "svc/a"))
	if _, err := admission.Admit(g, nil, prover(t), 8); err == nil {
		t.Fatal("admitting an empty ready set did not report an error")
	}
}

func TestAdmissionIsDeterministic(t *testing.T) {
	nodes := []dag.Node{node("a", "svc/a"), node("b", "svc/a/inner"), node("c", "svc/c"), node("d", "svc/c")}
	first, err := admission.Admit(build(t, nodes...), []string{"a", "b", "c", "d"}, prover(t), 8)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	for i := range 20 {
		next, err := admission.Admit(build(t, nodes...), []string{"a", "b", "c", "d"}, prover(t), 8)
		if err != nil {
			t.Fatalf("admit run %d: %v", i, err)
		}
		if len(next.Members) != len(first.Members) {
			t.Fatalf("run %d admitted %v, first run admitted %v", i, next.Members, first.Members)
		}
		for j := range next.Members {
			if next.Members[j] != first.Members[j] {
				t.Fatalf("run %d admitted %v, first run admitted %v", i, next.Members, first.Members)
			}
		}
	}
}
