# design.md — `runlog-stats`

Effort slug: `runlog-stats`
Status: pre-graph design. No `project.json`, graph shard, or node file exists or is authored here.

## Context & problem

An anoikis effort records its execution as an append-only run log: `run-log.jsonl`, one JSON
object per line, each line a single state transition rather than a mutable record. The event
shape is governed by `schemas/anoikis/run-log-event.schema.json` in the **anoikis-tools** repo
(`$id: https://schemas.anoikis.dev/anoikis/run-log-event.schema.json`).

Today, reading that log means reading raw JSONL by eye. There is no way to answer the two
questions an operator asks first after a run: *which nodes and roles did work, and how did
that work go*, and *what did it cost*. Cost is the sharper problem, because usage is not
always priced. Each event may carry a `usage` object whose `known` boolean states whether
usage was actually measured. A summary that adds unpriced events into the total as `0`
silently reports a cost lower than reality and gives false confidence in the figure. The
unpriced events must be visible as their own count, never folded into the sum.

This repo (`github.com/dogfood/runlog-stats`) is a fresh Go module — `go.mod` and a README
stub, one commit (`635221c`). It will hold one small, real, standalone CLI, `runlog-stats`,
that reads one effort's `run-log.jsonl` and prints that summary to stdout. It is built,
tested, and merged for real by this anoikis effort over its own graph.

Relevant grounded facts read from the schema itself (not from the brief), which shape the
design below:

- `event` is an enum of **five** values: `dispatched`, `complete`, `failed`, `merged`,
  **`grafted`**. The brief names only the first four as the counted set.
- Required fields are exactly `schema_version`, `ts`, `run_id`, `node_id`, `event`.
- `role` is **optional**. So is the whole `usage` object.
- Inside `usage`, only `known` is required; `cost_usd` is optional even when `known` is true.
- `additionalProperties: false` at both levels — a conforming log carries no fields beyond
  those defined.

## Goals & non-goals

**Goals**

1. A single standalone CLI binary, `runlog-stats`, that reads one `run-log.jsonl` and writes
   one plain-text summary block per invocation to stdout.
2. Per-`node_id` counts of `dispatched`, `complete`, `failed`, `merged` events.
3. Per-`role` counts of the same four event kinds.
4. One total cost figure: the sum of `usage.cost_usd` over every event with
   `usage.known == true`.
5. A separate count of events whose usage is not known-and-priced, reported alongside the
   total and never added into it.
6. Deterministic, byte-stable output for a given input, so its own tests can assert on it.
7. Its own tests, and a short usage doc.

**Non-goals**

- Grouping or splitting output by `run_id`. One invocation aggregates the whole input into
  one block, even when the log contains several `run_id`s.
- Full JSON Schema validation of input. The tool reads the subset of fields it needs and does
  not enforce `schema_version`, patterns, or `additionalProperties`.
- Token counts (`input_tokens`, `output_tokens`, cache token fields), `model`,
  `context_window`, `effort`, `layer_seq`, timing, or duration analysis. Cost is the only
  usage figure summarized.
- Machine-readable output (JSON/CSV), colour, tables that reflow to terminal width, or paging.
- Reading multiple log files in one invocation, globbing, or directory walking.
- Anything that writes: no mutation of the run log, no output files, no network access.
- A library API for third parties. Any exported surface exists to make the CLI testable.

## Success criteria

Each is observable by running the built tool or its tests, without consulting the author.

1. `go build ./...` and `go vet ./...` succeed on a clean checkout with no network access and
   no module dependencies beyond the standard library (`go.mod` gains no `require` block).
2. `go test ./...` passes, and its cases cover, at minimum: all five `event` values including
   `grafted`; an event with no `role`; an event with no `usage` object; `usage.known == false`;
   `usage.known == true` with `cost_usd` present; `usage.known == true` with `cost_usd`
   absent; a malformed (non-JSON) line; a line missing a required field; and an empty input.
3. Given a fixture log, the tool prints a block containing a per-`node_id` section and a
   per-`role` section, each with the four counts named in the goals.
4. Cost: for a fixture whose known-and-priced events are `0.12` and `0.34`, the printed total
   is `0.4600` and the printed unpriced count is exactly the number of events in that fixture
   that are not known-and-priced.
5. No unpriced event changes the total: adding an event with `usage.known == false` (or with
   no `usage`) to a fixture leaves the printed total figure byte-identical.
6. Determinism: running the tool twice on the same input produces byte-identical stdout, and
   rows appear in a documented sort order rather than Go map order.
7. Accounting reconciles: the block reports lines read, events counted, and lines skipped, and
   a reader can add the reported numbers up to the reported line count for any fixture.
8. Exit status is `0` whenever a summary was printed, and non-zero with a message on stderr
   when the input could not be read at all. A skipped line alone does not fail the run.
9. The usage doc shows a real invocation and its actual output, and states the unpriced-vs-total
   rule. Its commands run as written against the built binary.

## Scope

**In scope — two deliverables**, matching the two-node shape the dispatch calls for. The
split below is offered to `anoikis-planner`; node identity, edges, and gate assignment are
that role's to fix, not this document's.

- *Code deliverable*: the CLI implementation plus its own tests. Input handling, line parsing,
  the counting and cost logic, output rendering, exit codes, and table-driven tests over the
  cases in success criterion 2.
- *Docs deliverable*: a short usage doc — synopsis, one worked example with real output, and
  the unpriced-cost rule. Reconciling the README stub's wording (below) belongs here.

**Out of scope**: everything under non-goals; CI configuration, release packaging, install
scripts, versioning or changelog; and vendoring or code-generating from the run-log schema.

**Known wording drift to settle in the docs deliverable**: the existing `README.md` describes
the output as "per-stage/per-role". The schema has no `stage` field, and this design fixes the
first grouping on `node_id`. Read "stage" as loose prose for `node_id`; the docs deliverable
should align the README with the terms the tool actually prints.

## Key decisions & tradeoffs

1. **Unknown is never zero — and neither is "known but unpriced".** Only events with
   `usage.known == true` *and* `cost_usd` present contribute to the total. Three distinct
   input shapes are therefore all counted as unpriced rather than summed: `usage.known == false`;
   no `usage` object at all; and `usage.known == true` with `cost_usd` absent. The third is a
   judgment call the brief does not cover (see open questions): `known: true` asserts usage was
   measured, but with no number present, adding `0` would understate the total in exactly the way
   this tool exists to prevent. Tradeoff: one count conflates three causes. Accepted for a
   two-node tool; splitting them is a later refinement, and the schema's optional
   `usage.reason` is available if that split is ever wanted.
2. **The counted set is the four events the goal names; `grafted` is not silently dropped.**
   Per-`node_id` and per-`role` rows carry `dispatched`/`complete`/`failed`/`merged`. But
   `grafted` is a real schema value, and its cost still counts toward the total (the sum is over
   *every* known-and-priced event, whatever its `event` value). Dropping it from view would make
   the printed counts fail to reconcile against lines read. Resolution: keep the four columns as
   specified, and report `grafted` in the accounting line (criterion 7). Tradeoff: a fifth column
   would be more uniform but exceeds the stated output shape.
3. **Missing `role` gets an explicit bucket, not exclusion.** `role` is optional in the schema,
   so events without one are grouped under a single reserved, documented label (e.g. `(none)`)
   rather than dropped. Dropping them would break reconciliation; inventing a role would be a
   fabrication. The label must be one that cannot collide with a real role: the schema's `token`
   pattern requires roles to start with an alphanumeric, so a parenthesized label is safe.
4. **Skipped lines are tallied and reported, not fatal and not silent.** A line that is not
   valid JSON, or that lacks a required field, is skipped, counted, and surfaced in the
   accounting line (and noted on stderr). Rationale: a run log is append-only and may be read
   while being written, so a torn final line is expected and must not destroy an otherwise
   useful summary. Tradeoff: a systematically malformed file still exits `0`; criterion 7's
   reconciliation is what makes that visible to the reader.
5. **Deterministic output via explicit sorting.** Go map iteration order is randomized, so rows
   are sorted before printing — `node_id` and `role` ascending by byte order. Cost is accumulated
   in that same sorted order so float64 addition is reproducible run to run. Without this, the
   tool's own tests could not assert on stdout.
6. **Fixed-precision cost formatting.** `cost_usd` is a JSON number (float64). The total is
   printed to four decimal places. Two would round genuinely sub-cent per-node costs to `0.00`;
   four keeps small figures legible without implying more precision than pricing carries.
7. **Stdlib only, no dependencies.** `encoding/json` decoding line by line (`bufio.Scanner`
   with a raised buffer limit, or `json.Decoder` over the stream) into a struct holding just the
   needed fields, with pointer or `*bool`/`*float64` fields where absence must be distinguished
   from a zero value. This is the crux of decision 1: a plain `float64 cost_usd` cannot tell
   `0.0` from absent. Tradeoff: no schema-validation library, so conformance is not checked —
   consistent with the non-goals.
8. **One input, positional path, stdin fallback.** The tool takes an optional path argument and
   reads stdin when the path is absent or `-`. Stdin support keeps it pipeline-friendly and makes
   tests trivial; a single input keeps aggregation semantics unambiguous.
9. **Logic in a testable seam, not welded to `main`.** Parsing/counting/rendering is reachable
   from tests over an `io.Reader` and an `io.Writer`, so tests need not exec a binary or touch
   the filesystem. Whether that seam is a second file in `package main` or a small `internal/`
   package is left to the implementer; a `cmd/` layout is unnecessary for a single binary.

## Constraints & dependencies

- **Language level `go 1.23`** as declared in `go.mod`, while the local toolchain is `go1.26.5`.
  Use no stdlib or language feature newer than 1.23, or the declaration is a lie.
- **Module path** `github.com/dogfood/runlog-stats`; binary name `runlog-stats`.
- **Standard library only.** No `require` entries, no vendor directory, no network at build or
  test time.
- **The input schema is an external dependency and is not vendored here.** It lives in the
  anoikis-tools repo and is not present in this checkout (no `schemas/` directory exists). The
  tool hardcodes the field subset it reads; there is no build-time coupling, and no code
  generation from the schema.
- **Input contract**: one JSON object per line, UTF-8. Fields consumed: `node_id`, `event`,
  `role`, `usage.known`, `usage.cost_usd`. All other schema fields are read past and ignored.
- **Read-only, offline, no side effects** beyond writing stdout/stderr and the exit code.
- **Test fixtures must be self-contained** — inline strings or `testdata/`, never a real
  effort's log from elsewhere on the machine.
- Both deliverables land in this repo; nothing outside it is modified.

## Risks

1. **Schema drift.** A new `event` value, or a change to how unpriced usage is expressed,
   silently lands in the "skipped" or "unpriced" bucket. *Mitigation*: an unrecognized `event`
   value is reported in the accounting line rather than discarded, so drift shows up as a
   nonzero count instead of a wrong summary.
2. **Float precision in the total.** Summing many small `cost_usd` values in float64 accumulates
   error, and a reader may compare the printed total against a hand-computed figure.
   *Mitigation*: fixed sorted summation order (decision 5) and four-decimal formatting
   (decision 6) make the output reproducible; exact decimal arithmetic is out of scope for a
   two-node tool.
3. **The "known but unpriced" reading proves wrong.** If the anoikis convention is that
   `known: true` without `cost_usd` genuinely means zero cost, decision 1 overstates the
   unpriced count. *Mitigation*: it is one localized branch with a dedicated test case; flipping
   it is a small change. Recorded as an open question rather than settled silently.
4. **Long lines.** `bufio.Scanner` defaults to a 64KiB token limit, and events carry free-text
   `detail` (bounded at 500 chars) plus several refs. A long line would surface as a spurious
   skip. *Mitigation*: raise the limit explicitly or stream with `json.Decoder`; cover with a
   long-line test.
5. **Reconciliation arithmetic is easy to get subtly wrong** — an event can be both counted in a
   node row and counted as unpriced, so naive addition double-counts. *Mitigation*: criterion 7
   requires the block to state which numbers sum to the line count, and the docs deliverable
   shows it on a real example.
6. **Scope creep past two deliverables.** JSON output, per-`run_id` grouping, and token
   summaries are each one small step away and all are non-goals. *Mitigation*: the non-goals
   list is explicit; treat any of them as a separate effort.
7. **Docs drifting from behaviour.** The usage doc quotes real output, so any rendering change
   dates it. *Mitigation*: criterion 9 requires the doc's example to match the built binary, so
   review catches drift while both deliverables sit on the same gate.

## Open questions

None of these blocks planning or implementation; each has a stated default that the
implementer should follow unless an operator says otherwise.

1. **`usage.known == true` with `cost_usd` absent** — count as unpriced (default, decision 1),
   or treat as a genuine `0.00` contribution to the total? Confirm against anoikis-tools'
   emitter behaviour if that is cheap to check.
2. **Exact output layout** — section order, column headers, and the wording of the unpriced and
   accounting lines are left to the implementer, constrained only by criteria 3–7. Whatever it
   chooses becomes the contract the docs deliverable quotes.
3. **Label for events with no `role`** — `(none)` is the default; any label that cannot collide
   with a schema-valid `token` is acceptable.
4. **Whether the four-event columns should become five** by promoting `grafted` to a real column.
   Default is no (decision 2), since the goal fixes the four.
5. **Should a systematically unreadable input fail?** Default is exit `0` whenever a summary was
   printed (criterion 8). A threshold — e.g. non-zero when every line was skipped — is plausible
   but unspecified; leave it out unless asked.
6. **README reconciliation depth** — the docs deliverable aligns the "per-stage" wording; whether
   it rewrites the README's status line is the implementer's call.
