# runlog-stats usage

`runlog-stats` reads one anoikis effort's `run-log.jsonl` — one JSON object per
line — and prints a plain-text summary to stdout: per-`node_id` and per-role
event counts, plus a total cost figure that only ever includes priced usage.

## Synopsis

```
runlog-stats [path]
```

- `path` is optional. If it is absent or `-`, the tool reads stdin.
- Exactly one input is read per invocation; there is no per-`run_id` grouping,
  and no support for reading multiple files.
- Output is a single plain-text block on stdout. There is no JSON or CSV
  output mode, and no token or timing figures are reported — the tool
  summarizes event counts and cost only.
- Exit status is `0` whenever a summary was printed. It is non-zero, with a
  message on stderr, only when the input itself could not be read (e.g. the
  given path does not exist). A malformed or incomplete line inside an
  otherwise-readable input does not fail the run — it is skipped and counted
  (see Accounting below).

## Building

From the repository root:

```
$ go build -o runlog-stats .
```

This builds only the root `main` package (`.`), not `./...` — the module also
contains `internal/runlogstats`, and `go build -o runlog-stats ./...` fails
with `go: cannot write multiple packages to non-directory runlog-stats`
because `-o` can't name a single file for more than one build target.

## Worked example

Given `testdata/fixture.jsonl` in this repo, and the `runlog-stats` binary
built above on `$PATH` (or invoked as `./runlog-stats`):

```
{"node_id":"node-a","event":"dispatched","role":"builder","usage":{"known":true,"cost_usd":0.12}}
{"node_id":"node-a","event":"complete","role":"builder","usage":{"known":true,"cost_usd":0.34}}
{"node_id":"node-a","event":"failed","role":"builder","usage":{"known":false}}
{"node_id":"node-b","event":"dispatched","role":"tester"}
{"node_id":"node-b","event":"complete","role":"tester","usage":{"known":true}}
{"node_id":"node-b","event":"merged","usage":{"known":false}}
{"node_id":"node-a","event":"grafted","role":"builder","usage":{"known":false}}
this is not json
{"event":"dispatched","role":"builder"}
```

Running the built binary against it:

```
$ runlog-stats testdata/fixture.jsonl
=== Per-node_id ===
node_id	dispatched	complete	failed	merged
node-a	1	1	1	0
node-b	1	1	0	1

=== Per-role ===
role	dispatched	complete	failed	merged
(none)	0	0	0	1
builder	1	1	1	0
tester	1	1	0	0

=== Cost ===
total_cost_usd: 0.4600
unpriced_events: 5
note: only usage.known == true events with cost_usd present are summed; all other events (no usage, known == false, known == true without cost_usd) count as unpriced, never as 0

=== Accounting ===
lines_read: 9
events_counted: 7
lines_skipped: 2
note: lines_read = events_counted + lines_skipped
events_counted_by_kind: complete=2 dispatched=2 failed=1 grafted=1 merged=1
note: events_counted = sum of events_counted_by_kind values
```

(The tool also writes `runlog-stats: skipped 2 unparsable line(s)` to stderr
and exits `0`, since a skipped line does not fail the run.)

## The unpriced-vs-total rule

Only an event with `usage.known == true` **and** `cost_usd` present
contributes to `total_cost_usd`. Three distinct input shapes are all counted
as unpriced instead, and never added into the total as `0`:

1. no `usage` object at all,
2. `usage.known == false`, or
3. `usage.known == true` with `cost_usd` absent.

In the fixture above, the five unpriced events are: `node-a failed`
(`known: false`), `node-b dispatched` (no `usage`), `node-b complete`
(`known: true`, no `cost_usd`), `node-b merged` (`known: false`), and the
`grafted` event (`known: false`). The two priced events are `node-a
dispatched` (`0.12`) and `node-a complete` (`0.34`), summing to the printed
`0.4600`.

## Reading the accounting line

`lines_read` is every line the scanner saw, including blank lines — a blank
line is not valid JSON, so it counts as skipped rather than being filtered
out beforehand. Every line falls into exactly one of two buckets:
`lines_skipped` (blank, not valid JSON, or missing `node_id`/`event`) or
`events_counted` (successfully parsed). So:

```
lines_read = events_counted + lines_skipped
```

In the example: `9 = 7 + 2`.

`events_counted_by_kind` breaks `events_counted` down by every `event` value
actually seen — including `grafted`, which is not one of the four columns in
the per-`node_id`/per-role tables but is still a real, counted event (its cost
still contributes to `total_cost_usd` when priced). So:

```
events_counted = sum of events_counted_by_kind values
```

In the example: `7 = 2 (complete) + 2 (dispatched) + 1 (failed) + 1 (grafted) + 1 (merged)`.

**The per-`node_id`/per-role counts and `unpriced_events` are not additive
with each other or with the accounting line.** An event is counted once in
its node row, once in its role row, once in `events_counted_by_kind`, and
*separately* counted in `unpriced_events` if it lacks known-and-priced usage
— the same event contributes to more than one of these figures at once. For
example, `node-a failed` is counted once in the `node-a` row, once in the
`builder` role row, once under `failed` in `events_counted_by_kind`, and once
in `unpriced_events`. Do not sum counts across sections expecting them to
reconcile to `lines_read` or to each other; only the two equalities above
(`lines_read = events_counted + lines_skipped` and `events_counted = sum of
events_counted_by_kind`) hold.

## Row sort order and the no-`role` label

Rows in both the per-`node_id` and per-role tables are sorted ascending by
byte order (plain string sort) on the row key — `node_id` in the first table,
`role` in the second — never by Go map iteration order, so repeated runs on
the same input produce byte-identical output.

An event that carries no `role` field is grouped under the reserved label
`(none)`, shown as its own row rather than being dropped or assigned a
fabricated role. `(none)` sorts among the other role rows by the same byte
ordering as any other label.

## What this tool does not do

- It summarizes exactly one `run-log.jsonl` per invocation. It does not group
  or split output by `run_id`, even when the input contains several.
- It has no JSON or CSV output mode — only the plain-text block shown above.
- It does not report token counts, cache token figures, model, context
  window, or any timing/duration figures. Cost is the only usage figure
  summarized.
- It does not validate its input against the run-log-event JSON Schema; it
  reads only the fields it needs (`node_id`, `event`, `role`, `usage.known`,
  `usage.cost_usd`) and ignores everything else.
- It reads exactly one input (a path argument, or stdin). It does not glob,
  walk directories, or read multiple files in one invocation.
