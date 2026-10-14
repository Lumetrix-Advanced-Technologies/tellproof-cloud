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
