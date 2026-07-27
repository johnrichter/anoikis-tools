# The injected harness-policy roster

Six agent briefs the harness-agnostic core dispatches to drive a real project design -> plan -> execution. Each is self-briefing, names its output contract by path, and terminates every failure path with a stated action. This directory is injected policy, not the Anoikis harness plugin — the plugin itself stays Phase-B.

## Roster

| Brief | Role | Runs when | Feeds |
|---|---|---|---|
| `anoikis-architect.md` | Pre-graph design: goal/brief -> `design.md` | Before any `project.json` or graph exists | `anoikis-planner` |
| `anoikis-planner.md` | Decompose `design.md` into the graph: `project.json`, `gates.json`, graph shards, node details | Once `design.md` exists, before any node is dispatched | `anoikis-builder` / `anoikis-writer` / `anoikis-tester` / `anoikis-reviewer` |
| `anoikis-builder.md` | Implement one admitted node's code deliverable | Node admitted and dispatched with `deliverable_kind=code` | test stage |
| `anoikis-writer.md` | Implement one admitted node's docs deliverable | Node admitted and dispatched with `deliverable_kind=docs` | test stage |
| `anoikis-tester.md` | Run one admitted node's test stage, report pass/fail | After the node's own build stage, same worktree | dispatched review (gate) |
| `anoikis-reviewer.md` | Judge a diff against acceptance criteria, return a verdict | Node-close or gate-boundary review, once a diff exists | engine grafts a fix node from findings, or merges |

Every brief's frontmatter carries its own complete discriminator entry against the other five roster members (`discriminators:`), each with a `reason` and, where the boundary is fuzzy, a named `tie_break`. The M1.P2.T7 lint (`agentcontract-lint`) proves that matrix is complete and every SC-AGENTCONTRACT property (self-briefing, terminated failure paths, schema-by-path, one-decision-per-choice) is structurally present:

```sh
agentcontract-lint anoikis-tools/policy/agents
```

Cell *quality* — whether a reason or tie-break actually holds up — is reviewer-checked, never mechanically verified; the lint output states this on every run.

## Return contract

Every brief returns through the same bounded manifest: [`../schemas/anoikis/boundary-manifest.schema.json`](../schemas/anoikis/boundary-manifest.schema.json), enforced at runtime by [`boundary.Validate`](../internal/dispatch/boundary/boundary.go). A deliverable role (architect/planner/builder/writer/tester) is held to the wider deliverable ceiling; the reviewer's control-plane verdict is held to the tighter one. No return ever carries deliverable content itself — only status, artifact paths, up to five facts, and the next action.

## AC4 — the routing-rate invariant is registered

Acceptance for this roster splits in two. The deterministic half — granted tools suffice, and a trial return per agent validates against the bounded-manifest contract — is a build bar, asserted in [`../internal/dispatch/boundary/zz_rosterdispatch_test.go`](../internal/dispatch/boundary/zz_rosterdispatch_test.go). The probabilistic half — whether a brief's routing description (`description:`/triggers) actually fires for its declared request class — is explicitly **not** a build bar (SC-ENFORCE): it is a rung-4 advisory with a per-model floor that stays null until a real calibration run sets it, per the invariant-registry's own rule that a rung-4 floor is `declared-unmeasured` until measured.

That invariant is registered as `anoikis-tools.policy.roster-routing-fires-for-request-class` in `ai-shared-lib`'s `schemas/invariant-registry/invariant-registry.json` (register entry `rp-anoikis-roster-routing-rate`), status `planned`, `measurement_status: declared-unmeasured`, with a null per-model floor for every declared model. The floor values are set by the registry's first calibration run against this roster, not by this document.
