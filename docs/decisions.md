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
