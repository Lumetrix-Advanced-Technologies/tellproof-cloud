# Plumbline starter kit

Everything Claude Code needs to start building Plumbline (by Lumetrix): the rules, the design, the build plan, the format specs, working test vectors and a tested database schema. There is no product code yet; Claude Code writes it task by task.

## Start

1. Create the repo and copy this kit into it: `git init plumbline && cp -r plumbline-starter/. plumbline/ && cd plumbline`.
2. Open Claude Code in the repo. It reads `CLAUDE.md` automatically.
3. First prompt: `/next-task` (it will pick P0-1, the repo skeleton). Keep using `/next-task` for each step, `/check` to get `make check` green and `/guard-review` before you open a PR.
4. Concierge work for design partners (Phase C) can run in parallel with Phase 1.
5. When Milestone 1 passes, a founder edits `docs/progress-tracker.md` to `M1: PASSED`; that unlocks Phases 3–6.

## What is here

| Path | What |
| --- | --- |
| `CLAUDE.md` | Rules Claude Code follows every session: read order, non-negotiables, commands, workflow |
| `docs/project-overview.md` | Product, users, scope, non-goals, milestones and gates, glossary |
| `docs/architecture.md` | Components and normative formats: JCS rules, DSSE, pack statement, Merkle, batch, API, evaluator, signing, data, deployment |
| `docs/code-standards.md` | Go, web, SQL, Python, Git and UI-copy rules; dependency allowlist |
| `docs/library-docs.md` | Notes on every external API (OTel, Langfuse, OpenAI, Anthropic, Bedrock, KMS, Supabase, Vanta, Drata, GitHub, Resend, Fly, Web Crypto) |
| `docs/build-plan.md` | Tasks P0 to P6 and C-1 to C-4 with acceptance tests and the M1 gate |
| `docs/progress-tracker.md` | Status of every task and milestone; Claude Code updates it |
| `docs/decisions.md` | Decision log (D-001 to D-010), including the open ones |
| `spec/` | Pack and batch JSON Schemas, the ten objectives, framework references (IDs only), OTel name map, JCS vectors |
| `spec/vectors/` | 3 pass and 5 fail signed packs, a manifest of expected results, the test key and the generator |
| `migrations/0001_init.sql` | Full schema: RLS by tenant, append-only evidence triggers, lookup functions for the agent and the public verify page |
| `migrations/tests/` | SQL tests for RLS and append-only behaviour (all pass on Postgres 16) |
| `web/brand.css` | Plumbline brand tokens (light and dark) generated from the design system |
| `reference/` | The working sample verify page and the reference verifier test |
| `concierge/verify.py` | Python verifier used during the concierge phase |
| `Makefile`, `.github/workflows/ci.yml`, `docker-compose.yml`, `.env.example`, `agent.example.json`, `go.mod` | Tooling |
| `.claude/commands/` | `/next-task`, `/check`, `/guard-review` |

## Already verified in this kit

- `node --test reference/verify-reference.test.mjs`: 17 of 17 pass (8 vector cases, canonical-form check, 8 JCS cases).
- All three pass vectors verify with `concierge/verify.py`, and all three predicates validate against `spec/pack.schema.json`.
- `migrations/0001_init.sql` applies cleanly to Postgres 16 and the RLS and append-only tests behave as expected.
