# Changelog

Each release's section is its release notes: the release workflow publishes the section whose heading is the tag, and attests it with the binaries.

## v0.1.0

First public release.

- `plumbline-verify`: verifies evidence packs and their chain offline against an issuer's JWKS (key validity windows and revocation included) and proves a pack is in a daily checkpoint.
- `plumbline-agent`: receives OpenTelemetry spans from the SDKs or any OTLP exporter, drops all message content at parse time, pulls Langfuse traces and provider usage (OpenAI, Anthropic; Bedrock in the `-bedrock` build), and sends signed hourly aggregates. `--dry-run` shows exactly what would be sent. `connect <token>` enrols and runs in one command.
- Python and TypeScript SDKs: one line, `plumbline.init()`, instruments OpenAI, Anthropic and Bedrock clients without reading message content.
- The static verify page, the pack and batch schemas, and the cross-implementation test vectors.
