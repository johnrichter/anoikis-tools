# Anoikis artifact contracts

The JSON Schemas this engine owns and enforces. Every artifact is validated against its contract when it is read and again before it is written, so a malformed file is refused at the edge instead of reaching a scheduling decision.

These files are the canonical bytes. They are also embedded in the binary, so a released engine enforces exactly the contract it shipped with — and they stay ordinary files on disk, so another tool can read the same bytes rather than transcribe them.

## The contracts

| File | Artifact | What it governs |
|---|---|---|
| `harness-policy.schema.json` | injected policy | Stages, roles, routes, gate and review vocabulary, document mirrors, resource domains, the post-merge backstop command, the tier band, the spend source. |
| `project.schema.json` | `project.json` | Effort identity, budget, signing policy, carryover, and where every other artifact lives. |
| `graph-index.schema.json` | `graph.json` | One row per gate shard with its status counts. |
| `graph-shard.schema.json` | `graph/<gate>.json` | One gate's nodes: identity, status, edges, resource surface. |
| `node.schema.json` | `nodes/<id>.json` | One node's intent, acceptance criteria, stages, and result — or, for a gate, the operator signal it requires and the confirmation that satisfies it. |
| `gates.schema.json` | `gates.json` | Per-gate policy and status. |
| `run-log-event.schema.json` | `run-log.jsonl` | One line: a single state transition, append-only. |
| `run-result.schema.json` | `results/<id>.json` | What one node's run produced, durably. |
| `boundary-manifest.schema.json` | a dispatch return | The bounded shape every dispatched agent's return is held to. Enforced at runtime by `boundary.Validate`'s own decode, not by compiling this file — canonical here so a roster brief's `output_schema` reference has exactly one file to resolve against. |
| `findings-register.schema.json` | `findings.json` (`Layout.Findings`) | The effort's ranked findings register, exactly as `go/ledger` already reads and writes it. The version field is `schema` (`ledger@1.0.0`), not `schema_version` — the ledger library's own tag, kept faithful to what is already on disk rather than redefined here. |
| `review-findings.schema.json` | a review's findings artifact (`Layout.ReviewFindings`, `.anoikis/<effort>/reviews/<review-id>.json`) | One entry per finding an `anoikis-reviewer` fix verdict raises, scored on exactly what the engine's blocking threshold reads. Never the same path as `findings.json` — the two contracts must not share a directory convention, or a reviewer's output can again land on the register's path. |

## Conventions these contracts share

- **A version on every top object.** `schema_version` is semver. A file declaring a MAJOR this engine does not read is refused rather than reinterpreted. `findings-register.schema.json` is the one exception: its version field is `schema`, owned by `go/ledger`, and it is documented rather than newly designed here.
- **Closed vocabularies.** Statuses, events, verification tiers and deliverable kinds are enumerations. Adding or removing a member is a MAJOR change, because a consumer branches exhaustively over them.
- **No undeclared members.** Every object sets `additionalProperties: false`, so a typo is a validation error rather than a silently ignored field.
- **One fact, one home.** A node's gate is the shard it lives in; gate policy carries no membership list. Signing policy lives on the manifest; a gate only inherits it. Spend is stored where it was measured and rolled up from there.
- **Unknown is a value.** A run's spend carries a `known` flag and a reason, so a figure that could not be measured is never reported as zero. The manifest's budget counts the runs it could not price, so a rolled-up total says plainly when it is really a floor.
- **A gate is confirmed, not produced.** `deliverable_kind` is `code | docs | gate`. code and docs name an artifact an agent authors; gate names none — it is an operator-precondition boundary, verified by a recorded confirmation of the signal it declares (who confirmed, when, and against what). A gate therefore declares no stages, no worktree and no result, and the harness policy may declare no route for it: there is nothing for a builder or a writer to author, so neither is ever handed one. A confirmation missing any of its three facts is refused, which leaves the gate unmet and its dependents blocked.

## Changing one

A contract change is a change to what every stored effort means. Widen before narrowing: adding an optional member is a MINOR bump, adding an enumeration member or making a member required is MAJOR. Update the embedded constant set in the parent package at the same time — a test asserts the files and the constants agree, and that the embedded bytes match what is on disk.
