# Engineering log

What was built, what went wrong, and how it was verified, one entry per task. Newest first. Decisions with lasting consequences go in `decisions.md`; the one-line status lives in `progress-tracker.md`. This log is for the next engineer (or reviewer) who wants to know why the code looks the way it does.

## 2026-10-15 · P1-5 · `cmd/verify`

**Built.** `plumbline-verify --jwks <file|https-url> [--json] pack...`, oldest pack first. Reuses `internal/pack.Verify`; the CLI only reads files, loads keys, chains packs and formats output. Exit codes 0/1/2 (D-017).

**Problems.** The first test run failed on layout (dots one short of `verify.py`'s column) and on a test comparing a sorted-key map with a struct; both fixed, the second in the test. Raw `dsse:` prefixes leaked into user-facing text; stripped. The untrusted-certificate test made `httptest` log a TLS handshake error; its error log now goes to `io.Discard`.

**Verified.** Acceptance: the pass chain prints VERIFIED and exits 0; each fail vector exits 1 and its output marks exactly the manifest's failed checks as FAILED (and no others). `--json` matches the manifest for all 8 cases. Usage errors exit 2 (8 cases). JWKS over https from a test server works; 404, oversized bodies, plain http and untrusted certificates are refused. Ran the built binary by hand on the vectors.

## 2026-10-15 · P1-4 · `internal/pack`

**Built.** Statement and predicate types, `Digest`, `CheckSubjectDigest`, `CheckChain`, `CheckDisclosure`, `Verify`, strict `DecodeStatement`, `ParseJWKS`.

**Problems.** Found a parser differential: `encoding/json` matches keys case-insensitively and lets later duplicates win, while JS reads exact keys, so `prev_pack_digest` plus `PREV_PACK_DIGEST` could read differently. Fixed by requiring the typed statement to re-encode to exactly the signed canonical bytes (D-016). The mutation for this guard first failed to compile (unused variable) and had to be written as a disabled condition. `disclosed: null` is schema-valid but crashes the JS reference; Go reports `merkle: null` (D-016).

**Verified.** All 8 manifest cases match exactly; tampered-payload and wrong-key now differ by subject digest (closing the D-014 gap); typed predicate round-trips byte-identically on all pass vectors; 15 malformed statements and 13 bad JWKS rejected; 4 injected bugs caught.

## 2026-10-15 · P1-3 · `internal/merkle`

**Built.** `Salt`, `LeafHash`, `NodeHash`, `Tree` (all levels kept), `Proof`, `VerifyProof`.

**Problems.** None in the code. Noted that side-based proofs do not bind `leaf_index` or `leaf_count` (D-015). The vector-rebuild test makes the package take ~9 s under `-race`; `-short` skips it.

**Verified.** Every manifest case's merkle result matches; Go rebuilt all three pass-vector trees (318k–401k leaves) from the generator's scheme and matched root, salt and disclosed proof exactly; every leaf of trees of size 1–1,000 proves; 3 injected bugs caught (including duplicating the odd node, the most common Merkle variant).

## 2026-10-15 · P1-2 · `internal/dsse`

**Built.** PAE, strict `Decode`, `Encode`, `Signer` with local P-256 (DER) and Ed25519 implementations, keyid-based `Verify`, `ParsePublicKeyPEM`.

**Problems.** The acceptance test "tampered-payload and wrong-key fail with distinct errors" cannot be met: both carry the trusted keyid and, with one key and one signature, are the same outcome. Both return `ErrBadSignature`; the pack layer separates them (D-014, raised on PR #5).

**Verified.** Pass vectors verify with the PEM key; every manifest case's signature result matches; DSSE spec PAE known answer; 14 error cases; envelope fuzzing (142k execs). A Go-signed copy of KST-CTA-0001 was verified by `concierge/verify.py` and the JS reference, proving the PAE and DER output match other implementations. 3 injected bugs caught.

## 2026-10-14 · P1-1 · `internal/jcs`

**Built.** RFC 8785 `Marshal`, strict `Parse` on `json.Decoder.Token`, `Canonicalize`.

**Problems.** `encoding/json` silently replaces invalid UTF-8 and lone surrogate escapes with U+FFFD, so Parse pre-checks the raw bytes (D-013). The JS reference never exercises `must_reject` (JSON.parse accepts duplicates and lone surrogates), so Go is the first implementation to enforce it. Miscounted one expected string length in a test by hand; the code was right.

**Verified.** 8/8 JCS vectors and 3/3 must-reject; pass payloads re-serialize byte-identically; number formatting matches Node's `String(x)` for 3,119 doubles (generated test data); 3.4M round-trip and 12.5M number fuzz execs; 3 injected bugs caught.

## 2026-10-13 · P0-2 · `internal/spec`

**Built.** Strict loading and cross-checking of `objectives.json`, `framework-refs.json`, `semconv-map.json`, with rule shapes, a ref title cap and presence-only exclusivity (D-012).

**Problems.** A stray `verify` binary from an earlier `go build ./...` was nearly committed; removed and confirmed absent from history.

**Verified.** Real files validate; 17 single-replacement breakages each fail with a message naming file, item and value.

## 2026-10-13 · P0-1 · Repo skeleton

**Built.** `cmd/verify` version stub, folder placeholders, module renamed to the GitHub org path.

**Problems.** The kit pinned Go 1.23 (out of support) and staticcheck 2025.1.1, which cannot read Go 1.27 export data; moved to Go 1.26 minimum, CI on 1.27, tools run via `go run tool@version` (D-011). Local Go is a snap stuck at 1.27.1 with 13 stdlib CVEs; switched to the 1.27.2 toolchain with `GOTOOLCHAIN`. CI's RLS step failed although the database behaved correctly: psql errors go to stderr and the step only captured stdout (fixed with `2>&1`). `make vectors` failed once `internal/` existed without packages; it now checks for packages.

**Verified.** `make check` green locally and in CI; the RLS and append-only SQL tests pass in CI.
