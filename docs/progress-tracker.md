# Progress tracker

Claude Code updates this file at the end of every task. Humans update the milestone lines.

## Milestones

- M1: PENDING (target 22 Nov 2026). Platform phases 3–6 stay blocked until a founder changes this line to `M1: PASSED`.
- M2: PENDING (target 20 Dec 2026)
- M3: PENDING (target 31 Jan 2027)

## Tasks

| Task | Title | Status | Updated | Notes |
| --- | --- | --- | --- | --- |
| P0-1 | Repo skeleton | done | 2026-10-13 | `cmd/verify` prints its version; Go 1.26 min / CI 1.27, tool pins bumped (D-011); folder placeholders |
| P0-2 | Spec checks | done | 2026-10-13 | `internal/spec.Check`; 18 tests incl. rule shapes, title cap, presence-only exclusivity (D-012) |
| P1-1 | internal/jcs | done | 2026-10-14 | 8/8 JCS vectors, 3/3 must-reject, pass payloads byte-identical; numbers cross-checked vs Node on 3,119 doubles; fuzzed (D-013) |
| P1-2 | internal/dsse | not started | | |
| P1-3 | internal/merkle | not started | | |
| P1-4 | internal/pack | not started | | |
| P1-5 | cmd/verify | not started | | |
| P1-6 | web/verify | not started | | |
| C-1 | Concierge ingest | not started | | |
| C-2 | Concierge evaluate | not started | | |
| C-3 | Concierge sign and chain | not started | | |
| C-4 | Concierge share | not started | | |
| P3-1 | OTLP JSON receiver + normaliser | blocked (M1) | | |
| P3-2 | Hourly aggregator | blocked (M1) | | |
| P3-3 | Commitments | blocked (M1) | | |
| P3-4 | Provider usage | blocked (M1) | | |
| P3-5 | Langfuse puller | blocked (M1) | | |
| P3-6 | Enrolment and batch signing | blocked (M1) | | |
| P3-7 | Disk queue | blocked (M1) | | |
| P3-8 | Dry-run and config | blocked (M1) | | |
| P4-1 | Migrate + store | blocked (M1) | | |
| P4-2 | Enrol and ingest endpoints | blocked (M1) | | |
| P4-3 | Jobs | blocked (M1) | | |
| P4-4 | Evaluator | blocked (M1) | | |
| P4-5 | Pack builder + KMS + chain | blocked (M1) | | |
| P4-6 | JWKS and rotation | blocked (M1) | | |
| P4-7 | Daily checkpoint | blocked (M1) | | |
| P5-1 | Share links + verify API | blocked (M1) | | |
| P5-2 | Auth | blocked (M1) | | |
| P5-3 | Web app screens | blocked (M1) | | |
| P6-1 | Vanta integration | blocked (M1) | | |
| P6-2 | Release + open-source split | blocked (M1) | | |
| P6-3 | Launch checklist | blocked (M1) | | |

Status values: `not started`, `in progress`, `done`, `blocked (reason)`.

## Log

Newest first. One line per finished task: date, task, PR, one-sentence outcome.

- 2026-10-14 · P1-1 · PR pending · RFC 8785 serializer and strict parser pass every JCS vector and reproduce the pass-vector payloads byte for byte.
- 2026-10-13 · P0-2 · PR pending · Spec files load strictly and cross-check, and breaking any reference fails with a message naming the file, item and value.
- 2026-10-13 · P0-1 · PR pending · Repo skeleton in place and `make check` green on a version-printing `cmd/verify`.
