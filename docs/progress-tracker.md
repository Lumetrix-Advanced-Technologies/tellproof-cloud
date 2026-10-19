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
| P3-7 | Disk queue | done | 2026-10-15 | SIGKILLed three times mid-send: no batch lost, none stored twice; crash recovery; backoff; refusals keep the batch (D-031) |
| P3-8 | Dry-run and config | done | 2026-10-15 | `plumbline-agent` enrol/run/version; strict config with env overrides; dry-run prints envelope + payload and touches nothing; replay; end-to-end receive → sign → deliver test (D-032) |
| S-1 | Python SDK core | done | 2026-10-15 | `plumbline.init()`, helpers as context managers/decorators, bounded exporter; shared scenario aggregates identically in agent and concierge; model history per slot and operation (D-033) |
| S-2 | Python auto-instrumentation | done | 2026-10-15 | openai, anthropic, Bedrock (botocore) sync/async/streaming; usage only, billed-input rules per provider; stream proxies record once; real libraries tested in CI (D-034) |
| S-3 | TypeScript SDK core | done | 2026-10-15 | `@plumbline/sdk`: same API as Python with callback scopes over AsyncLocalStorage; one CJS core with ESM re-export; shared scenario aggregates identically (D-035) |
| S-4 | TypeScript auto-instrumentation | done | 2026-10-15 | openai, @anthropic-ai/sdk, Bedrock client (CJS and ESM copies); returns the library's own APIPromise and streams; real libraries tested in CI (D-036) |
| P4-1 | Migrate + store | done | 2026-10-15 | `plumbline-server migrate` (embedded, checksummed, atomic per file, advisory lock); `store.WithTenant` with transaction-local tenant; RLS isolation and append-only proved against Postgres 16 (D-037) |
| P4-2 | Enrol and ingest endpoints | done | 2026-10-16 | `serve` with enrol (atomic token claim) and ingest (verify, row-locked seq/prev, idempotent 200 with stored digest); the real agent delivers to it end to end (D-038) |
| P4-3 | Jobs | done | 2026-10-16 | `plumbline-server work`: SKIP LOCKED claims, retries with backoff, dead jobs, stale-lock recovery, atomic enqueue, daily `prune_jobs` (D-039) |
| P4-4 | Evaluator | done | 2026-10-16 | `internal/evaluate`: Go twin of the concierge evaluator, identical JSON on five fixture sets; region-less usage and `unknown` values handled in both; concierge counter bug fixed (D-040). DB loading arrives with P4-5. |
| P4-5 | Pack builder + KMS + chain | done | 2026-10-16 | `internal/issue` + `internal/kmssign` + migration 0002 (registers, system profile) + `issue_pack` job; 50 concurrent issues form one chain; all three verifiers accept issued packs (D-041) |
| P4-6 | JWKS and rotation | done | 2026-10-16 | nbf/exp/revoked in the JWKS with one rule in all three verifiers (14 new key vectors); `signing_keys` + `keys add/retire/revoke/list`; `/.well-known/jwks.json`; issuer signs only with an in-date registered key (D-042) |
| P4-7 | Daily checkpoint | done | 2026-10-16 | signed, chained daily checkpoints over every pack digest, published to GitHub (never overwritten), empty days and catch-up; `plumbline-verify checkpoint` proves inclusion (D-043) |
| P5-1 | Share links + verify API | done | 2026-10-16 | `internal/share` (hashed tokens, tenant-checked targets, revoke, list with views) + `GET /v1/verify/{token}` (200/404/410) via `verify_lookup`; the browser verifier accepts its responses (D-044) |
| P5-2 | Auth | done | 2026-10-16 | Supabase magic link (server-side PKCE), ES256 access tokens verified with `crypto/ecdsa` against the project JWKS, cookie sessions with refresh, Origin-checked forms, member mapping (D-045) |
| P5-3 | Web app screens | done | 2026-10-16 | Onboarding, systems, setup and sources (tokens, agents, declarations), registers (forms and CSV), packs (issue, list, download), share links, settings; no JS; the partner journey runs end to end in a test (D-046) |
| S-5 | One-command agent install and connect flow | done | 2026-10-16 | `plumbline-agent connect <token>`, early first batch (default 60 s), container image, six static binaries, Setup snippet; a sample app's call reaches the server as a batch in about a second in the test (D-047) |
| S-6 | End to end in CI | done | 2026-10-16 | `e2e/`: Python and Node sample apps → SDKs → agent → server → evaluation → signed pack → share link verified by the verify page in headless Chrome; CI job from a clean database (D-048) |
| P6-1 | Vanta integration | done | 2026-10-16 | Settings connects the vendor's own Vanta private integration (credentials sealed with AES-256-GCM); hourly full-state sync of one resource per objective with a live verify link, packs uploaded once as documents, errors shown in plain words (D-049) |
| P6-2 | Release + open-source split | done | 2026-10-16 | `tools/ossexport` generates the Apache-2.0 public tree from `go list -deps` of the agent and verifier, with a leak check; CI proves it builds and passes its own `make check`; tag-triggered release workflow: 6-platform matrix, checksums, attested binaries and notes, draft release; image and SDK publishing behind approval. Nothing published yet (D-050) |
| P6-3 | Launch checklist | done | 2026-10-16 | Security page and agent docs page on the static site (no JS, tested links and CSP), a verifier-writer's guide to the published vectors, the Show HN draft and the launch checklist of human steps; nothing posted or published (D-051) |
| P6-4 | Local mode, early access, public repository | done | 2026-10-16 | `plumbline-agent local` + `report`; early-access issue form; SDKs renamed `plumbline-sdk`; this repo renamed `plumbline-cloud`; public `plumbline` repo created from the export with v0.1.0 released (attested binaries). Waiting on founders: make the ghcr image public, PyPI/npm publishing (D-052) |
| P6-5 | Local dashboard | done | 2026-10-16 | `run --local` serves a dashboard on 127.0.0.1:4319 from the verified log: tiles, calls per model, coverage and tokens over time, models table, timeline, evidence chain; plain HTML/SVG, loopback-only, strict CSP (D-053) |
| P6-6 | Agent from pip and npm | done | 2026-10-16 | `pip install plumbline-agent`, `npx plumbline-agent`: platform wheels and npm packages around the release binary, no install scripts; built and attested in the release, published once the founders set up PyPI and npm (D-054) |
| P6-7 | Governance as code | done | 2026-10-17 | `plumbline.policy.json` (schema, `policy init`/`validate`), `check` with the pack evaluator (now open source) and JUnit/JSON, the dashboard's Evidence objectives panel, `aibom` (CycloneDX 1.6), a GitHub Action (D-055) |
| P7-1 | Rename to Witnessmark | done | 2026-10-17 | Every name, package and page is Witnessmark with a new mark; v0.2 names (env, attributes, policy file, data dir) still read, v0.2 SDK exports kept as fixtures; wire formats, key bindings and DB names frozen. Repos, domains and package publishing wait on the founders (D-056) |
| P7-2 | Falsifiable evidence | done | 2026-10-17 | Guardrail canaries in both SDKs (probes stay in the app; kind and caught only), batch v0.3 only for windows with canaries, EO-CAN-01 in both evaluators, `check --self-test` (each objective must fail on injected violations; vacuous registers flagged; tampering detected), report, dashboard tile and Action (D-057) |
| P7-3 | Coding-agent evidence | done | 2026-10-17 | `witnessmark-agent ci run/canary/attest/verify/keygen`: gate outcomes per commit (no logs, code or command lines), must-fail canaries on a patched worktree, signed in-toto statements (or GitHub attestations) with coding-agent commit counts, `witnessmark-verify ci`, policy `ci` section (D-058) |
| P7-4 | Rename to Tellproof | done | 2026-10-17 | Witnessmark collided with a governance advisory (witnessmark.co) and its domains were taken; renamed to Tellproof before any public release, with no Witnessmark aliases (never shipped); v0.2 compatibility unchanged (D-059) |
| P7-5 | npm CI/CD and the welcome banner | done | 2026-10-19 | npm publishing back in the release and SDK workflows with trusted publishing, smoke tests of the built pip and npm packages before publishing; a welcome banner for interactive terminals; README npm section (D-061) |
| P8-1 | Workflow hardening, Scorecard and community files | done | 2026-10-19 | Every action in both repos pinned to a commit SHA (tag in a comment) and checked by a test; `persist-credentials: false`; per-job permissions; Dependabot; Scorecard workflow and badge; CONTRIBUTING, CODE_OF_CONDUCT, PR template, CODEOWNERS (D-063) |
| P8-2 | `init`, `doctor` and `demo` | done | 2026-10-19 | init detects Python/Node (CJS or ESM) and the entry file, prints the SDK line, writes the policy, offers .gitignore, starts local mode; doctor: 12 checks with fixes, probe sends no spans, `--json`; demo: synthetic week + policy (6 pass, 4 fail, 1 attention), `internal/demolog` shared with tools/demolog (D-064) |
| P8-3 | Content-leak regression suite | done | 2026-10-19 | `spec/leak-corpus.json` (16 markers, 35 content spellings, 16 read attributes, trace IDs) through the agent end to end, Langfuse, and both SDKs; found that read attributes (model, provider, region, prompt version, canary kind) carried any text into batches: now identifiers only (`spec/identifiers.json`, Go and Python agree on shared vectors), else `invalid`/`other` (D-065); `make leak-test` |
| P8-3a | Content spellings counted | done | 2026-10-19 | Semconv map v4: OpenInference, Traceloop, Langfuse, Vercel AI SDK and newer GenAI prompt/completion names count towards `content_present_spans` (still never read); tool definitions, prompt templates and generic OTel fields deliberately not (corpus `uncounted_content_attributes`); leak test requires every span with a counted spelling to count; agent/concierge parity over every spelling (D-066) |
| P8-7 | One install and the background agent | in review | 2026-10-19 | `pip install "tellproof[agent]"` / `npm install tellproof` (agent as an extra / optional dependency, never imported by the SDK) and a `tellproof` command in both SDKs; `start`, `stop`, `restart`, `status`, `logs`; a run record and a stop file in the data directory (no signals, no endpoint); init starts in the background; SDKs 0.3.0 (D-067) |
| P8-8 | README split | in review | 2026-10-19 | `README.md` keeps install, setup and usage (one-install quick start, start/stop, CLI reference); `HOW-IT-WORKS.md` takes the use cases, the parts, the path from a model call to a signed batch, what leaves the machine, the data directory, objectives, canaries and self-test, coding-agent evidence, packs, verification, security model and wire formats; every old line accounted for, every link and anchor checked; site install copy updated |
| P8-9 | Security hardening of what adopters run | in review | 2026-10-19 | Review of every surface an adopter takes on; fixed: receiver Host check on loopback (DNS rebinding could post fake spans), launchers never run an agent from the working directory, SDK transport follows no redirect and takes http(s) only (Node re-posted on 307/308), Langfuse URL must be https, the Action installs its own release by default (checked at release); `oss/docs/threat-model.md` (D-068) |

Status values: `not started`, `in progress`, `done`, `blocked (reason)`.

## Log

Newest first. One line per finished task: date, task, PR, one-sentence outcome.

- 2026-10-19 · P8-9 · PR pending · A web page can no longer post spans into a developer's evidence, a cloned repository cannot plant the agent the launcher runs, spans never follow a redirect, and every surface an adopter takes on is in a published threat model.
- 2026-10-19 · P8-8 · PR pending · The README is now the setup and usage guide, and how Tellproof works has its own document.
- 2026-10-19 · P8-7 · PR pending · One install gives the SDK, the agent and a `tellproof` command, and the local agent starts and stops in the background like any dev server.
- 2026-10-19 · P8-3a · PR #77 merged · EO-CNT-01 now sees prompts and completions exported under OpenInference, Traceloop, Langfuse and Vercel AI SDK names, so an app that exports content can no longer pass it by using another library.
- 2026-10-19 · P8-3 · PR #74 merged · A shared corpus of prompts, personal data and keys in every position proves nothing of it leaves the agent or the SDKs, and closed the one gap it found: text in a model or region attribute.
- 2026-10-19 · P8-2 · PR #73 merged · `tellproof-agent init` takes an app from nothing to a batch on the dashboard, `doctor` explains what is wrong and how to fix it, and `demo` shows a full dashboard in seconds.
- 2026-10-19 · P8-1 · PR #72 merged · Every workflow action is pinned to a reviewed commit, checkouts keep no token, and the public repo has a Scorecard, contribution guide and code of conduct.
- 2026-10-19 · P7-5 · v0.4.0 released (GitHub, Docker, PyPI; npm staged by CI for approval) · npm releases are automated again behind approval, and the agent greets people in the terminal.
- 2026-10-18 · P7-4 · All eight npm packages published by hand through staged publishing: `npx tellproof-agent` and `npm install tellproof` work.
- 2026-10-17 · P7-4 · npm automation removed; the founders publish npm packages by hand once npm lifts the rate limit (D-060).
- 2026-10-17 · P7-4 · SDK 0.2.0 on PyPI; npm rate-limits new packages, so a temporary npm-first-publish workflow publishes one every three hours until all eight are up.
- 2026-10-17 · P7-4 · PR #59 merged; v0.3.0 released; site on GitHub Pages · The product is Tellproof; the name check that missed a governance advisory called WitnessMark was redone before anything shipped.
- 2026-10-17 · P7-3 · PR #57 merged · Any commit, human or agent written, can carry signed evidence that its CI gates ran, tested something, and could have failed.
- 2026-10-17 · P7-2 · PR #56 merged · Evidence can now be shown to be able to fail: canaries prove the guardrail catches known-bad input, and the self-test proves every objective and the log's integrity respond to violations.
- 2026-10-17 · P7-1 · PR #55 merged · Plumbline is now Witnessmark, and nothing set up under the old name breaks.
- 2026-10-17 · P6-7 · PR #54 merged · Teams can declare an AI system in a policy file and check its evidence against the ten objectives locally and in CI, with the same evaluator as signed packs.
- 2026-10-16 · P6-6 · PR pending · The agent installs with pip or npx and runs with one command.
- 2026-10-16 · P6-5 · PR pending · Local mode has a dashboard that shows what an AI system did, hour by hour, from its signed log.
- 2026-10-16 · P6-4 · PR pending · Plumbline is open source: anyone can run the agent locally with one line of app code, and v0.1.0 is released with signed provenance.
- 2026-10-16 · P6-3 · PR pending · Everything a launch needs is drafted; what is left needs a person, an account or a decision (docs/launch/checklist.md).
- 2026-10-16 · P6-2 · PR pending · The open-source agent and verifier can be split out and released with signed provenance by pushing a tag; nothing is published yet.
- 2026-10-16 · P6-1 · PR pending · A vendor's Vanta shows every objective's latest status with a link that verifies the pack, updated hourly.
- 2026-10-16 · S-6 · PR pending · The whole product runs end to end in CI, down to a reviewer's browser saying "Verified".
- 2026-10-16 · S-5 · PR pending · One docker run line and one line of app code connect a system; the first batch arrives in about a minute.
- 2026-10-16 · P5-3 · PR pending · A vendor can go from sign-up to a shared, verified pack using only the app's pages.
- 2026-10-16 · P5-2 · PR pending · Vendors sign in with a Supabase magic link and every request is tied to their organisation.
- 2026-10-16 · P5-1 · PR pending · A vendor's share link lets a bank open and verify a pack in the browser, and the vendor sees who looked.
- 2026-10-16 · P4-7 · PR pending · A public, append-only daily log proves which packs existed by when.
- 2026-10-16 · P4-6 · PR pending · Signing keys can be rotated and revoked, and every verifier agrees which packs a key still vouches for.
- 2026-10-16 · P4-5 · PR pending · The control plane issues signed, chained packs from stored evidence that every verifier accepts.
- 2026-10-16 · P4-4 · PR pending · The control plane evaluates the ten objectives exactly as the concierge does, including on agent-collected data.
- 2026-10-16 · P4-3 · PR pending · A shared job queue: any number of workers, no job run twice, failures retried then parked.
- 2026-10-16 · P4-2 · PR pending · Agents can enrol with the control plane and their signed batches are verified and stored exactly once.
- 2026-10-15 · P4-1 · PR pending · The control plane can migrate a fresh database and every app query is confined to one tenant.
- 2026-10-15 · S-4 · PR pending · Node apps now get every OpenAI, Anthropic and Bedrock call recorded with billed tokens from `plumbline.init()`, without content.
- 2026-10-15 · S-3 · PR pending · `npm install @plumbline/sdk` + one line gives Node apps the same evidence helpers, with no dependencies.
- 2026-10-15 · S-2 · PR pending · `plumbline.init()` now records every OpenAI, Anthropic and Bedrock call with its billed tokens, without reading content.
- 2026-10-15 · S-1 · PR pending · `pip install plumbline` + one line sends evidence spans to the agent, with no dependencies and no content.
- 2026-10-15 · P3-8 · PR pending · The agent runs end to end: `make agent-dry` shows exactly what would leave, and a live run delivers verified batches.
- 2026-10-15 · P3-7 · PR pending · A durable outbox that survives kill -9 mid-send without losing or duplicating a batch.
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
