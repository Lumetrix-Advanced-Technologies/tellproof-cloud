Review the current diff against Tellproof's non-negotiable rules in `CLAUDE.md` before it goes to the human reviewer.

Check and report each as pass or fail with file and line:
1. No path in the agent can send span content, prompt or completion text, or raw trace IDs. Message attributes are dropped at parse time.
2. Changes to jcs, dsse, merkle, pack or web/verify still pass all vectors; no vector file was edited.
3. No new third-party module, npm package, framework or build step.
4. No UPDATE, DELETE or TRUNCATE against evidence tables; no edited migration.
5. No secrets, tokens, JWTs or batch payloads in logs.
6. No ISO/IEC 42001 text reproduced.
7. Untrusted strings never reach innerHTML; status always shown with an icon and a word.
Do not fix anything in this command; only report.
