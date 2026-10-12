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
- Regions: EU, US, JP and a HIPAA region; **no Australian region**. The customer's region decides the base URL.
- Use the Observations API v2 (cursor pagination, up to 1,000 per page) and Scores API v3 (100 per page). The legacy ingestion API is switched off on Langfuse Cloud on **16 Nov 2026**; do not build on v1 endpoints. (verify exact paths)
- Docs: https://langfuse.com/docs/api-and-data-platform/features/public-api

## OpenAI usage (for trace coverage)

- `GET https://api.openai.com/v1/organization/usage/completions` (and `/usage/embeddings`). Needs an **organisation admin key**.
- Query: `start_time` (Unix seconds), `end_time`, `bucket_width` (`1m`, `1h`, `1d`), `group_by` (e.g. `model`, `project_id`, `api_key_id`), `limit`, `page`.
- Returns buckets with `input_tokens`, `output_tokens`, `num_model_requests` per group. (verify field names and bucket freshness)
- Docs: https://developers.openai.com/api/reference/resources/admin/subresources/organization/subresources/usage

## Anthropic usage (for trace coverage)

- `GET https://api.anthropic.com/v1/organizations/usage_report/messages` with an **Admin API key** (`x-api-key`) and `anthropic-version` header.
- Buckets `1m`, `1h`, `1d`; group by model, workspace, API key and `inference_geo`. Data lands about 5 minutes after use. Poll no more than once a minute.
- Returns **tokens only, no request counts**. Coverage is therefore token-based for every provider.
- Not available programmatically for Claude on AWS; use Bedrock metrics there. (verify)
- Docs: https://platform.claude.com/docs/en/manage-claude/usage-cost-api

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
- Auth: magic-link email sign-in. The app verifies the access token itself. Newer projects sign with asymmetric keys published at `https://<project-ref>.supabase.co/auth/v1/.well-known/jwks.json`; check `alg` ES256, `exp`, `aud` = `authenticated`, `iss` = `https://<project-ref>.supabase.co/auth/v1`. (verify the project uses asymmetric keys; open decision D-007)
- Docs: https://supabase.com/docs/guides/platform/regions , https://supabase.com/pricing

## Vanta (private integration)

- The customer creates an application in Vanta and pastes the client ID and secret. Private integrations may define **custom resource types**; public integrations are limited to built-in types. (verify with integrationpartners@vanta.com before a public listing)
- Sync is an idempotent, full-state `PUT` of all resources of one type, keyed by `uniqueId`; resources left out count as deleted. Up to 10,000 resources and 10 MB per call. Rate limit **20 requests per minute per token**. Tokens last 3,600 s and minting a new one revokes the old, so one job owns one token per customer.
- Upload packs as documents with the `self:write-document` scope.
- Docs: https://developer.vanta.com/reference/build-integrations/overview , https://developer.vanta.com/docs/concepts/resources

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
