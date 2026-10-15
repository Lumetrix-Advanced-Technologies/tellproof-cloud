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
| P1-2 | internal/dsse | done | 2026-10-15 | pass vectors verify; tampered/wrong-key both ErrBadSignature (indistinguishable at this layer, D-014); P-256 DER + Ed25519 signers; Go-signed pack verified by Python and JS |
| P1-3 | internal/merkle | done | 2026-10-15 | disclosed proofs match manifest; Go rebuilds all 3 pass-vector trees (root, salt, exact proof); every leaf of sizes 1–1,000 proves (D-015) |
| P1-4 | internal/pack | done | 2026-10-15 | all 8 manifest cases match exactly; typed predicate round-trips byte-identically; strict statement and JWKS decoding (D-016) |
| P1-5 | cmd/verify | done | 2026-10-15 | pass chain VERIFIED exit 0; every fail vector exit 1 naming its failed checks; --json matches manifest; JWKS file or https URL (D-017) |
| P1-6 | web/verify | done | 2026-10-15 | 44 node tests (manifest + 14 shared extra vectors); renders from files with no network; print repeats digest and share URL on every page (D-018, D-019) |
| C-1 | Concierge ingest | done | 2026-10-15 | OTLP, Langfuse JSON and CSV give identical aggregates; grep finds no prompt text or trace IDs in output; batch-schema metrics; salted commits (D-020) |
| C-2 | Concierge evaluate | done | 2026-10-15 | ten objectives match a hand calculation for a fixture partner month (mixed pass/attention/fail); full Python JCS; objective_set_hash matches the browser JCS (D-021) |
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

- 2026-10-15 · C-2 · PR pending · `evidencepack.py evaluate` computes the ten objectives, coverage and lineage, matching a hand calculation for one partner month.
- 2026-10-15 · C-1 · PR pending · `evidencepack.py ingest` turns OTLP or Langfuse exports into hourly batch-shaped aggregates with no content and salted trace commitments.
- 2026-10-15 · P1-6 · PR pending · Static verify page with a tested browser verifier that agrees with Go on all core and extra vectors; Phase 1 complete.
- 2026-10-15 · P1-5 · PR pending · `plumbline-verify` verifies a chain of packs offline, with human and JSON output and exit codes 0/1/2.
- 2026-10-15 · P1-4 · PR pending · Pack Verify returns signature, subject digest, chain and Merkle results that match every case in manifest.json.
- 2026-10-15 · P1-3 · PR pending · Salted Merkle commitment with odd-node carry-up; Go reproduces the vectors' roots and disclosed proofs exactly.
- 2026-10-15 · P1-2 · PR pending · DSSE PAE, strict envelope decode, P-256 and Ed25519 signers and keyid-based verification; the pass vectors verify with the test key.
- 2026-10-14 · P1-1 · #4 · RFC 8785 serializer and strict parser pass every JCS vector and reproduce the pass-vector payloads byte for byte.
- 2026-10-13 · P0-2 · #2, #3 · Spec files load strictly and cross-check, and breaking any reference fails with a message naming the file, item and value.
- 2026-10-13 · P0-1 · #1 · Repo skeleton in place and `make check` green on a version-printing `cmd/verify`.
