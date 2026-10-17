# Architecture

## 1. Components

```text
 CUSTOMER ENVIRONMENT                         LUMETRIX (Sydney)                          ANYONE
 ┌───────────────┐  OTLP/JSON  ┌───────────┐  DSSE batch   ┌──────────────────────────┐  share link  ┌─────────────┐
 │ AI app + OTel │────────────▶│ evidence  │──────────────▶│ cmd/server serve          │─────────────▶│ verify page │
 │ (or Langfuse) │  (or pull)  │ agent     │  (hourly)     │  ingest · API · web app   │              │ (static)    │
 └───────────────┘             │ cmd/agent │               ├──────────────────────────┤              ├─────────────┤
 ┌───────────────┐ usage APIs  │           │               │ cmd/server work           │  JWKS        │ cmd/verify  │
 │ OpenAI /      │────────────▶│           │               │  evaluate · build · sign  │─────────────▶│ (offline)   │
 │ Anthropic /   │ (read-only) └───────────┘               │  checkpoint · vanta push  │              └─────────────┘
 │ Bedrock       │                                         └───────────┬──────────────┘
 └───────────────┘                                     Supabase Postgres (Sydney) · AWS KMS (ap-southeast-2) · public git repo
```

One Go module produces three binaries: `cmd/agent`, `cmd/verify`, `cmd/server` (subcommands `serve`, `work`, `migrate`). Shared packages: `internal/jcs`, `internal/dsse`, `internal/merkle`, `internal/pack`.

## 2. Formats (normative)

### 2.1 Canonical JSON: RFC 8785 (JCS)

All digests and signatures are over JCS bytes. Rules our implementation must meet (vectors: `spec/jcs-vectors.json`):

- Object keys sorted by UTF-16 code units (not by UTF-8 bytes, not by code points). These differ for characters above U+FFFF versus U+E000–U+FFFF.
- No insignificant whitespace. UTF-8 output.
- Strings: escape only `"` `\` and U+0000–U+001F. Use `\b \t \n \f \r` where they exist, otherwise `\u00xx` with lowercase hex. Do **not** escape `<`, `>`, `&`, U+2028 or U+2029, all of which Go's `encoding/json` escapes. JCS output therefore cannot come from `encoding/json`; write the serializer.
- Numbers: IEEE-754 doubles serialised as ECMAScript `Number.prototype.toString` does. Integers print without a decimal point (`100.0` → `100`); `-0` → `0`; exponent form only when the decimal exponent is ≥ 21 or ≤ -7 (`1e+21`, `1e-7`); otherwise plain decimal (`1000000`, `0.000001`). Go's `strconv.FormatFloat(f, 'g', -1, 64)` does not match this. Get the shortest digits from `strconv.FormatFloat(f, 'e', -1, 64)`, then lay them out by the ES rules. NaN and ±Inf are errors.
- Parsing input: decode with `json.Decoder.UseNumber()` and convert numbers to float64; reject duplicate keys.

### 2.2 DSSE envelope

```json
{"payloadType": "application/vnd.in-toto+json",
 "payload": "<base64 std of JCS(statement)>",
 "signatures": [{"keyid": "<kid>", "sig": "<base64 std of signature>"}]}
```

Signed bytes are the pre-authentication encoding `PAE(type, body) = "DSSEv1" SP len(type) SP type SP len(body) SP body` with ASCII-decimal byte lengths. Pack signatures are ECDSA P-256 / SHA-256 in **ASN.1 DER** form (what KMS returns). Browser Web Crypto needs raw `r||s` (64 bytes), so the verify page converts DER to raw.

Agent batches use the same envelope with `payloadType: "application/vnd.lumetrix.plumbline.batch+json"` and an **Ed25519** signature (64 bytes) by the agent's key.

### 2.3 Pack statement (in-toto Statement v1)

```json
{"_type": "https://in-toto.io/Statement/v1",
 "subject": [{"name": "<pack_id>", "digest": {"sha256": "<hex sha256 of JCS(predicate)>"}}],
 "predicateType": "https://lumetrix.example/plumbline/pack/v0.1",
 "predicate": { ...pack... }}
```

`predicateType` uses a placeholder domain. Replace it with the real domain **before the first customer pack** (decision D-008); vectors are regenerated then.

**Pack digest** (used for chaining and checkpoints) = `sha256(payload bytes)` = `sha256(JCS(statement))`.

Predicate fields (schema: `spec/pack.schema.json`): `pack_schema`, `pack_id`, `seq_no`, `prev_pack_digest` (`"sha256:<hex>"` or null for #1), `issued_at`, `period{start,end,timezone}`, `subject{organisation, ai_system, environment, description}`, `profiles[]`, `assurance_level{level, meaning}`, `sources[]`, `coverage{basis, total_pct, providers[]}`, `lineage[]`, `changes[]`, `incidents[]`, `objectives[]` (`id, title, status ∈ pass|attention|fail, threshold, measured, basis, maps[]`), `commitments{trace_merkle_root, leaf_count, leaf, node, disclosed{trace_id, leaf_index, salt, proof[]}}`, `limitations[]`. Production packs add `objective_set_hash` and `batch_digests[]`; production packs drop `sample_notice`.

### 2.4 Merkle commitment

- Salt per trace: first 16 bytes of `HMAC-SHA256(customer_secret, trace_id)`. The secret never leaves the customer.
- Leaf: `sha256(0x00 || salt16 || utf8(trace_id))`. Node: `sha256(0x01 || left || right)`. An odd node at the end of a level is carried up unchanged (not duplicated).
- Proof: list of `{side: "L"|"R", hash: hex}` from leaf to root; levels where the node was carried up contribute no entry.

### 2.5 Agent batch payload (schema: `spec/batch.schema.json`, v0.2; v0.1 kept as `spec/batch-v0.1.schema.json`)

```json
{"schema": "plumbline-batch/v0.2", "agent_id": "agt_…", "seq": 1842, "prev": "sha256:<hex of previous batch payload> | null",
 "window": {"start": "2026-11-30T13:00:00Z", "end": "2026-11-30T14:00:00Z"}, "semconv_map": "v1",
 "metrics": {"tokens": [...], "pii": {...}, "content_present_spans": 0, "guardrails": {...}, "evals": [...], "agent": {...}, "transitions": [...]},
 "observed": {"models": [{"provider": "aws.bedrock", "model": "..."}], "versions": [{"model": "...", "prompt_version": "14"}]},
 "usage": [{"provider": "aws.bedrock", "model": "...", "region": "...", "input_tokens": 0, "output_tokens": 0, "requests": 0}],
 "commit": {"root": "<hex>", "leaves": 402}}
```

## 3. One-line SDKs (`sdk/python`, `sdk/js`) and the evidence agent

### 3.0 SDKs (D-025)

`pip install witnessmark` / `npm install witnessmark`, then one line: `witnessmark.init()`. The SDK patches the LLM clients the app uses (OpenAI, Anthropic, AWS Bedrock Runtime; sync, async, streaming) and records one span per call with provider, model, region, token usage, operation, duration and error status. Helpers add what no client knows: `prompt(name, version)`, `guardrail(decision)`, `personal_data(redacted)`, `tool(name, high_impact, approved)`, `escalation()`, `agent_run(name)` (attribute names from D-020). Spans go as OTLP JSON to the agent (`http://127.0.0.1:4318/v1/traces`, or `WITNESSMARK_AGENT_URL`) from a bounded background queue that drops rather than blocks. No message content is ever read. No runtime dependencies. Apps already on OpenTelemetry can skip the SDK and point an OTLP JSON exporter at the agent. Scope, guardrail and exporter semantics: D-033; every SDK's export of the shared scenario (`sdk/testdata/`) must aggregate identically in the agent.

## 3.1 Evidence agent (`cmd/agent`)

- **Inputs:** OTLP/HTTP JSON receiver on `127.0.0.1:4318` path `/v1/traces` (no protobuf); Langfuse puller (Observations v2, Scores v3); provider usage pollers (OpenAI Usage API, Anthropic Usage & Cost API, Bedrock CloudWatch with build tag `bedrock`). Change and incident registers are uploaded in the web app, not read by the agent (D-032).
- **Normaliser:** `spec/semconv-map.json` maps old and new OTel GenAI attribute names to internal fields. The map version is recorded in each batch. Message-content attributes are dropped on parse; only their presence is counted.
- **Aggregation:** hourly windows, UTC, closed 5 minutes after the hour to absorb late spans. Late spans after close go into the next window and are counted in `metrics.late_spans`. A new run also sends its open hour once, a minute after its first spans, so a new agent is seen at once (windows are additive, D-047).
- **Install:** `witnessmark-agent connect <token>` (enrols once, then runs), as a container (`cmd/agent/Dockerfile`) or a static binary (D-047).
- **Queue:** append-only segment files under `--data-dir`, `fsync` before acknowledgement, deleted after the server returns 201. Retry with exponential backoff capped at 15 minutes.
- **Keys:** Ed25519 key generated at `witnessmark-agent enrol` (one-time token from `WITNESSMARK_ENROL_TOKEN`), private key stored `0600` in the data dir; the public key is registered with the server.
- **Egress:** one host (`--server`). `--dry-run` prints each batch envelope to stdout and sends nothing.
- **Config:** JSON file plus env overrides; secrets (Langfuse keys, provider admin keys, salt secret) only from env or files with `0600`.

## 4. Control plane (`cmd/server`)

### 4.1 Processes

- `serve`: `net/http` with Go 1.22 patterns (`mux.HandleFunc("POST /v1/batches", …)`), HTML via `html/template` and `go:embed`, JSON API. Graceful shutdown on SIGTERM.
- `work`: loops claiming jobs: `SELECT … FROM jobs WHERE run_at <= now() AND locked_at IS NULL ORDER BY run_at FOR UPDATE SKIP LOCKED LIMIT 1`. Job kinds: `evaluate`, `issue_pack`, `checkpoint`, `vanta_sync`, `prune_jobs`.
- `migrate`: applies `migrations/*.sql` in order inside a transaction each, tracked in `schema_migrations`.

### 4.2 API (MVP)

| Method and path | Auth | Request → response |
| --- | --- | --- |
| `POST /v1/agents/enrol` | one-time enrolment token | `{public_key, hostname}` → `{agent_id}` |
| `POST /v1/batches` | agent signature inside the envelope | DSSE batch → `201 {digest}`; `200 {digest}` seq already stored (the stored digest); `409` seq gap or wrong prev; `401` unknown or revoked agent, bad signature; `400` invalid batch; `413` over 1 MB |
| `GET /v1/systems/{id}/status` | session | latest evaluation results |
| `POST /v1/systems/{id}/packs` | session | `{period_start, period_end}` → `202 {job_id}` |
| `POST /v1/packs/{id}/shares` | session | `{verifier_org, expires_at}` → `{url}` (token shown once) |
| `DELETE /v1/shares/{id}` | session | revoke |
| `GET /v1/verify/{token}` | public | `{envelope, prev_envelope_digest (the issuer's stored previous digest), pack_meta}`; `410` revoked or expired; `404` unknown or no pack yet; logs a view with a daily keyed address hash (D-044) |
| `GET /.well-known/jwks.json` | public | issuer public keys with `kid` (RFC 7638 thumbprint), `nbf`, `exp` (NumericDate), and a `revoked` list; a signature counts only if its key may vouch for the pack's `issued_at` (D-042) |

Sessions: Supabase Auth issues the JWT; the server verifies it against the project's published JWKS (ES256) with `crypto/ecdsa`, checks `exp`, `aud`, `iss`, and maps `sub` to `members.user_id`.

### 4.3 Evaluator

Objectives are embedded from `spec/objectives.json`, hashed (`objective_set_hash` = sha256 of the file's JCS form) and stored in `objective_defs`. Rule kinds:

| Kind | Fields | Passes when |
| --- | --- | --- |
| `ratio_at_least` | numerator, denominator, threshold | numerator / denominator ≥ threshold (denominator 0 → `attention`) |
| `max_value` | metric, threshold | metric ≤ threshold |
| `zero_count` | metric | metric = 0 |
| `freshness_within` | event, versions_from, max_age | an `event` exists for every live version within `max_age` |
| `all_declared` | observed, declared | every observed item is in the declared list |

Each objective also has `attention_band` (how close to the threshold reads as `attention` rather than `fail`). Results store measured values and the digests of every batch used.

### 4.4 Pack issue and signing

1. Lock the system's latest pack row (`SELECT … FOR UPDATE`), set `seq_no = last + 1`, `prev_pack_digest = last.digest`.
2. Build the predicate, JCS it, wrap in the statement, JCS the statement → payload.
3. `PAE` → `sha256` → `kms.Sign(KeyId, Message = digest, MessageType = DIGEST, SigningAlgorithm = ECDSA_SHA_256)`. KMS caps `Message` at 4,096 bytes, so never send the PAE itself.
4. Verify the signature locally with the cached public key before storing (catch key mix-ups).
5. Insert into `packs` (unique `(ai_system_id, seq_no)`), commit.

### 4.5 Checkpoints

Daily at 00:10 Australia/Sydney: a signed checkpoint statement for the previous local day listing every pack digest issued that day (all tenants, issue order) with their Merkle root (leaf `sha256(0x00 ‖ "sha256:<hex>")`, node as 2.4, all-zero root for none) and the previous day's checkpoint digest; empty days included, missed days caught up in order. Written to `checkpoints/YYYY/MM/DD.json` in the public repo via the GitHub contents API (never overwritten), commit SHA stored. `witnessmark-verify checkpoint` proves a pack is in a day's checkpoint (D-043).

### 4.6 Share and verify

Share token: 16 random bytes, base64url; store `sha256(token)`. The verify page lives at `web/verify/` (static, Cloudflare Pages). It reads `?t=` or `#t`, fetches `/v1/verify/{token}`, fetches JWKS, runs all checks with Web Crypto, renders from the verified payload only, and has a print stylesheet.

### 4.7 Vanta

A private integration per customer (D-049): an admin pastes the client ID and secret of their own Vanta application, the custom resource ID and optionally a document ID in Settings; the credentials are sealed with AES-256-GCM under `INTEGRATION_KEY` in `integration_secrets`. Hourly `vanta_sync` job: per connected tenant, mint a token (client credentials), one full-state `PUT /v1/resources/custom_resource` of one resource per objective of each system's latest pack (`uniqueId` `witnessmark:<system>:<objective>`; custom properties `objectiveId`, `objective`, `system`, `status`, `passing`, `packId`, `packDigest`, `periodStart`, `periodEnd`, `lastEvaluated`, `verifyUrl`), then upload packs not yet sent as files of the document (at most 10 per hour; rate limit 20 req/min per token). `verifyUrl` is a share link for the system's latest pack that the sync keeps alive and the vendor can revoke. Errors are stored per tenant as a sentence. Drata: manual upload until a Drata Advanced customer pays.

## 5. Data

Postgres 16 on Supabase (Sydney). Schema in `migrations/0001_init.sql`. The server connects as role `plumbline_app` (no `BYPASSRLS`). Each request runs in a transaction that first executes `SELECT set_config('app.tenant_id', $1, true)`; RLS policies compare `tenant_id` to `current_setting('app.tenant_id')::uuid`. Lookups that happen before the tenant is known use `SECURITY DEFINER` functions owned by the migration role instead of bypassing RLS: `agent_lookup(agent_id)` (ingest), `enrolment_claim(token_hash)` (enrolment), `member_lookup(user_id)` (login) and `verify_fetch(token_hash, ip_hash, ua_family)` (public verify; also logs the view). RLS is enabled but not forced, so the table owner bypasses it and `plumbline_app` does not. Tests: `migrations/tests/`.

## 6. Deployment and config

- Fly.io app `witnessmark` in `syd`, one shared-cpu-1x 512 MB machine running both process groups (`fly.toml` `[processes] serve = "serve"`, `work = "work"`).
- Env: `DATABASE_URL`, `KMS_KEY_ID`, `AWS_REGION=ap-southeast-2`, `SUPABASE_URL`, `SUPABASE_JWKS_URL`, `PUBLIC_BASE_URL`, `VERIFY_BASE_URL`, `CHECKPOINT_REPO`, `GITHUB_TOKEN`, `RESEND_API_KEY`, `INTEGRATION_KEY` (seals vendors' Vanta credentials; optional `VANTA_API_URL`). Secrets via `fly secrets`.
- Environments: local (docker compose Postgres; `KMS_KEY_ID=local` uses a P-256 key from `.dev/local-signing.pem`), staging, production. Different KMS keys; the verify page shows the `kid`.

## 7. Deferred (and the trigger that brings each back)

LangSmith, Braintrust, Helicone connectors: a paying customer needs one. Datadog connector: first Datadog-only customer. SSO/SCIM/roles: enterprise tier. Buyer-side portal: about 20 paying vendors. Rekor, SCITT, W3C VCs, hardware attestation: verifiers ask. TRACE export: after M2. Drata CCT: a Drata Advanced customer pays. Self-serve billing: after 10 customers.
