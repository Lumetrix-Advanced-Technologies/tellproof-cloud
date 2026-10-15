# Decisions

Append-only log. New decisions go at the bottom with the next number. Superseding a decision means adding a new one that names the old.

## D-001 Go standard library, not C++ or Rust (11 Oct 2026)

The agent parses untrusted input inside customers' networks, often banks and insurers. C++ would make that memory-unsafe; CISA and the FBI list new critical-infrastructure products in memory-unsafe languages as a bad practice, and buyers' security reviews ask. Go's standard library covers HTTP, TLS, JSON, SHA-256, ECDSA, Ed25519 and templates, so Go needs fewer third-party modules than Rust (Tokio, Hyper, Rustls, Serde) for this job. Nothing in the product is CPU-bound.

## D-002 One Go binary for the control plane; no web framework, no ORM, no queue library (11 Oct 2026)

`net/http` routing (Go 1.22+), `html/template`, `pgx` with plain SQL, and a Postgres `jobs` table with `FOR UPDATE SKIP LOCKED`. Replaces the earlier TypeScript, Hono, Drizzle and pg-boss plan.

## D-003 Plain HTML, CSS and JavaScript in the browser; no build step (11 Oct 2026)

Server-rendered app pages with `go:embed`; the verify page is one static page with Web Crypto. No React, Vite, Tailwind or npm. JS tests run with `node --test` and no packages.

## D-004 Buy login and AWS request signing (11 Oct 2026)

Supabase Auth for magic links; the AWS SDK for KMS (and CloudWatch for Bedrock). Hand-written versions save a dependency and add the most common sources of security bugs.

## D-005 Aggregates and commitments only leave the customer (11 Oct 2026)

The agent sends counts, ratios and salted Merkle roots. No prompts, completions or raw trace IDs. This keeps us out of customer data, shrinks our SOC 2 scope and removes data-residency objections.

## D-006 Sign the digest with KMS (11 Oct 2026)

KMS `Sign` accepts at most 4,096 bytes and packs are about 12 KB, so we compute SHA-256 of the DSSE PAE locally and call `Sign` with `MessageType=DIGEST`, `ECDSA_SHA_256`, P-256.

## D-007 OPEN: Supabase session verification

Confirm the project signs sessions with asymmetric keys (JWKS) so the server verifies with `crypto/ecdsa` and no JWT library. Owner: build lead, by 29 Nov 2026.

## D-008 OPEN: predicateType domain

Packs use the placeholder `https://lumetrix.example/plumbline/pack/v0.1`. Buy the domain and switch before the first customer pack; regenerate vectors in the same PR.

## D-009 OPEN: Level 2 provider-usage keys

OpenAI and Anthropic usage APIs need organisation admin keys. Decide whether Level 2 reconciliation polls from the customer's agent (key stays with them) or from us. Owner: build lead, by 6 Dec 2026.

## D-010 OTLP JSON only; Bedrock behind a build tag (11 Oct 2026)

The agent accepts OTLP/HTTP with JSON encoding only, avoiding a protobuf dependency. Bedrock usage needs the AWS CloudWatch SDK, so it compiles only with `-tags bedrock`; default agent builds stay standard-library only.

## D-011 Go 1.26 minimum, CI on 1.27; lint tools pinned via `go run` (13 Oct 2026)

The kit pinned Go 1.23, which has been out of support since Aug 2025 (supported releases are now 1.26 and 1.27), so stdlib fixes stop reaching it and `govulncheck` would flag the verifier and agent. staticcheck 2025.1.1 also cannot read Go 1.27 export data, and current staticcheck (2026.2.2) and govulncheck (v1.9.0) need Go 1.26 to build. `go.mod` now says `go 1.26`, CI builds with the latest 1.27.x, and the Makefile runs both tools with `go run tool@version` so the pinned version is always used and `go.mod` gains no tool modules. Revisit at each Go release: keep the minimum at the oldest supported release.

## D-012 Spec checks go beyond references (13 Oct 2026)

`internal/spec` (P0-2) checks what the build plan asks (strict decoding, `maps_to` resolves, rule kinds are the five) and three more things, because each guards a rule the evaluator or CLAUDE.md depends on: (1) each rule kind has fixed required and optional fields (`ratio_at_least`: numerator, denominator, threshold, optional attention_band; `max_value`: metric, threshold, optional attention_band; `zero_count`: metric; `freshness_within`: event, versions_from, max_age, optional min_pass_rate; `all_declared`: observed, declared), so a rule cannot carry a field its kind ignores; (2) framework ref titles are at most 60 characters, a tripwire against pasted ISO/IEC 42001 or other licensed text; (3) in `semconv-map.json` every attribute appears once, and no presence-only (content) attribute can also be mapped to a value field. A new rule kind or field means changing `ruleShapes` in the same PR as the spec.

## D-013 JCS parser built on `json.Decoder.Token`, with I-JSON pre-checks and a depth cap (14 Oct 2026)

`internal/jcs.Parse` walks `json.Decoder.Token()` with `UseNumber()` (as §2.1 says), which keeps syntax checking in the standard library and lets us see every object key to reject duplicates. `encoding/json` silently turns invalid UTF-8 and unpaired `\uD800`–`\uDFFF` escapes into U+FFFD, so Parse first checks `utf8.Valid` and scans string escapes for unpaired surrogates; both fail with `ErrInvalidUnicode`. Numbers that overflow float64 fail with `ErrNonFinite`. Nesting is capped at 1,000 levels (`ErrTooDeep`) because the input is untrusted; packs and batches nest a few levels, so no real pack hits it, but a browser `JSON.parse` would accept such input, so this is the one place the Go verifier may reject what the JS one accepts. `Marshal` accepts only the decoded value types (nil, bool, float64, string, `[]any`, `map[string]any`); Go structs go through `encoding/json` then `Canonicalize`. Number formatting is cross-checked against Node's `String(x)` for 3,119 doubles (`internal/jcs/testdata/es-numbers.json`, generator alongside).

## D-014 DSSE verification: one error for any bad signature; keyid and payload type required (15 Oct 2026)

P1-2 asks that `fail/tampered-payload` and `fail/wrong-key` fail "with distinct errors". Both envelopes carry the trusted keyid; one is the trusted key's signature over other bytes, the other a different key's signature over these bytes. With one public key and one ECDSA signature the two are mathematically the same outcome (the signature does not verify), so `internal/dsse.Verify` returns `ErrBadSignature` for both. The manifest already separates them one layer up: only `tampered-payload` also fails `subject_digest`, which `internal/pack` (P1-4) reports. Failures that can be told apart get their own error: `ErrMalformed`, `ErrNoSignatures`, `ErrPayloadType`, `ErrUnknownKey`, `ErrUnsupportedKey`.

Other choices: `Verify` takes the expected payload type, so a batch envelope can never pass as a pack; it looks keys up by `keyid` (an empty or unknown keyid is `ErrUnknownKey`) and accepts the envelope if any signature from a trusted key verifies; it returns the payload so callers use the verified bytes. `Decode` is strict: unknown fields, trailing data, URL-safe or unpadded base64, and empty payloads are rejected. The reference JS verifier ignores `keyid` and uses the first JWKS key; P1-6 should look keys up by `kid` the same way.

## D-015 Merkle proofs prove membership, not position (15 Oct 2026)

`internal/merkle` keeps every level of the tree (`NewTree`, then `Root` and `Proof(i)` without rehashing; about 2n hashes of memory, 25 MB at 400k leaves) and verifies with `VerifyProof(leaf, proof, root)`, exactly the side-based rule the JS and Python verifiers use. Because levels where a node is carried up contribute no step, the proof does not determine `leaf_index`, and the root does not commit to `leaf_count`. A valid disclosure therefore shows that the trace is in the committed set, not where it sits or how many leaves there are. That is all a pack claims today. If a future check relies on position or count (for example ordering traces in time), every verifier must also check the proof's shape against `leaf_index` and `leaf_count`, which is a format change with new vectors.

## D-016 Pack verification semantics: null disclosure, strict statements, strict JWKS (15 Oct 2026)

`internal/pack.Verify` computes each check on its own (as the reference does), so a pack that fails its signature still reports its subject digest, chain and Merkle results. It returns an error, never `verified`, only when the payload is not a v0.1 pack statement. Three choices every verifier (P1-5, P1-6) must share:

1. **No disclosed trace.** The schema allows `commitments.disclosed: null`. Then `merkle` is `null` (not checked), like `chain` without a previous pack, and does not block `verified`. The JS reference would throw on such a pack; no vector covers it yet. P1-6 should match, and a pass vector with `disclosed: null` should be added when vectors are next generated.
2. **Strict statements.** The payload must be I-JSON (no duplicate keys, valid Unicode), have `_type` in-toto Statement v1, our `predicateType`, exactly one subject with a lowercase-hex sha256, `pack_schema` `assurance-pack/v0.1`, no fields outside the schema, and well-formed digests, salt and proof steps. Because `encoding/json` matches keys case-insensitively and lets later duplicates win, the typed statement must re-encode to exactly the canonical bytes that were signed; otherwise a payload with both `prev_pack_digest` and `PREV_PACK_DIGEST` could read differently in Go and JS. Packs we issue always satisfy this; a hand-made pack with an empty optional field (for example `"acknowledged": ""`) is rejected.
3. **Strict JWKS.** `ParseJWKS` accepts only EC P-256 keys with `kid`, optional `alg: ES256` and `use: sig`, unpadded base64url coordinates on the curve, and no unknown members, so a JWKS with `nbf`, `exp` or a revocation list fails until P4-6 gives those meaning instead of being ignored.

The subject digest is always computed over JCS of the predicate exactly as signed, never over a re-encoded struct.

## D-017 `plumbline-verify` contract: exit codes, JWKS sources, chain across files (15 Oct 2026)

Exit 0 only when every pack verifies; 1 when any pack fails, including a file that decodes as JSON but is not a valid pack; 2 for usage and input errors (bad flags, unreadable files, a JWKS that cannot be loaded or parsed). Scripts can therefore tell "the evidence failed" from "the check could not run". `--jwks` takes a file or an `https://` URL only: fetching the issuer's keys over plain HTTP would let anyone on the path substitute keys and make a forged pack verify. Fetches use a 10 s timeout and a 1 MB cap; pack files are capped at 16 MB. Packs are given oldest first and each is chain-checked against the previous file; if a file is not an envelope, the next pack's chain check fails rather than being skipped. Flags may come before or after file names. Human output follows `concierge/verify.py`'s layout, names every failed check on its line and in the pack's result line, and prints digests in full. `--json` prints one document `{verified, packs[]}` whose per-pack fields match `manifest.json`'s expected results.

## D-018 Every verifier enforces the same strictness, proven by shared extra vectors (15 Oct 2026)

The core manifest covers eight cases; the verifiers could still disagree on malformed packs (a browser's `JSON.parse` keeps the last duplicate key and accepts unpaired surrogates; Go's `encoding/json` matches keys case-insensitively and decodes `null` into an empty value). So:

1. **The payload must be exactly JCS(statement)**, which §2.3 already defines it as. It is the one rule a browser can enforce identically: decode UTF-8 strictly, `JSON.parse`, re-canonicalize and compare, which also exposes duplicate keys. Every pack we issue and every existing vector already satisfies it.
2. **Same types, same rejections.** `web/verify/verify.js` carries a field-by-field table of the Go v0.1 types; required strings may be empty, optional fields must be absent or non-empty, nullable fields must be present, required arrays may not be null. Go adds the missing null-array check. Integers must be safe JavaScript integers (|n| ≤ 2^53−1) in both, so no verifier reads a different number.
3. **Shared extra vectors.** `spec/vectors/make_extra_vectors.py` writes `spec/vectors/extra/` and `extra-manifest.json` (14 cases: a pass with no disclosure, a batch-typed envelope, an unknown kid, and 11 packs that must be refused). Go (`internal/pack`) and the browser verifier both run it; existing vectors are untouched and the reference test is unchanged. `concierge/verify.py` gained the null-disclosure fix with its matching vector.
4. **DER signatures are parsed as strictly as Go** in the browser (short-form lengths, minimal non-negative integers, no trailing bytes), so a re-encoded valid signature cannot pass in one verifier and fail in another.

## D-019 Verify page: render only issuer-signed contents; deploy `web/` as the site root (15 Oct 2026)

`web/verify/` is plain HTML, CSS and ES modules: `verify.js` (pure checks, tested with `node --test`), `page.js` (loading and DOM), `verify.css` (brand tokens only, print stylesheet). Pack contents are rendered only when the issuer's signature verifies; otherwise the page shows the failure and the raw envelope, because unsigned contents cannot be attributed to the issuer. All DOM is built with `createElement`/`textContent`; status classes come from fixed maps, never from payload strings. A strict CSP (`default-src 'none'`, scripts and styles from self only), `no-referrer`, no third-party fonts or scripts. Share links (`?t=` or `#t=`) call `GET {api}/v1/verify/{token}` and `{api}/.well-known/jwks.json`, where `{api}` comes from the `plumbline-api` meta tag (empty means the page's own origin); the chain is checked against `prev_envelope_digest`. Offline mode takes a JWKS file and pack files, orders them by claimed `seq_no` and uses no network. Printing switches to the light theme and repeats the pack ID, full digest and share URL (or source file) on every page. The page links `../brand.css`, so the static site publishes the whole `web/` directory with the page at `/verify/`; the P6-2 open-source split must carry `brand.css` with it.

## D-020 Concierge ingest: Plumbline attribute defaults, per-partner overrides, hourly anomaly rule (15 Oct 2026)

`spec/semconv-map.json` maps provider, models, tokens, operation and region, and lists the content attributes. The objectives also need facts no OTel convention defines yet: personal-data tagging, redaction, guardrail decisions, prompt name and version, high-impact tools and their approval, escalations. C-1 reads them from documented defaults (`plumbline.data_class` = `personal`, `plumbline.redaction.applied`, `plumbline.guardrail.decision` = allow|block|flag on the LLM call span, `plumbline.prompt.name`/`.version`, `plumbline.tool.high_impact`, `plumbline.tool.approved`, `plumbline.escalation`), overridable per design partner with `--attributes partner.json`, because each partner's telemetry will name these differently. Model transitions are tracked per slot (`gen_ai.agent.name`, falling back to `service.name`) so two models used side by side are not read as changes; prompt transitions per prompt name. An hour is an anomaly when it has at least 20 LLM calls and an error rate of at least 5% or a guardrail block rate of at least 3% (all configurable). Only these named attributes are read; every other attribute value, including content, is ignored at parse time, and trace IDs leave only as salted Merkle leaves. Langfuse export fields (`traceId`, `type`, `startTime`, `model`, `usageDetails`, `level`, `promptName`, `promptVersion`, `metadata`; `input`/`output` for presence only) are marked (verify) until checked against a real partner export. P3-1 should adopt these names in `semconv-map.json` (or replace them) rather than invent a second set.

## D-021 Evaluation semantics the objective rules leave open (15 Oct 2026)

`concierge/evaluate.py` (C-2) applies `spec/objectives.json` generically by rule kind; P4-4's Go evaluator must give the same results (parity test). Choices made here:

- **Period**: whole days in the system's timezone, as a half-open UTC interval. Aggregate windows and usage rows count when they start inside it.
- **Bands**: `ratio_at_least` passes at ≥ threshold, is `attention` down to threshold − band, else fails; denominator 0 is `attention`. `max_value` passes at ≤ threshold, `attention` up to threshold + band. `zero_count` and `all_declared` pass or fail.
- **Coverage (EO-LOG-01)**: tokens (input + output), per provider and region. Only providers with usage data count toward the total; a provider with traces but no usage is listed and named in the limitations. Percentages shown in `measured` and `total_pct` are rounded **down**, so a result is never displayed above a threshold it missed.
- **Transitions (EO-CHG-01)**: a model or prompt transition is logged when the change register has an entry of the same kind dated within one day either side (local dates).
- **Anomalies (EO-INC-01)**: an anomaly window is acknowledged when an incident opened within 60 min of the window was acknowledged within 60 min of opening.
- **Eval freshness (EO-EVAL-01)**: live versions are the (model, prompt version) pairs observed in the period, or models alone when no prompt versions are emitted. Each needs a passing eval (pass rate ≥ `min_pass_rate`) that ran before the version was first seen and at most `max_age` before it; versions already live in the first window need only one that ran before. A version without one is `attention` while it has been live less than `max_age` at period end (as pack 3 of the sample shows), `fail` after.
- **Agent safety (EO-AGT-01)**: status follows the rule (tool error rate); escalation share and high-impact approvals are reported in `measured`.
- **Framework maps** render as "framework ref", with an asterisk when the ref is `verified: false`.
- **Input formats** (concierge): `system.json` (declarations), usage CSV `provider,model,region,start,end,input_tokens,output_tokens,requests` (provider IDs as in telemetry; founders convert each provider's export), changes CSV `date,kind,what`, incidents CSV `id,opened,acknowledged,resolved,summary`, evals CSV `suite,model,prompt_version,ran_at,pass_rate`.
- **Schema gap**: eval freshness needs the versions live in each window, which the batch `metrics` schema cannot carry (`additionalProperties: false`). Concierge aggregates put an `observed` record beside `metrics`; P3/P4 need a batch schema change (a new version) before the agent can support EO-EVAL-01.
- `objective_set_hash` is `sha256:` + SHA-256 of JCS(`spec/objectives.json`); the test checks it against the browser verifier's JCS.

## D-022 Concierge signing: schema-checked, self-verified, KMS by digest (15 Oct 2026)

`evidencepack.py sign` (C-3) builds the predicate from `evaluation.json`, commits to the period's leaves and signs. Choices:

- **Signers.** A local P-256 PEM (mode 0600) for pilots and tests, or AWS KMS through boto3 with `MessageType=DIGEST` and the 32-byte SHA-256 of the PAE (D-006). boto3 is imported only for KMS, so the rest of the concierge needs only `cryptography`.
- **Checks before anything is written.** The predicate must pass `spec/pack.schema.json` (a small built-in validator for the keywords the schema uses); the payload must be exactly JCS(statement) (D-018); and the signature is verified with the signer's public key (from KMS `GetPublicKey` for KMS), so a key mix-up never produces a pack.
- **Chain.** `--prev` sets `seq_no` to the previous pack's plus one and `prev_pack_digest` to the SHA-256 of its payload. Pack IDs follow the schema pattern (for example `WRN-RS-0001`).
- **Commitment.** The leaves file must hold exactly the leaves of the evaluated windows (its count must equal the evaluation's `leaf_count`). `--disclose TRACE_ID` reveals one trace: the salt is re-derived from the vendor's secret and the leaf must already be in the commitment, so a wrong trace ID or secret fails instead of producing a proof that cannot verify.
- **Optional fields** are left out rather than written empty (`sample_notice`, a subject `description`, incident `acknowledged`/`resolved`), as the verifiers require. Concierge packs carry `objective_set_hash`; `batch_digests` is for production packs only.
- `--jwks-out` writes the signer's public key as the JWKS the verifiers take.

## D-023 Concierge sharing: a static site in the API's shape; tokens never logged (15 Oct 2026)

`evidencepack.py share` (C-4) writes a static site: the verify page (`web/verify` plus `web/brand.css`), the issuer keys at `/.well-known/jwks.json`, and one file per share at `/v1/verify/<token>` holding exactly what `GET /v1/verify/{token}` will return (`{envelope, prev_envelope_digest, pack_meta}`). The verify page's share-link mode therefore works unchanged, and when P5-1 moves sharing to the server, links keep their form (`<base>/verify/?t=<token>`). Choices:

- **Tokens**: 16 random bytes, base64url (§4.6). The token is printed once for the founders to paste into the CRM; the share log (`share-log.csv`, written beside the site directory, never inside it) records only `sha256(token)`, the pack, its digest and the verifier organisation, because CLAUDE.md forbids logging share tokens.
- **Nothing unverified is published**: `share` checks the signature against the JWKS it publishes, canonical form, subject digest, the chain against `--prev` (required for any pack after the first) and the disclosed proof. A kid already published with a different key is refused.
- **Revoking** deletes the share file (`unshare --token` or `--token-sha256` from the log); the page then reports no pack at the link. Static hosting cannot return 410 or log views; both arrive with P5-1.
- **Framework map labels** (supersedes the format in D-021): "framework ref title" using our own short titles (for example "AIUC-1 E Accountability"), or "framework: title" where the ref adds nothing (APRA letter themes, CPS 230), asterisked when unverified.
