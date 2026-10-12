# Project overview

## What we are building

**Plumbline** is a product of **Lumetrix**. It gives AI-native software companies evidence about how their AI behaves in production that their enterprise buyers, insurers and auditors can check for themselves.

The output is a **pack**: a signed JSON document (in-toto Statement in a DSSE envelope) that reports ten evidence objectives for one AI system over one period, such as trace coverage, PII redaction coverage, guardrail coverage and eval freshness. Each pack links to the previous one by digest, so a missing or edited month is visible. A verifier opens a share link and the browser checks the signature, the digest, the chain and a disclosed trace's Merkle proof, with no account and no trust in us.

## Who it is for

| Role | Who | What they need |
| --- | --- | --- |
| Customer (pays) | AI-native B2B SaaS, 20–200 staff, selling LLM or agent features to banks, insurers, super funds, or AI agent vendors seeking AIUC-1 certification or AI liability insurance | Get through AI security reviews and underwriting faster, with evidence rather than questionnaire answers |
| Champion | First security/GRC hire, or the CTO in smaller companies | Less questionnaire work, evidence that updates itself |
| Blocker | Head of AI/ML | Proof that raw prompts and data never leave their environment |
| Verifier (free) | Bank or insurer third-party risk teams, AI insurers, ISO 42001 and AIUC-1 auditors | A result they can check independently and rely on |

## The problem in one paragraph

Enterprise buyers and APRA-regulated institutions now ask AI vendors for continuous assurance of their AI systems (APRA's AI letter of 30 Apr 2026 criticises "overreliance on vendor presentations"). Vendors answer with self-written questionnaires. Compliance platforms (Vanta, Drata) check cloud configuration and treat ISO 42001 as a checklist; observability tools (Langfuse, Datadog) hold the telemetry but give it no assurance meaning. Nobody turns runtime telemetry into evidence a third party can verify.

## MVP scope

The eleven features in `build-plan.md` Phases 3–6: evidence agent (OTLP JSON and Langfuse inputs), provider usage reconciliation (OpenAI, Anthropic, Bedrock), signed batches, evaluator with five rule kinds and ten objectives, KMS-signed and chained packs, daily public checkpoint, share links with view log, print-ready verify page, Vanta private integration, open-source Go verifier, and a five-screen web app.

## Non-goals for the MVP

Dashboards; storing raw traces; LangSmith, Braintrust, Helicone or Datadog connectors; SSO/SCIM/roles; a buyer-side portal; hardware attestation, SCITT, W3C credentials or Rekor anchoring; questionnaire auto-answering; self-serve billing; multi-region hosting. Each has a trigger that brings it back (see `architecture.md` § Deferred).

## Milestones and gates

| Gate | Date | Pass criteria | Unlocks |
| --- | --- | --- | --- |
| M1 | 22 Nov 2026 | ≥15 qualified calls; ≥40% with a deal blocked in AI review; ≥3 paid concierge pilots at ≥A$3K; ≥60% of payers choose the telemetry pack over a document-only offer | Phases 3–6 (platform build) |
| M2 | 20 Dec 2026 | ≥1 named verifier relies on a pack in writing; ≥3 of 10 verifier interviews prefer signed evidence | OSS launch, raise or partner conversations |
| M3 | 31 Jan 2027 | ≥3 inbound or referral leads not from founder outbound; ≥1 pack reused with a second verifier; ≥2 pilots converted to recurring | Continue, US push, SOC 2 Type I planning |

Open-source launch (agent, verifier, verify page) is planned for 5–6 Jan 2027.

## Team

Two founders, both part-time. Founder A leads go-to-market; Founder B leads the build. Claude Code is the pair engineer; every PR is reviewed by the other founder.

## Glossary

- **Evidence objective (EO):** one control we compute from telemetry, e.g. `EO-LOG-01 Trace coverage`. Defined in `spec/objectives.json`.
- **Batch:** one hour of aggregates from one agent, signed by the agent's Ed25519 key.
- **Pack:** the signed result of evaluating all EOs for one AI system over one period.
- **Trace coverage:** traced tokens divided by provider-billed tokens.
- **Assurance level 1:** provider usage supplied by the vendor's agent. **Level 2:** pulled independently (design open, see decisions).
- **Verifier:** anyone checking a pack: browser page, Go CLI, or `concierge/verify.py`.
- **Checkpoint:** the daily signed Merkle root over all pack digests issued that day, published to a public git repo.

## Related material

- Technical design doc (living): "Assurance Pack MVP technical design" in Claude Docs.
- Brand system: "Plumbline" design system (tokens, logo marks, voice rules). Tokens are mirrored in `web/brand.css`.
- Working sample of the format: `reference/sample-verify-page.html` and `spec/vectors/pass/`.
