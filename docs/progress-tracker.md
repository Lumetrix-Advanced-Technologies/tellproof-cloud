# Progress tracker

Claude Code updates this file at the end of every task. Humans update the milestone lines.

## Milestones

- M1: PENDING (target 22 Nov 2026). Since D-024 it no longer blocks Phases 3–6: the whole product is built first and M1 is the goal of the trial runs. Only a founder changes this line, and only to `M1: PASSED` when its criteria are met.
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
| C-3 | Concierge sign and chain | done | 2026-10-15 | a two-pack chain passes verify.py, cmd/verify and the verify page's verifier; local key or KMS by digest; schema-checked and self-verified (D-022) |
| C-4 | Concierge share | done | 2026-10-15 | headless Chrome opens a share link and sees Verified (chain checked on pack 2); revoked link shows no pack; tokens never logged (D-023) |
| P3-0 | Batch schema v0.2 | done | 2026-10-15 | `observed` (live models and model + prompt versions) beside metrics; v0.1 kept; concierge windows validate |
| P3-1 | OTLP JSON receiver + normaliser | done | 2026-10-15 | collector-style and OpenLLMetry/JS fixtures parse; no-content fuzz (198k execs); gzip and limits; semconv v2 (D-026) |
| P3-2 | Hourly aggregator | done | 2026-10-15 | windows match the concierge's C-1 output exactly (Python run in the test); late spans, grace, transitions, anomalies |
| P3-3 | Commitments | done | 2026-10-15 | disclosed traces verify against their window root; roots equal the concierge's; period root over window roots (D-027) |
| P3-4 | Provider usage | done | 2026-10-15 | OpenAI, Anthropic (schemas checked today), Bedrock behind tag; coverage within 0.1 point of a hand calculation (D-028) |
| P3-5 | Langfuse puller | done | 2026-10-15 | recorded v2/v3 pages aggregate identically to the OTLP path; evals from scores; backoff (D-029) |
| P3-6 | Enrolment and batch signing | done | 2026-10-15 | Ed25519 identity (0600), enrol over https; canonical v0.2 batches signed and chained by seq/prev; server-side Verify (D-030) |
| P3-7 | Disk queue | not started | | |
| P3-8 | Dry-run and config | not started | | |
| S-1 | Python SDK core | not started | | |
| S-2 | Python auto-instrumentation | not started | | |
| S-3 | TypeScript SDK core | not started | | |
| S-4 | TypeScript auto-instrumentation | not started | | |
| P4-1 | Migrate + store | not started | | |
| P4-2 | Enrol and ingest endpoints | not started | | |
| P4-3 | Jobs | not started | | |
| P4-4 | Evaluator | not started | | |
| P4-5 | Pack builder + KMS + chain | not started | | |
| P4-6 | JWKS and rotation | not started | | |
| P4-7 | Daily checkpoint | not started | | |
| P5-1 | Share links + verify API | not started | | |
| P5-2 | Auth | not started | | |
| P5-3 | Web app screens | not started | | |
| S-5 | One-command agent install and connect flow | not started | | |
| S-6 | End to end in CI | not started | | |
| P6-1 | Vanta integration | not started | | |
| P6-2 | Release + open-source split | not started | | |
| P6-3 | Launch checklist | not started | | |

Status values: `not started`, `in progress`, `done`, `blocked (reason)`.

## Log

Newest first. One line per finished task: date, task, PR, one-sentence outcome.

- 2026-10-15 · P3-6 · PR pending · The agent enrols with a one-time token and signs chained, canonical batches the server can verify.
- 2026-10-15 · P3-5 · PR pending · Langfuse observations and scores feed the agent, aggregating exactly as OTLP spans do.
- 2026-10-15 · P3-4 · PR pending · Hourly billed usage from OpenAI, Anthropic and Bedrock, reconciled against traced tokens per provider.
- 2026-10-15 · P3-3 · PR pending · Salted per-window commitments and period proofs through the window root.
- 2026-10-15 · P3-2 · PR pending · The agent aggregates spans into hourly batch metrics identical to the concierge's for the same input.
- 2026-10-15 · P3-1 · PR pending · The agent's OTLP JSON receiver normalises spans from any SDK to content-free facts.
- 2026-10-15 · P3-0 · PR pending · Batch schema v0.2 carries the live model and prompt versions eval freshness needs.
- 2026-10-15 · D-024, D-025 · PR pending · Build the whole product before trials; one-line SDK integration (Python, TypeScript) feeding the local agent.
- 2026-10-15 · Docs · #16 · README rewritten for setup, usage and architecture; technical and executive whitepapers written as shared docs.
- 2026-10-15 · C-4 · #13, #17 · `evidencepack.py share` publishes verified packs on a static site; a verifier opening the link sees Verified. Phase C complete; Phases 0, 1 and C are done and the M1 gate is next.
- 2026-10-15 · C-3 · #12 · `evidencepack.py sign` issues chained, signed packs that all three verifiers accept.
- 2026-10-15 · C-2 · #11 · `evidencepack.py evaluate` computes the ten objectives, coverage and lineage, matching a hand calculation for one partner month.
- 2026-10-15 · C-1 · #10 · `evidencepack.py ingest` turns OTLP or Langfuse exports into hourly batch-shaped aggregates with no content and salted trace commitments.
- 2026-10-15 · P1-6 · #9 · Static verify page with a tested browser verifier that agrees with Go on all core and extra vectors; Phase 1 complete.
- 2026-10-15 · P1-5 · #8 · `plumbline-verify` verifies a chain of packs offline, with human and JSON output and exit codes 0/1/2.
- 2026-10-15 · P1-4 · #7 · Pack Verify returns signature, subject digest, chain and Merkle results that match every case in manifest.json.
- 2026-10-15 · P1-3 · #6, #15 · Salted Merkle commitment with odd-node carry-up; Go reproduces the vectors' roots and disclosed proofs exactly.
- 2026-10-15 · P1-2 · #5 · DSSE PAE, strict envelope decode, P-256 and Ed25519 signers and keyid-based verification; the pass vectors verify with the test key.
- 2026-10-14 · P1-1 · #4 · RFC 8785 serializer and strict parser pass every JCS vector and reproduce the pass-vector payloads byte for byte.
- 2026-10-13 · P0-2 · #2, #3 · Spec files load strictly and cross-check, and breaking any reference fails with a message naming the file, item and value.
- 2026-10-13 · P0-1 · #1 · Repo skeleton in place and `make check` green on a version-printing `cmd/verify`.
