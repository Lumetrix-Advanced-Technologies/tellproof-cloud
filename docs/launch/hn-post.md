# Show HN draft

Status: **draft, not posted.** A founder edits and posts it, after the launch checklist (`checklist.md`) is done. Links are placeholders until the public repository and site exist.

---

**Title:** Show HN: Witnessmark – signed evidence packs for AI systems that auditors verify offline

**Text:**

When an AI vendor sells to a bank or an insurer, the vendor-risk team asks the same questions every quarter: which models are you running, where, do guardrails run on every call, when did you last evaluate, did anything change? Today the answers are screenshots and a spreadsheet, and the buyer has to take the vendor's word for them.

Witnessmark turns the vendor's own runtime telemetry into a monthly evidence pack that the buyer can check without trusting the vendor or us:

- A one-line SDK (`witnessmark.init()`, Python and TypeScript, no dependencies) instruments OpenAI, Anthropic and Bedrock calls. It never reads prompts or completions.
- An open-source agent runs in the vendor's environment, drops any message content at parse time, and sends only signed hourly aggregates: token counts per model and region, guardrail and eval results, and a Merkle root over that hour's trace IDs. `--dry-run` prints exactly what it would send.
- The service evaluates eleven evidence objectives (coverage against provider billing, residency, guardrail coverage, eval freshness, change and incident registers…) and issues a pack. A pack is an in-toto statement in a DSSE envelope, signed with a KMS-held P-256 key and chained to the previous pack by digest.
- The reviewer opens a share link and the checks run in their browser (Web Crypto), or they run `witnessmark-verify` offline. Every pack digest also goes into a signed daily checkpoint in a public Git repository, so packs can't be backdated quietly.

What it doesn't do: a pack shows what the telemetry shows. If calls bypass the agent, they're not in it; the coverage objective compares traced tokens with the provider's billed tokens to show the gap.

The agent, the SDKs, three verifiers (Go, browser, Python) and the test vectors they must all agree on are Apache-2.0: <PUBLIC REPO LINK>. The security page explains what leaves your environment and what never does: <SECURITY PAGE LINK>. You can verify a sample pack here: <VERIFY PAGE LINK WITH SAMPLE>.

We'd especially like to hear from people on the receiving end of AI vendor questionnaires: what would you need to see in a pack before you'd accept it in place of a screenshot?

---

Notes for the founder posting it:

- Post from a personal account, Tuesday to Thursday, early US Eastern morning. Stay in the thread for the first few hours.
- Don't use "certified", "compliant", "guaranteed" or "bank-grade" in replies either (brand rules).
- Expect questions on: why not SOC 2 or ISO 42001 evidence directly (answer: packs map to framework references by ID, and they complement an audit rather than replace it); what stops a vendor faking telemetry (answer: nothing cryptographic; the coverage check against provider billing and the signed agent batches raise the cost, and the limits are stated on the security page); and why KMS rather than Sigstore for packs (answer: a long-lived issuer identity with dated keys in a JWKS, while releases do use Sigstore through GitHub attestations).
