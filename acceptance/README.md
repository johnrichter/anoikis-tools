# The greenfield acceptance gate

This engine was built from scratch against a target that already existed on paper. Building from a specification and conforming to it are different claims, and only the second one is checkable. This package checks it: every clause of the target is registered with a check, and the result is written to [`report.json`](report.json) and [`report.md`](report.md), clause by clause.

The three specifications it covers:

| Source | Clause prefix | What it holds the engine to |
|---|---|---|
| The acceptance specification of the plan this engine was rebuilt from | `prior-spec/` | The production, documentation, toolchain, contract, command, dependency, determinism, resume, decoupling, shared-library and plugin bars. |
| The core model of the work graph | `core-model/` | The execution loop, admission, the two merges, the artifact contracts, durability and the one-home-per-fact rules. |
| The first version's cost and context gates | `v1/` | Context held flat, returns bounded, the default context window, attribution, routing and deterministic dispatch. |

## Running it

```sh
go test ./acceptance                              # the gate; fails naming every clause that does not hold
go test ./acceptance -run TestGate -update        # regenerate the committed report from this checkout
```

The report is deterministic — no timestamps, no absolute paths — so a committed copy that differs from a fresh run is stale, and the gate says so rather than letting a stale report stand as evidence.

## Two bars, and the rule that assigns them

A clause is held to the **build bar** — settled here, entirely — unless its own source states it as a measurement over a real driven build. Compactions counted, cache writes trended, returns and tool calls observed: none of those can be settled by inspecting a checkout, and the switchover deliberately puts this gate *before* the driven build that would settle them.

Those clauses are held to the **mechanism bar** instead: what is checked here is the thing that makes the measurement possible, honest and loud — a bounded return that is refused rather than salvaged, a directive that carries identities rather than detail, a journalled transition carrying what a compaction would be attributed by. The measurement itself leaves the gate as an **open condition**, listed in the report with the words of the clause that owes it. Nothing is quietly downgraded: every clause's bar is in the report beside its result.

## A verdict of pause

Any clause that does not hold makes the verdict `pause`, and the gate's own test fails with it. That is the point. A greenfield rebuild can diverge from its target in ways no single test would notice, so the answer to divergence is an operator deciding — per clause — whether the engine is corrected or the divergence is accepted and the target amended. This gate never decides either, and it never trades an unmet clause for a caveat.

## Proving it has teeth

[`planted_test.go`](planted_test.go) copies the checkout, plants one realistic violation in the copy, and requires the clause that owns it to name the violation and the verdict to turn. The plants span all three specifications, and one of them empties the vocabulary a guard enforces rather than touching anything that guard protects — which is how a guard is defeated in practice. A companion test requires an untouched copy to report identically, so a planted result says something about the plant and nothing about the copying.

A behavioural clause drives the compiled engine over a fixture effort, and the harness behind that fixture is the policy file the checkout ships — so no clause can be satisfied by a policy written to satisfy it. Planting into a copy changes what those clauses read (the policy, the contracts) but not the compiled engine, which is why the plants target what the tree declares.

## What this gate does not check

- **Whether a name is a good name, or a module is well-factored.** The mechanical floor is here — formatting, documentation coverage, unfinished-work markers, contract conformance. The judgement above it is a reviewer's, and no check here pretends otherwise.
- **Anything outside this checkout.** A clause about another repository's plugins becomes, here, a clause about this engine owing them nothing.
- **The live measurements.** They are the open conditions, and they belong to the driven build that follows this gate.
