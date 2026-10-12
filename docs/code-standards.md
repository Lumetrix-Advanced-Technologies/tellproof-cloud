# Code standards

## Go

- Go 1.23 or later, one module. `gofmt`, `go vet`, `staticcheck` and `govulncheck` must pass (`make check`).
- Packages are small and single-purpose. `internal/jcs`, `internal/dsse`, `internal/merkle` and `internal/pack` import only the standard library; they are open-sourced with the agent and verifier.
- Errors: return them, wrap with `fmt.Errorf("…: %w", err)`, compare with `errors.Is/As`. No `panic` outside `main` and impossible-state assertions in tests.
- Every function that does I/O takes `context.Context` first. Set timeouts on every `http.Client` (10 s default) and every DB query.
- No package-level mutable state. Configuration is a struct built in `main` from flags and env and passed down.
- Time: store and compute in UTC (`time.Time` with `.UTC()`); convert to Australia/Sydney only for display and for pack periods, which carry their timezone.
- Logging: `log/slog` JSON handler. Log IDs, digests, durations and counts. Never log tokens, keys, JWTs, credentials, share tokens or batch metric payloads (see CLAUDE.md).
- Security helpers: compare secrets with `crypto/subtle.ConstantTimeCompare`; random from `crypto/rand` only; parse untrusted JSON with size limits (`http.MaxBytesReader`, 1 MB for batches) and `DisallowUnknownFields` where a schema is fixed.
- HTTP handlers: method-and-path patterns from `net/http` (Go 1.22+). Set `Content-Security-Policy`, `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer` on every response.
- SQL: `pgx/v5` with plain SQL strings in the `store` package only; no query building by string concatenation with user input; every tenant query runs inside `store.WithTenant(ctx, tenantID, fn)`.

## Dependencies

Allowed: `github.com/jackc/pgx/v5`; `github.com/aws/aws-sdk-go-v2/config`; `github.com/aws/aws-sdk-go-v2/service/kms`; `github.com/aws/aws-sdk-go-v2/service/cloudwatch` (agent, behind build tag `bedrock`). Adding any other module needs an ADR in `decisions.md` approved by a founder. Tests use the standard library `testing` package only (no testify).

## Tests

- Table-driven tests next to the code. Run with `-race`.
- **Vectors are the contract.** `spec/vectors/manifest.json` lists each envelope and the result every verifier must return. Go tests and `web/verify/verify.test.js` (run with `node --test`, no npm packages) both load it.
- Fuzz tests (`go test -fuzz`) for: the OTLP JSON parser, the batch envelope parser, the JCS parser and serializer (round-trip), and the "no content leaves" property (a fuzzed span with message attributes must produce a batch with no string from those attributes).
- Database tests run against the docker compose Postgres (`make db`) and are skipped when `DATABASE_URL` is unset.
- The evaluator has a parity test: for each fixture period in `spec/fixtures/`, Go results equal the concierge CLI's recorded output.

## Web (verify page and app)

- Plain HTML, one CSS file per surface, ES modules in plain JavaScript. No frameworks, no npm packages, no bundler, no build step.
- `web/brand.css` holds the brand tokens as CSS custom properties with light and dark themes. Style only through those tokens.
- Never put untrusted strings into `innerHTML`. Build DOM with `textContent` and `createElement`. Everything the verify page renders comes from the verified payload.
- The verify page loads no third-party scripts, no analytics and no fonts from third parties (self-host the woff2 files under `web/verify/fonts/`, or fall back to system fonts).
- Forms in the app post to the server and work without JavaScript; JS adds copy buttons and confirmations only.
- Accessibility: labels on every input, visible focus ring (2px `--chalk`, 2px offset), colour contrast per the brand tokens, status always as icon plus word.

## UI copy (Plumbline brand)

- Sentence case for headings and buttons; buttons say what they do ("Issue pack", "Revoke link").
- Results as plain facts: "Verified, unaltered since signing", "Failed: this pack was changed after signing".
- Never "certified", "compliant", "guaranteed", "bank-grade", "AI-powered". A pack shows what the telemetry shows and lists what it does not prove.
- Digests, IDs and commands in the mono face; never truncate a digest where a person is meant to compare it.

## SQL and migrations

- Files `migrations/NNNN_description.sql`, applied in order, never edited after merge.
- snake_case names; `uuid` primary keys from `gen_random_uuid()`; `timestamptz` everywhere; `NOT NULL` by default.
- Every customer-owned table has `tenant_id uuid NOT NULL` and an RLS policy. Evidence tables carry the `forbid_mutation` trigger.

## Python (concierge only)

Python 3.12, `cryptography` and `boto3` only. Frozen after M1: bug fixes only, and every fix gets a matching vector so the Go code inherits it.

## Git

- Branch per task: `p1-2-dsse`, `p4-5-pack-signing`. Conventional commit messages (`feat(dsse): …`, `fix(jcs): …`).
- One PR per build-plan task, reviewed by the other founder. The PR description lists the acceptance tests and how they were run.
- Never commit secrets, `.env*` files, private keys other than the clearly named test key in `spec/vectors/`, or customer data.
