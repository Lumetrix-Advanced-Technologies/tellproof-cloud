# Connecting Witnessmark to Vanta

Witnessmark can keep your Vanta account up to date with the result of every evidence objective for each AI system, and file each signed evidence pack as a Vanta document. It uses a private integration in your own Vanta account, so the credentials reach only your Vanta.

## What Witnessmark sends

Each hour, for each AI system with at least one pack, one Vanta resource per evidence objective, taken from the system's latest pack:

| Property | Type | Example |
| --- | --- | --- |
| `objectiveId` | string | `EO-LOG-01` |
| `objective` | string | the objective's short title |
| `system` | string | `Claims Triage Agent (production)` |
| `status` | string | `pass`, `attention` or `fail` |
| `passing` | boolean | `true` when the status is `pass` |
| `packId` | string | `KST-CTA-0003` |
| `packDigest` | string | `sha256:…` |
| `periodStart`, `periodEnd` | string | `2026-09-01`, `2026-09-30` |
| `lastEvaluated` | timestamp | when the pack was issued |
| `verifyUrl` | string | a link that verifies the pack in the browser |

With a document ID, each pack is also uploaded once as `<pack id>.dsse.json`, the same file the app downloads and `witnessmark-verify` checks. Nothing else is sent: no traces, no registers, no prompts or completions.

Each sync sends the whole set, so a resource that is no longer true disappears from Vanta.

## Setting it up

1. In Vanta's developer console, create an application for a private integration. Keep its client ID and client secret.
2. In the application's Resources tab, add a custom resource (for example "AI assurance objective") with the properties above and their types. Copy its resource ID.
3. Optional: create a document in Vanta for the signed packs and copy its ID.
4. In Witnessmark, an admin opens **Settings**, pastes the client ID, the client secret, the resource ID and the document ID, and chooses **Connect Vanta**. The first sync runs at once; the page shows its result.
5. In Vanta, add a custom test on the resource with the rule `passing equals true`. It fails when any objective of any system does.

## How it behaves

- The client secret is sealed on Witnessmark's server and never shown again. To change it, enter the client ID and the new secret; leave both empty to keep the stored ones.
- The verify link in each resource is a share link labelled "Vanta (automatic sync)". It always shows the system's latest pack, lasts a year and is renewed a month before it expires. It is listed on the system's share-links page with its views, so you see when an auditor opens it. If you revoke it, Witnessmark sends no verify link for that system until Vanta is reconnected.
- **Sync now** runs a sync at once. Errors (refused credentials, an unknown resource or document ID, Vanta's rate limit) are shown in Settings in plain words; the next hourly sync tries again.
- **Disconnect** stops the sync, revokes the verify links and deletes the stored credentials. The resources stay in Vanta until you remove the integration there.
