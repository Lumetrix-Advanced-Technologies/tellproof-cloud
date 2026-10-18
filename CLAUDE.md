# Tellproof

Tellproof (by Lumetrix) turns an AI product's runtime telemetry into signed evidence packs that a bank, insurer or auditor can verify without trusting us. Two programs, two SDKs and one rule: zero-dependency **SDKs** (Python, TypeScript) let an app integrate with one line, `tellproof.init()`, and stream spans without content to an open-source **evidence agent** that runs in the customer's environment and sends only signed aggregates; a **control plane** (one Go binary) evaluates evidence objectives, signs packs with AWS KMS and serves a public **verify page**. Raw prompts, completions and trace IDs never leave the customer.

## Read before any task

1. `docs/progress-tracker.md`: what is done and what is next. For the latest handoff (state, routines, roadmap): `docs/handoff.md`.
2. `docs/build-plan.md`: the task you are about to do, its acceptance tests and dependencies.
3. `docs/architecture.md`: the section for the component you are touching.
4. `docs/code-standards.md` and, when calling an external API, `docs/library-docs.md`.
5. `docs/decisions.md` before proposing anything that contradicts an earlier decision.

## Non-negotiable rules

- **No content leaves the customer.** No code path in `cmd/agent`, `internal/{otlp,langfuse,usage,queue}` or `sdk/` may send span content, prompt or completion text, or raw trace IDs; the SDKs never read message content at all. Message attributes are dropped at parse time. `--dry-run` must show exactly what would be sent. A change that weakens this is rejected.
- **The verifiers must agree.** Any change to `internal/jcs`, `internal/dsse`, `internal/merkle`, `internal/pack` or `web/verify/` must pass every file in `spec/vectors/` and `spec/jcs-vectors.json`. Never edit a vector to make a test pass; fix the code. New vectors are added, never changed.
- **Standard library first.** Allowed third-party Go modules: `github.com/jackc/pgx/v5`, `github.com/aws/aws-sdk-go-v2/config`, `.../service/kms`, and `.../service/cloudwatch` (agent, `bedrock` build tag only). Anything else needs an ADR in `docs/decisions.md` approved by a human first. No npm packages, no JS frameworks, no CSS frameworks, no build step for web assets. The SDKs in `sdk/` have no runtime dependencies (Python standard library; TypeScript `fetch` and Node built-ins) and must never break or block the host app (D-025).
- **Do not hand-roll** AWS request signing, login or session handling. KMS goes through the SDK; login is Supabase Auth.
- **Evidence is append-only.** Never write SQL that updates or deletes rows in `evidence_batches`, `objective_defs`, `evaluations`, `objective_results`, `packs`, `checkpoints` or `verifier_views`. Triggers enforce this; do not drop them.
- **Migrations are immutable** once merged. Add a new numbered file instead.
- **Never reproduce ISO/IEC 42001 text.** Framework references are IDs plus our own short titles (`spec/framework-refs.json`). IDs marked `verified: false` render with an asterisk.
- **Never log** tokens, keys, share-link tokens, Supabase JWTs, AWS credentials or batch metrics payloads. Log IDs and digests only.
- **Follow the build order.** Since D-024 the whole product is built before trial runs, in the order `docs/build-plan.md` gives. Milestone 1 is the goal of the trials: never write `M1: PASSED` unless its criteria are actually met.

## Commands

```bash
make check          # gofmt check, go vet, staticcheck, govulncheck, all tests, vectors, JS verify tests
make test           # go test ./... -race
make vectors        # cross-implementation vector suite (Go + node --test on web/verify/verify.js)
make db             # start local Postgres 16 (docker compose) and apply migrations
make test-db        # Go tests that need Postgres (CI sets TELLPROOF_TEST_DATABASE_URL and runs them in make test)
make run-server     # go run ./cmd/server serve   (env from .env.local)
make run-worker     # go run ./cmd/server work
make agent-dry      # go run ./cmd/agent --config agent.example.json --dry-run --replay concierge/testdata/otlp-sample.jsonl
python3 concierge/verify.py spec/vectors/test-public-key.pem spec/vectors/pass/*.dsse.json
```

## How to work a task

1. Pick the next unblocked task in `docs/progress-tracker.md` (or the one the human names). Mark it `in progress`.
2. Restate its acceptance tests from `docs/build-plan.md`. Write or extend the tests first.
3. Implement in the smallest set of files. Keep packages small and dependency-free.
4. Run `make check`. All green, or say exactly what fails and why.
5. Update `docs/progress-tracker.md` (status, date, notes) and, if a design choice was made, append an ADR to `docs/decisions.md`.
6. Summarise for the human reviewer: what changed, how it was tested, anything uncertain. Every PR is reviewed by the other founder.

## Where things live

`cmd/agent`, `cmd/verify`, `cmd/server` (one binary, subcommands `serve` and `work`) · `internal/` shared packages · `sdk/python`, `sdk/js` the one-line integration SDKs · `web/verify/` static verify page (open source) · `web/app/` templates, CSS, JS embedded with `go:embed` · `web/brand.css` brand tokens · `spec/` shared truth: schemas, objectives, framework refs, semconv map, vectors · `migrations/` numbered SQL · `concierge/` Python CLI, frozen after M1 · `reference/` the working sample verify page to port from.

## Brand

UI copy and styling follow the Tellproof brand system (tokens in `web/brand.css`, rules in `docs/code-standards.md` § UI). Status is never colour alone: always an icon and a word.
