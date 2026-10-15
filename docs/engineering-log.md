# Engineering log

What was built, what went wrong, and how it was verified, one entry per task. Newest first. Decisions with lasting consequences go in `decisions.md`; the one-line status lives in `progress-tracker.md`. This log is for the next engineer (or reviewer) who wants to know why the code looks the way it does.

## 2026-10-15 · P3-0 · Batch schema v0.2

**Built.** `spec/batch.schema.json` is now v0.2: a required `observed` object beside `metrics` with the models (`{provider, model}`) and model + prompt version pairs (`{model, prompt_version}`) live in the window. The v0.1 file is kept unchanged as `spec/batch-v0.1.schema.json`. The concierge emits and reads the object form.

**Problems.** The concierge had emitted `observed` as bare pairs (`[provider, model]`); JSON Schema describes objects more clearly than positional arrays, so the format moved to objects and the fixture month was converted.

**Verified.** Go: v0.2 differs from v0.1 only in the version const, `$id`, title and `observed` (every other property byte-identical). Python: every ingest window's `window`, `metrics`, `observed` and `commit` validate against v0.2; a v0.1 batch validates against the v0.1 schema and fails v0.2 until it carries the v0.2 const and `observed`. All 44 concierge tests pass, including the hand-calculated month.

## 2026-10-15 · C-4 · Concierge share

**Built.** `concierge/share.py` and `evidencepack.py share` / `unshare`: a self-contained static site (verify page, brand tokens, issuer JWKS, one file per share at `/v1/verify/<token>` in the future API's response shape), random 16-byte tokens, a share log with token hashes only, verification of every pack before it is published, and revocation by deleting the share file.

**Problems.** The verify page had no static mode; rather than add one, the concierge writes files at the API's paths so the existing share-link mode works and links survive the move to P5-1. A screenshot of a real concierge pack (the fixture month: mixed results) showed three presentation problems, fixed here: framework labels read "APRA AI letter (30 Apr 2026) theme" and "APRA CPS 230 CPS 230" (labels now use our short titles, D-023); the "Need attention" tile counted failures as well (now "Not passing: n attention · n fail"); status pills wrapped onto two lines.

**Verified.** 6 share tests (41 concierge tests in all): site layout matches the API; tokens are 16 random bytes, differ per share, and never appear in the log or on stderr, and the log is not inside the published site; tampered packs, a missing or wrong `--prev`, and a JWKS kid clash are refused. End to end with headless Chrome against a local server: both links render "Verified, unaltered since signing" (pack 2 with "Links to the previous pack"), and after `unshare` the link shows "No pack at this link". Looked at a full-page screenshot of the fixture month's pack.

## 2026-10-15 · C-3 · Concierge sign and chain

**Built.** `concierge/sign.py` and `evidencepack.py sign`: predicate from `evaluation.json`, period commitment from the leaves file with an optional disclosed trace, statement, JCS payload, PAE, ECDSA P-256 signature by a local key or KMS, chain fields from `--prev`, and `--jwks-out`. `concierge/schema.py`: the JSON Schema keywords the pack and batch schemas use, so every predicate is checked before signing. `evaluate` now carries `sources` and `assurance_level` from the system file.

**Problems.** boto3 is not installed on this machine and installing it means downloading packages, so the KMS path is exercised with a fake client that checks the call shape (DIGEST, 32-byte message, ECDSA_SHA_256); real KMS use needs boto3 and credentials. A leaves file could silently disagree with the evaluated windows, so its count must equal the evaluation's `leaf_count`.

**Verified.** 8 sign tests (35 concierge tests in all): a two-pack chain signed with a fresh key verifies in `concierge/verify.py`, in `go run ./cmd/verify --json` (signature, subject digest, chain and Merkle all true) and in the verify page's `verify.js` under Node, which is the acceptance test. The KMS signer sends the PAE digest with the right parameters; a signer whose public key differs is refused; bad pack IDs, timestamps, leaf counts, unknown traces and wrong salt secrets are refused; the CLI chains two packs and refuses a world-readable key file.

## 2026-10-15 · C-2 · Concierge evaluate

**Built.** `concierge/evaluate.py` and `evidencepack.py evaluate`: a generic engine over the five rule kinds in `spec/objectives.json`, coverage per provider and region, lineage with roles, registers filtered to the period, limitations, input fingerprints and `objective_set_hash`. `concierge/canon.py`: full RFC 8785 in Python (the existing `verify.py` subset only handles simple decimals), needed for the hash now and for signing in C-3. C-1 output gains a per-window `observed` record of live model and prompt versions.

**Problems.** The rules leave many semantics open (eval freshness above all, also transition and incident matching, bands, coverage scope); fixed in D-021 using the sample vectors' wording as the guide (pack 3 shows a new prompt version without an eval as `attention`). The batch schema cannot carry live versions, which the agent will need for EO-EVAL-01 (schema gap recorded in D-021). Python parses large integer literals exactly where JSON means doubles; `canon.loads` now converts beyond 2^53. My own tally of statuses in one test was wrong (3/3/4, not 4/3/3); the code was right.

**Verified.** 10 evaluate tests, 27 concierge tests in all. A fixture month for a fictional partner (`testdata/partner-month/`) with a hand calculation written out in the test: all ten objectives' status and measured text match, with 3 pass, 3 attention and 4 fail, including the Sydney-midnight period boundary that excludes one usage row. Coverage table and lineage shares match. The eval-freshness rule turns to `fail` once an unevaluated version has been live past `max_age`. `objective_set_hash` equals the browser verifier's JCS hash computed in Node. Python JCS matches all 3,119 Node number cases, the JCS vectors and the pass payloads byte for byte.

## 2026-10-15 · C-1 · Concierge ingest

**Built.** `concierge/evidencepack.py ingest` with `ingest.py` (OTLP JSON or JSONL; Langfuse JSON, JSONL or CSV) and `merkle.py` (§2.4 in Python). Hourly UTC windows in the batch `metrics` shape, a salted Merkle `commit` per window, and a leaves file for the period commitment in C-3. `testdata/make_samples.py` writes the same synthetic traffic in all three formats, with a content marker in every content field. `make concierge-test` added to `make check`.

**Problems.** The repo defines no attribute names for PII, guardrails, prompt versions, tool approval or escalation, and no anomaly rule; chose documented `plumbline.*` defaults with per-partner overrides (D-020). Naive model-transition detection would flip on every call when two models run side by side; transitions are tracked per agent slot. The Langfuse export format is unconfirmed; fields read are listed and marked (verify).

**Verified.** 12 unittest cases: totals match a count made directly from the generator's traces (not from ingest); Langfuse JSON, Langfuse CSV and OTLP give identical windows and leaves; no marker text, customer name, policy number or trace ID appears in any output (acceptance grep, also run by hand on the CLI output: 0 hits against 800 in the input); every window validates against `spec/batch.schema.json`; window roots and a disclosed trace's proof verify; the Python Merkle code rebuilds pass vector 1's root and exact proof; attribute overrides merge; the salt secret is required and a secret file must be mode 0600.

## 2026-10-15 · P1-6 · `web/verify/`

**Built.** `verify.js` (pure, Web Crypto: `jcs`, `pae`, `derToRaw`, `verifyEnvelope`, `verifyMerkle`, plus strict envelope, statement and JWKS decoding), `page.js` (share-link and offline loading, DOM rendering), `index.html`, `verify.css` with a print stylesheet. Shared extra vectors (`spec/vectors/extra/`, 14 cases) that Go and JS both run (D-018).

**Problems.** The reference page loads Google Fonts, builds most markup with `innerHTML` (some payload strings unescaped), renders contents before verifying, ignores `keyid`, accepts loose DER, and crashes on `disclosed: null`; rebuilt rather than ported line by line. Writing the extra vectors exposed two Go gaps, now fixed in `internal/pack`: a non-canonical payload was accepted, and `null` passed for required arrays (encoding/json decodes it to a nil slice that re-encodes as null). Added the safe-integer check so Go and JS read the same numbers. `concierge/verify.py` crashed on the no-disclosure vector; fixed. Found a bug in the P1-5 CLI while designing the page's multi-file flow: after a file that is not an envelope, the next pack's chain was "not checked" instead of failed as D-017 says; fixed with a test. UI copy: "Sample. SAMPLE." doubled (the signed notice already starts with SAMPLE); status word and text ran together for screen readers.

**Verified.** `node --test web/verify/verify.test.js`: 44 pass (8 manifest cases, 14 extra cases, JCS vectors and must-reject, DER, JWKS and envelope strictness). Go passes the same 14 extra cases. 3 injected JS bugs caught (canonical check off, kid ignored, null disclosure failing). In the browser pane: three pass vectors given out of order were sorted and verified with zero network requests; tampered, wrong-key, duplicate-key and no-disclosure packs render the right verdicts, with contents hidden when the signature fails. Share-link mode against a local mock API: chain checked against `prev_envelope_digest`, only first-party requests, clean console. Headless Chrome print to PDF: 5 pages, light theme, full digest and share URL on every page.

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
