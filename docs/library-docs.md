# Library and API notes

Working notes on every external interface, as of October 2026. Items marked **(verify)** came from secondary sources or memory: open the linked docs and confirm before writing code against them, then remove the mark and note the date.

## OpenTelemetry GenAI semantic conventions

- Status: every `gen_ai.*` attribute is still "Development". In June 2026 (semconv v1.42.0) the GenAI conventions moved to the `open-telemetry/semantic-conventions-genai` repo. Expect renames.
- Names we read (both old and new, see `spec/semconv-map.json`): `gen_ai.provider.name` (new) / `gen_ai.system` (old); `gen_ai.request.model`; `gen_ai.response.model`; `gen_ai.usage.input_tokens` / `gen_ai.usage.prompt_tokens`; `gen_ai.usage.output_tokens` / `gen_ai.usage.completion_tokens`; `gen_ai.operation.name` (`chat`, `execute_tool`, `invoke_agent`, …).
- Content attributes `gen_ai.input.messages` and `gen_ai.output.messages` are opt-in. We count their presence and never read their values.
- Agent runs appear as an `invoke_agent` span with `chat` and `execute_tool` children.
- Docs: https://opentelemetry.io/docs/specs/semconv/gen-ai/

## OTLP/HTTP with JSON encoding

- Endpoint: `POST /v1/traces`, `Content-Type: application/json`. Body is `ExportTraceServiceRequest` in the protobuf JSON mapping: `resourceSpans[].scopeSpans[].spans[]`.
- In OTLP JSON, `traceId` and `spanId` are **hex strings** (not base64, unlike the generic proto3 JSON mapping). 64-bit integers (`startTimeUnixNano`, `intValue`) may arrive as JSON strings; accept both strings and numbers.
- Attribute values are `{"stringValue": …}`, `{"intValue": …}`, `{"doubleValue": …}`, `{"boolValue": …}`, `{"arrayValue": …}`.
- Respond `200` with `{}` (or a `partialSuccess` object). Clients may gzip the body (`Content-Encoding: gzip`); support it.
- Customers configure it in their collector as a second `otlphttp` exporter with `encoding: json` pointed at `http://127.0.0.1:4318`. (verify the exporter option name in the current collector-contrib docs)
- Spec: https://opentelemetry.io/docs/specs/otlp/

## Langfuse

- Auth: HTTP Basic with the project public key as user and secret key as password.
- Base URLs: EU `https://cloud.langfuse.com`, US `https://us.cloud.langfuse.com`, Japan `https://jp.cloud.langfuse.com`, HIPAA `https://hipaa.cloud.langfuse.com`; **no Australian region**. Self-hosted instances have their own URL and no enforced rate limit.
- **Observations API v2**: `GET /api/public/v2/observations` with `fromStartTime`, `toStartTime` (ISO 8601; start inclusive, end exclusive), `fields` (groups: `core` always; `basic`, `time`, `io`, `metadata`, `model`, `usage`, `prompt`, `metrics`), `limit` (max 1,000), `cursor`. Response `{data: [...], meta: {cursor}}`, newest first; follow `meta.cursor` until null. Fields used: `traceId`, `startTime`, `type` (`GENERATION`, `EMBEDDING`, `AGENT`, `TOOL`, `SPAN`, `EVENT`, `CHAIN`, `RETRIEVER`, `EVALUATOR`, `GUARDRAIL`), `level`, `providedModelName`, `inputUsage`, `outputUsage`, `promptName`, `promptVersion`, `metadata`; `input`/`output` for presence only. Metadata is truncated to 200 characters per value unless `expandMetadata` is set.
- **Scores API v3**: `GET /api/public/v3/scores` with `fromTimestamp`, `toTimestamp`, `name` (comma-separated), `fields` (`core`, `details`, `subject`, `annotation`), `limit` (max 100), `cursor`. Each score has `name`, `value`, `dataType` (`NUMERIC`, `BOOLEAN`, `CATEGORICAL`, `TEXT`, `CORRECTION`), `timestamp` and `subject: {kind: trace | observation | session | experiment, id, traceId?}`.
- The legacy Ingestion API is switched off on Langfuse Cloud on **16 Nov 2026**; v1 observations are deprecated. Paths and fields above checked against the Langfuse docs on 15 Oct 2026. Still to verify with a partner: the exact metadata shape Langfuse stores for OpenTelemetry-ingested spans (assumed `{attributes, resourceAttributes}`), and whether `inputUsage` includes cached tokens.
- Docs: https://langfuse.com/docs/api-and-data-platform/features/public-api, https://langfuse.com/docs/api-and-data-platform/features/observations-api, https://langfuse.com/docs/api-and-data-platform/features/scores-api

## OpenAI usage (for trace coverage)

- `GET https://api.openai.com/v1/organization/usage/completions` and `/usage/embeddings`, `Authorization: Bearer <organisation admin key>`.
- Query: `start_time`, `end_time` (Unix seconds; start inclusive, end exclusive), `bucket_width` (`1m`, `1h`, `1d`), `group_by` (e.g. `model`, `project_id`, `api_key_id`), `project_ids`, `limit`, `page`. Repeated parameters are sent as `group_by=model&group_by=project_id` (verify against a live call).
- Response: `{object: "page", data: [{object: "bucket", start_time, end_time, results: [...]}], has_more, next_page}`. Completions results: `input_tokens` (includes `input_cached_tokens`), `output_tokens`, `num_model_requests`, `model`, `project_id`, and others. Embeddings results: `input_tokens`, `num_model_requests`, `model`. Fields checked against the official SDK's types on 15 Oct 2026 (`internal/usage/openai.go`).
- No region: usage is reconciled per provider (D-028).
- Docs: https://developers.openai.com/cookbook/examples/completions_usage_api

## Anthropic usage (for trace coverage)

- `GET https://api.anthropic.com/v1/organizations/usage_report/messages` with an **Admin API key** (`x-api-key`) and `anthropic-version: 2023-06-01`; workspace keys do not work. Not available for Claude on AWS; use Bedrock metrics there.
- Query: `starting_at`, `ending_at` (RFC 3339), `bucket_width` (`1m`, `1h`, `1d`; `1h` allows up to 168 buckets per page), `group_by[]` (`model`, `inference_geo`, `workspace_id`, `api_key_id`, `service_tier`, ...), `limit`, `page` (the previous `next_page`).
- Response: `{data: [{starting_at, ending_at, results: [{uncached_input_tokens, cache_creation: {ephemeral_5m_input_tokens, ephemeral_1h_input_tokens}, cache_read_input_tokens, output_tokens, model, inference_geo, ...}]}], has_more, next_page}`. Buckets with no usage have empty `results`. **Tokens only, no request counts.** Billed input = uncached + cache creation + cache reads (D-028).
- Data lands about 5 minutes after use; poll at most once a minute for sustained use.
- Verified against the API reference on 15 Oct 2026 (`internal/usage/anthropic.go`). Docs: https://platform.claude.com/docs/en/manage-claude/usage-cost-api

## Amazon Bedrock (CloudWatch metrics)

- Namespace `AWS/Bedrock`; metrics `Invocations`, `InputTokenCount`, `OutputTokenCount`; dimension `ModelId`. Use `GetMetricData` with 1-hour periods.
- Requests that fail before model resolution have no `ModelId`.
- Requires the AWS SDK (`service/cloudwatch`), so it lives behind build tag `bedrock` in the agent.
- Docs: https://docs.aws.amazon.com/bedrock/latest/userguide/monitoring-runtime-metrics.html

## AWS KMS (pack signing)

- Key spec `ECC_NIST_P256`, usage `SIGN_VERIFY`, region `ap-southeast-2`. One key per environment.
- `Sign`: `Message` is limited to **4,096 bytes**; we always send the 32-byte SHA-256 of the PAE with `MessageType=DIGEST` and `SigningAlgorithm=ECDSA_SHA_256`. The signature comes back ASN.1 DER encoded.
- `GetPublicKey` returns a DER `SubjectPublicKeyInfo`; parse with `x509.ParsePKIXPublicKey`, cache it, and publish it as a JWK (`kty: EC`, `crv: P-256`, `x`, `y` base64url, `kid`).
- Key policy: only the production app role may call `kms:Sign` and `kms:GetPublicKey`; every call appears in CloudTrail.
- Price: US$1 per key per month; asymmetric `Sign` US$0.15 per 10,000 requests.
- Docs: https://docs.aws.amazon.com/kms/latest/APIReference/API_Sign.html

## Supabase (Postgres and Auth)

- Region: `ap-southeast-2` (Sydney) is available. Pro plan US$25/month incl. US$10 compute credit; SOC 2 reports only on the Team plan.
- Connect the app with a dedicated role `plumbline_app` that does not bypass RLS. Use the session pooler or direct connection with `pgx`.
- Auth endpoints used (checked against the supabase/auth source on 16 Oct 2026): `POST /auth/v1/otp?redirect_to=…` with `{email, create_user, code_challenge, code_challenge_method: "s256"}`; the magic link returns to `redirect_to` with `?code=`; `POST /auth/v1/token?grant_type=pkce` with `{auth_code, code_verifier}`; `POST /auth/v1/token?grant_type=refresh_token` with `{refresh_token}`; `POST /auth/v1/logout` with the access token as bearer. Every call sends the publishable key as `apikey`. Access-token claims: `sub`, `iss`, `aud` (string or array), `exp`, `role`, `email`, `session_id`. JWKS at `/auth/v1/.well-known/jwks.json`.
- Auth: magic-link email sign-in. The app verifies the access token itself. Newer projects sign with asymmetric keys published at `https://<project-ref>.supabase.co/auth/v1/.well-known/jwks.json`; check `alg` ES256, `exp`, `aud` = `authenticated`, `iss` = `https://<project-ref>.supabase.co/auth/v1`. (verify the project uses asymmetric keys; open decision D-007)
- Docs: https://supabase.com/docs/guides/platform/regions , https://supabase.com/pricing

## Vanta (private integration)

- The customer creates an application in Vanta and pastes the client ID and secret. Private integrations may define **custom resource types**; public integrations are limited to built-in types. (verify with integrationpartners@vanta.com before a public listing)
- Sync is an idempotent, full-state `PUT` of all resources of one type, keyed by `uniqueId`; resources left out count as deleted. Up to 10,000 resources and 10 MB per call. Rate limit **20 requests per minute per token**. Tokens last 3,600 s and minting a new one revokes the old, so one job owns one token per customer.
- Token: `POST https://api.vanta.com/oauth/token`, JSON `{client_id, client_secret, scope, grant_type: "client_credentials"}` → `{access_token, expires_in}`. Scopes used: `connectors.self:write-resource` and, for uploads, `connectors.self:write-document`.
- Resources: `PUT /v1/resources/custom_resource` with `{resourceId, resources: [{displayName, uniqueId, externalUrl (https), customProperties: {...}}]}` → `{success}`. Custom properties are flat (boolean, int32, string, timestamp as ISO 8601) and declared in the resource's schema in the developer console.
- Documents: `POST /v1/documents/{documentId}/uploads`, multipart `file` (required), `effectiveAtDate`, `description` → 201. (confirm the scope with a real account: the guides name `connectors.self:write-document` but do not demonstrate it)
- Docs: https://developer.vanta.com/reference/build-integrations/overview , https://developer.vanta.com/docs/concepts/resources , https://developer.vanta.com/docs/quickstart/build-private-integration

## Drata

- Custom Connections and Tests exist only on Advanced and Enterprise plans: pushed JSON up to 5 MB, 500 requests/min per IP, no top-level arrays. MVP: manual upload.
- Docs: https://help.drata.com/en/articles/11117161-custom-connections-and-tests

## GitHub (public checkpoints)

- `PUT /repos/{owner}/{repo}/contents/{path}` with a base64 `content`, `message` and, when updating, the file's `sha`. A fine-grained token with contents read/write on the one checkpoint repo only.

## Resend (email)

- `POST https://api.resend.com/emails`, `Authorization: Bearer <key>`, JSON `{from, to, subject, html, text}`. Called with `net/http`; no SDK.

## Fly.io

- Region `syd`. Machine prices vary by region (Sydney about 1.27× the US East baseline); shared-cpu-1x 512 MB is US$3.69/month at baseline.
- `fly.toml` `[processes]` runs `serve` and `work` from one image; `fly secrets set` for env secrets.
- Docs: https://docs.fly.io/about/pricing

## Browser Web Crypto (verify page)

- `crypto.subtle.importKey("jwk", {kty, crv, x, y}, {name: "ECDSA", namedCurve: "P-256"}, false, ["verify"])`.
- `crypto.subtle.verify({name: "ECDSA", hash: "SHA-256"}, key, rawSig, data)` expects the **raw 64-byte r‖s** signature: convert from DER first (see `reference/sample-verify-page.html`, `derToRaw`).
- Requires a secure context (HTTPS or localhost).
- Node 22 exposes the same API as `globalThis.crypto`, so `node --test` can run `web/verify/verify.js` against the vectors without any npm package.

## Standards referenced

- RFC 8785 JSON Canonicalization Scheme: https://www.rfc-editor.org/rfc/rfc8785
- DSSE envelope and PAE: https://github.com/secure-systems-lab/dsse
- in-toto Statement v1: https://github.com/in-toto/attestation/blob/main/spec/v1/statement.md
- TRACE (optional export after M2): https://github.com/agentrust-io/trace-spec

## Client libraries the Python SDK instruments (S-2, D-034)

Checked against the libraries' sources and the AWS API reference on 15 Oct 2026.

- **openai** (`openai-python`): resource classes `openai.resources.chat.completions.{Completions,AsyncCompletions}` (`create`, `parse`, `stream` calls `self.create`), `openai.resources.responses.{Responses,AsyncResponses}` (`create`, `parse`, `stream` calls `self.create`), `openai.resources.embeddings.{Embeddings,AsyncEmbeddings}`, `openai.resources.completions`. Usage: `prompt_tokens`/`completion_tokens` (chat, embeddings, completions), `input_tokens`/`output_tokens` (responses). Streams: `openai.Stream` / `AsyncStream`; chat chunks carry `usage` only on a final choice-less chunk when `stream_options.include_usage` is set; Responses streams end with `response.completed` (or `.incomplete` / `.failed`) carrying the response and its usage. Omitted arguments are passed as `NotGiven` / `Omit` sentinels.
- **anthropic** (`anthropic-sdk-python`): `anthropic.resources.messages.{Messages,AsyncMessages}` and `anthropic.resources.beta.messages`; `create(stream=True)` returns a raw event stream (`message_start` with the message and its usage, `message_delta` with cumulative usage); `stream()` returns a `MessageStreamManager` built on `self._post`, not `create`, whose `MessageStream` keeps a snapshot (`_current_snapshot_or_none()`). `Usage`: `input_tokens`, `output_tokens`, `cache_creation_input_tokens`, `cache_read_input_tokens`; `MessageDeltaUsage` has the same fields, all optional except `output_tokens`.
- **Bedrock Runtime** (botocore): every operation goes through `BaseClient._make_api_call(operation_name, api_params)`. Converse `TokenUsage`: `inputTokens`, `outputTokens`, `totalTokens`, `cacheReadInputTokens`, `cacheWriteInputTokens`; ConverseStream ends with `messageStop` (`stopReason`) and `metadata` (`usage`). CloudWatch `InputTokenCount` is separate from `CacheReadInputTokenCount` / `CacheWriteInputTokenCount`. InvokeModel returns `x-amzn-bedrock-input-token-count` and `x-amzn-bedrock-output-token-count` headers for at least Anthropic models (verify for others). Docs: https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_TokenUsage.html

## Client libraries the TypeScript SDK instruments (S-4, D-036)

Checked against the libraries' sources on 15 Oct 2026 (openai-node 7.34, anthropic-sdk-typescript 0.134).

- **openai** (Node): `OpenAI.Chat.Completions`, `OpenAI.Responses`, `OpenAI.Embeddings`, `OpenAI.Completions` are static class properties; `chat.completions.parse()` and `responses.parse()` call `create()`, as do the `stream()` helpers (`partial(self.create)` in Python, `create(...)` in Node). `create` returns `APIPromise` (lazy: parses only in `then`/`catch`/`finally`/`withResponse`; `_thenUnwrap(transform)` returns a new `APIPromise` over the same `responsePromise`). `Stream` has `#client` and a private `iterator`; `tee()` and `toReadableStream()` use the private iterator. Package exports: `import` → `dist/index.mjs`, `require` → `dist/index.js`.
- **@anthropic-ai/sdk**: `Anthropic.Messages`, `Anthropic.Beta`, `Beta.Messages`; `MessageStream._createMessage` calls `messages.create({ ...params, stream: true }, ...).withResponse()`. Same `APIPromise` design.
- **@aws-sdk/client-bedrock-runtime**: `BedrockRuntimeClient` extends the smithy `Client` (`send(command, options?, cb?)`); `config.region` is an async provider; commands carry `input` and their own `middlewareStack`; Converse output has `usage` and `stopReason`, ConverseStream output has `stream` (async iterable of `messageStart`, `contentBlockDelta`, `messageStop`, `metadata` events), InvokeModel output has `body` (bytes) and no headers.

## AWS KMS (pack signing, P4-5)

- `GetPublicKey(KeyId)` → `PublicKey` (DER SubjectPublicKeyInfo), `KeySpec` (must be `ECC_NIST_P256`), `KeyUsage` (`SIGN_VERIFY`).
- `Sign(KeyId, Message, MessageType=DIGEST, SigningAlgorithm=ECDSA_SHA_256)`: `Message` is the 32-byte SHA-256 of the DSSE PAE (KMS takes at most 4,096 bytes of `RAW` message); the `Signature` is ASN.1 DER, which every verifier accepts. Through `github.com/aws/aws-sdk-go-v2/service/kms` only (no hand-rolled request signing).
- Docs: https://docs.aws.amazon.com/kms/latest/APIReference/API_Sign.html

## GitHub contents API (checkpoints, P4-7)

- `PUT /repos/{owner}/{repo}/contents/{path}` with `{message, content: base64}` creates a file and returns `201 {content, commit: {sha}}`; without `sha` it fails (422) when the file exists, which the publisher treats as "already published" and checks byte for byte.
- `GET /repos/{owner}/{repo}/contents/{path}` returns `{content (base64, wrapped at 60 characters), encoding: "base64", sha}`; `GET /repos/{owner}/{repo}/commits?path={path}&per_page=1` gives the file's latest commit.
- Headers: `Authorization: Bearer <token>`, `Accept: application/vnd.github+json`, `X-GitHub-Api-Version: 2022-11-28`. A fine-grained token with contents write on the checkpoint repository only.
- Docs: https://docs.github.com/en/rest/repos/contents

## Release signing and publishing (P6-2)

- GitHub artifact attestations: `actions/attest-build-provenance@v2` with `subject-path` (files) or `subject-name` + `subject-digest` (+ `push-to-registry: true` for images). The job needs `id-token: write` and `attestations: write`. Verify with `gh attestation verify <file> --repo <owner>/<repo>`, or `oci://<image>` for images. Docs: https://docs.github.com/en/actions/security-for-github-actions/using-artifact-attestations
- PyPI trusted publishing: `pypa/gh-action-pypi-publish@release/v1` with `id-token: write` and no token; the PyPI project must name the repository, workflow file (`sdk-release.yml`) and environment (`release`) as a trusted publisher first. Docs: https://docs.pypi.org/trusted-publishers/
- npm: published by hand (D-060), `npm publish ./<file>.tgz --access public` (the `./` matters: a bare `dir/file.tgz` is read as a GitHub owner/repo). Local publishing needs 2FA; npm's browser approval is good for one request, so a 429 on the first attempt surfaces as EOTP on npm's automatic retry. New package names are rate-limited per account.
- `gh release create <tag> --draft --verify-tag --notes-file <file> <assets…>` creates an unpublished release.
