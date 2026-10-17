# Build plan

Tasks are sized for one PR each (roughly 2–6 hours with Claude Code). Do them in order within a phase unless "Depends on" says otherwise. Every task lists the tests that prove it is done.

**Order (D-024):** the founders chose to build the whole product before trial runs, so the Milestone 1 gate no longer blocks Phases 3–6; M1's criteria are the goal of the trial runs instead. Work in this order: Phase 3 (agent), Phase S (SDKs), Phase 4 (control plane), Phase 5 (sharing and web app), S-5 and S-6 (one-command install, end to end), Phase 6 (integrations and launch).

---

## Phase 0: Bootstrap (allowed now)

**P0-1 Repo skeleton.** `go.mod` (module `github.com/lumetrix/tellproof`, rename if the org differs), folders from CLAUDE.md, `Makefile` targets, `docker-compose.yml`, `.github/workflows/ci.yml`, `.gitignore`.
Done when: `make check` runs green on an empty `cmd/verify/main.go` that prints its version; CI runs on every PR.

**P0-2 Spec checks.** A Go test in `internal/spec` that loads `spec/objectives.json`, `spec/framework-refs.json` and `spec/semconv-map.json` with `encoding/json` (`DisallowUnknownFields`) and validates that every objective's `maps_to` entries exist in `framework-refs.json` and every rule kind is one of the five.
Done when: the test passes, and breaking any reference fails it with a clear message.

## Phase 1: Trust core (allowed now; these packages are open source)

**P1-1 `internal/jcs`.** RFC 8785 serializer and a strict parser (duplicate keys rejected, numbers as float64). Rules: `architecture.md` §2.1.
Done when: every case in `spec/jcs-vectors.json` passes; a fuzz test round-trips parse → serialize → parse; the three pass vectors' payloads re-serialize byte-identically.
Depends on: P0-1.

**P1-2 `internal/dsse`.** PAE, envelope encode and decode, a `Signer` interface (`Sign(ctx, pae []byte) (sig []byte, keyID string, err error)`) with implementations: local ECDSA P-256 (tests and dev), local Ed25519 (agent). Verification for P-256 (DER signatures) and Ed25519.
Done when: the pass vectors verify with `spec/vectors/test-public-key.pem`; `fail/tampered-payload` and `fail/wrong-key` fail with distinct errors.
Depends on: P1-1.

**P1-3 `internal/merkle`.** Build root, produce proof, verify proof, exactly as `architecture.md` §2.4 (odd node carried up).
Done when: the disclosed proofs in all pass vectors verify; `fail/bad-merkle-proof` fails; property test that every leaf of random trees of size 1–1,000 proves to the root.

**P1-4 `internal/pack`.** Statement and predicate types, `Digest(envelope)`, `CheckSubjectDigest`, `CheckChain(prev, next)`, and `Verify(envelope, jwks, prev)` that runs all checks and returns a structured result (`signature`, `subject_digest`, `chain`, `merkle`).
Done when: results for every file in `spec/vectors/manifest.json` match its expected result exactly.
Depends on: P1-1 to P1-3.

**P1-5 `cmd/verify`.** CLI: `tellproof-verify --jwks <file|url> pack1.json [pack2.json …]` (oldest first), human output by default, `--json` for machines, exit code 0 only when everything verifies.
Done when: running it over `spec/vectors/pass/*` prints VERIFIED and exits 0; each fail vector exits 1 with the right failed check named.

**P1-6 `web/verify/`.** Port `reference/sample-verify-page.html` into `index.html`, `verify.js` (pure functions exported as an ES module: `jcs`, `pae`, `derToRaw`, `verifyEnvelope`, `verifyMerkle`) and `verify.css` using `web/brand.css` tokens. Loads the envelope from `?t=` via `GET /v1/verify/{token}` or from a local file picker (offline mode). Print stylesheet included.
Done when: `node --test web/verify/verify.test.js` passes the whole vector manifest; the page renders the pass vectors from a file with no network; printing gives a clean PDF with the digest and share URL on every page.
Depends on: P1-4 (for parity checks).

## Phase C: Concierge (allowed now; Python; frozen after M1)

**C-1 Ingest.** `concierge/evidencepack.py ingest` reads a Langfuse export (JSON or CSV) or an OTLP JSON dump and writes hourly aggregates in the batch `metrics` shape (`spec/batch.schema.json`), dropping all message content.
Done when: a sample export produces aggregates; a grep for any prompt text in the output finds nothing.

**C-2 Evaluate.** Compute the ten objectives from aggregates plus provider usage CSVs, using the rules in `spec/objectives.json`.
Done when: output matches a hand calculation for one design partner's month.

**C-3 Sign and chain.** Build the predicate, JCS, statement, PAE; sign with KMS via `boto3` (`MessageType='DIGEST'`, `ECDSA_SHA_256`) or a local key; set `seq_no` and `prev_pack_digest` from the previous pack file.
Done when: `verify.py`, `cmd/verify` (once P1-5 exists) and the verify page all pass its output. Note: `concierge/verify.py`'s float handling only covers simple decimals; prefer integers and one-decimal percentages in concierge packs.

**C-4 Share.** Publish the envelope as a static file next to the verify page with an unguessable name, and record per-verifier links in the CRM sheet.
Done when: a verifier can open a link and see "Verified".

## Milestone 1 (22 Nov 2026): the goal of the trial runs (D-024)

## Phase 3: Evidence agent

**P3-0 Batch schema v0.2.** `spec/batch.schema.json` gains an `observed` object beside `metrics` (models and model + prompt version pairs live in the window), so the agent can support EO-EVAL-01 (D-021 gap). `schema` const becomes `plumbline-batch/v0.2`; v0.1 batches stay valid for ingest. `internal/spec` checks it.
Done when: the concierge aggregates' windows validate against v0.2; a v0.1 example still validates against the v0.1 file kept as `spec/batch-v0.1.schema.json`.

**P3-1 OTLP JSON receiver + normaliser.** `internal/otlp`; `spec/semconv-map.json`; drops content attributes at parse.
Done when: fixture exports from two SDKs parse; the content fuzz test passes; gzip bodies accepted.

**P3-2 Hourly aggregator.** Windows, late-span handling, all `metrics` fields.
Done when: aggregates for the fixture equal the concierge's C-1 output.

**P3-3 Commitments.** Salted Merkle root per window (`internal/merkle`), salt secret from env or file.
Done when: a disclosed trace from a window verifies against that window's root.

**P3-4 Provider usage.** OpenAI and Anthropic pollers (`net/http`); Bedrock behind tag `bedrock`.
Done when: against recorded API responses in `testdata/`, coverage matches a hand calculation within 0.1 point.

**P3-5 Langfuse puller.** Observations v2 and Scores v3 with cursor paging and backoff.
Done when: recorded responses produce the same aggregates as the OTLP path for the same traces.

**P3-6 Enrolment and batch signing.** `agent enrol`, Ed25519 key, DSSE batch envelopes with `seq` and `prev`.

**P3-7 Disk queue.** Append-only segments, fsync, retry with backoff, delete on 201.
Done when: killing the agent mid-send and restarting loses no batch and sends none twice (server dedupes by `(agent_id, seq)`).

**P3-8 `--dry-run` and config.** Prints envelopes; JSON config file plus env overrides.

## Phase S: SDKs and one-line integration (D-025)

Zero-dependency SDKs that make adoption one line of code, like Sentry: `tellproof.init()` instruments the LLM clients an app already uses and streams spans (OTLP JSON, no content) to the local evidence agent. The SDKs never capture prompt or completion text.

**S-1 Python SDK core (`sdk/python`, package `tellproof`).** `tellproof.init(agent_url=None, service=None)` (default `http://127.0.0.1:4318`, env `TELLPROOF_AGENT_URL`); a background exporter that batches spans as OTLP JSON and never blocks or raises into the app; manual helpers for the D-020 attributes: `tellproof.prompt(name, version)`, `tellproof.guardrail(decision)`, `tellproof.personal_data(redacted=…)`, `tellproof.tool(name, high_impact=…, approved=…)`, `tellproof.escalation()`, `tellproof.agent_run(name)`. Standard library only.
Done when: spans the SDK exports parse in `internal/otlp` (shared fixture) and aggregate to the expected counts; the exporter drops on overload instead of blocking; a fuzz-style test proves no argument text reaches an exported span.

**S-2 Python auto-instrumentation.** `init()` patches, when installed: `openai` (chat completions, responses, embeddings; sync, async and streaming), `anthropic` (messages; sync, async, streaming), `boto3` Bedrock Runtime (`converse`, `invoke_model`). Each call becomes a span with provider, model, region, token usage, error status; message content is never read.
Done when: against fake clients with the real libraries' call shapes (and the real libraries when installed), every call type yields the right span; streaming usage is counted once; `tellproof.shutdown()` flushes.

**S-3 TypeScript SDK core (`sdk/js`, package `@tellproof/sdk`).** The S-1 API for Node 18+ (ES module and CommonJS builds without a bundler: the package ships plain `.js` with `.d.ts`), using `fetch`; no dependencies.
Done when: as S-1, tested with `node --test`.

**S-4 TypeScript auto-instrumentation.** `openai`, `@anthropic-ai/sdk`, `@aws-sdk/client-bedrock-runtime` (middleware), including streams.
Done when: as S-2.

**S-5 One-command agent install and connect flow.** Agent container image and a static binary per platform; `tellproof-agent connect <enrolment token>` enrols and starts; the web app's Sources screen shows a copy-paste snippet (one `docker run` line plus the one-line `init()` for Python and TypeScript) with the token filled in. Depends on P3-6, P5-3.
Done when: on a clean machine, the snippet from the Sources screen gets a sample app's calls into a batch the server accepts, in under five minutes.

**S-6 End to end.** A sample Python app and a sample TypeScript app → SDK → agent → server → evaluation → signed pack → share link verified in a browser, in CI.
Done when: the CI job passes from a clean database.

## Phase 4: Control plane core

**P4-1 `cmd/server migrate` + `internal/store`.** Applies `migrations/`; `WithTenant` helper; role `plumbline_app` without BYPASSRLS.
Done when: migrations apply to a fresh DB; a test proves tenant A cannot read tenant B's rows; UPDATE on `packs` raises.

**P4-2 Enrol and ingest endpoints.** `POST /v1/agents/enrol`, `POST /v1/batches` (signature, seq, prev, size limit).
Done when: valid batches return 201; bad signature 401; seq gap 409; duplicate seq returns the stored digest (idempotent).

**P4-3 Jobs.** `jobs` table worker with `SKIP LOCKED`, retries, `prune_jobs`.

**P4-4 Evaluator.** Five rule kinds, ten objectives, attention bands, `objective_set_hash`.
Done when: parity test with the concierge on fixtures passes.

**P4-5 Pack builder + KMS signer + chain.** `internal/kmssign` implements `dsse.Signer` with `DIGEST`; local verify before insert; unique `(ai_system_id, seq_no)`.
Done when: 50 concurrent issue requests for one system yield one unbroken chain; all three verifiers pass the output.

**P4-6 JWKS and key rotation.** `/.well-known/jwks.json` with `kid`, `nbf`, `exp`, revocation list.

**P4-7 Daily checkpoint.** Merkle over the day's pack digests, signed, committed to the public repo.
Done when: a script proves a given pack is in that day's checkpoint.

## Phase 5: Sharing and the web app

**P5-1 Share links + public verify API + view log.** Tokens hashed; 410 on revoke or expiry; `verifier_views` rows.

**P5-2 Auth.** Supabase JWT verification with `crypto/ecdsa` against JWKS; `members` mapping.

**P5-3 Web app screens.** AI systems, Sources (enrolment token, last batch), Packs (list, issue, download), Share links (create, revoke, views per organisation), Settings (Vanta). `html/template`, `web/brand.css`, forms that work without JS.
Done when: a new partner goes from sign-up to first issued pack in under 30 minutes without help.

## Phase 6: Integrations and launch

**P6-1 Vanta private integration.** Hourly full-state PUT of `AIAssuranceControl`; pack documents; one token per customer.

**P6-2 Release `cmd/verify` and `cmd/agent`.** CI matrix builds for linux/darwin/windows × amd64/arm64, checksums, signed release notes. Split `internal/{jcs,dsse,merkle,pack}`, `cmd/agent`, `cmd/verify` and `web/verify` into the public repo under Apache-2.0.

**P6-3 Launch checklist.** Security page, docs site page for the agent, HN post draft, test vectors published.

**P6-4 Local mode, early access and the public repository.** (Added 16 Oct 2026 at the founders' request.) `tellproof-agent local`: no server or token, signed batches kept in the data directory; `tellproof-agent report` verifies and summarises them. Early-access request options in the public repository. SDKs published as `tellproof`. This repository renamed `tellproof-cloud`; the public `tellproof` repository created from the export, with its first release.


**P6-5 Local dashboard.** (Added 16 Oct 2026 at the founders' request.) `run --local` serves a dashboard of the verified local log on loopback, in the spirit of Sentry or Grafana: totals, calls per model over time, coverage, tokens, models, timeline and the evidence chain. No frameworks.

**P6-7 Governance as code.** (Added 17 Oct 2026 at the founders' request.) A policy file declaring models, regions and registers; `check` evaluating the local log against the ten objectives with the pack evaluator; a dashboard panel; an AI-BOM; a GitHub Action.

**P6-6 The agent from pip and npm.** (Added 16 Oct 2026.) `pip install tellproof-agent` and `npx tellproof-agent`, carrying the release binaries; `run --local` as the documented local-mode command.
## Phase 7: Tellproof (added 17 Oct 2026 at the founders' request)

A consultancy called Plumbline (plumblinehq.ai) audits AI systems by hand. It has no technology, but its point is a good one: most AI controls are checks that cannot fail. Phase 7 renames the product and builds two things no audit and no observability tool gives: evidence that a control *can* fail, and evidence of what coding agents did to a codebase.

**P7-1 Rename to Witnessmark.** (Superseded before release by P7-4.) Every name, package, command, variable, attribute, file and page becomes Witnessmark; a new mark (a bolt head with a torque stripe). Acceptance: v0.2 integrations keep working unchanged (`PLUMBLINE_*` variables, `plumbline.*` attributes, `plumbline.policy.json`, `.plumbline-agent`), proven by tests and by keeping the v0.2 SDK exports as fixtures that must aggregate to the same windows; every vector still verifies; wire formats, key bindings and DB identifiers are frozen; `make check` and `make oss-check` pass.

**P7-2 Falsifiable evidence.** Guardrail canaries: the SDK sends a known-bad, content-free probe through the app's own guardrail on a schedule and records whether it was caught, so a pack shows the guardrail was alive and catching, not just configured. `check --self-test` mutates the evidence to prove each objective can fail and that a tampered log fails verification. Acceptance: canary counts per window in a new batch schema version the server accepts beside v0.2; an objective on canary catch rate in both evaluators with Go and Python parity; the self-test fails if an objective passes on evidence built to fail it.

**P7-3 Coding-agent evidence.** `tellproof-agent ci`: wraps each CI gate (tests, lint, scans), records exit code, duration and JUnit counts without logs or code, runs must-fail canary gates, and writes a signed in-toto statement per commit, including which commits name an AI agent as author. `ci verify` checks that the required gates ran and passed on a commit. Acceptance: a gate skipped, edited or faked in the statement fails `ci verify`; the statement verifies with `tellproof-verify`; the GitHub Action can attest it.

**P7-4 Rename to Tellproof.** (Added 17 Oct 2026.) Witnessmark turned out to be the name of a governance and assurance advisory (witnessmark.co) and its domains were taken, so before any public release every Witnessmark name becomes Tellproof. Acceptance: no `witnessmark` identifier left outside history; the v0.2 compatibility of P7-1 unchanged; `make check` and `make oss-check` pass; repositories renamed `tellproof` and `tellproof-cloud`.

