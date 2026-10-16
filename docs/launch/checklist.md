# Launch checklist

What must be true before Plumbline is announced (P6-3). Code-side items were done in Phases 4 to 6; most of what remains needs a person, an account or a decision. Each item says who and what. Tick an item only when it is actually done.

## Decisions

- [ ] **Repository names** (D-050). Rename this repository (for example `plumbline-cloud`) and create the public `plumbline` repository, or choose another public name and export with `-module`.
- [ ] **Predicate type domain** (D-008). Packs still use the placeholder `https://lumetrix.example/plumbline/pack/v0.1`. Decide the real domain before the first real pack; changing it later means new vectors.
- [ ] **Supabase signing keys** (D-007). Confirm the project signs access tokens with asymmetric ES256 keys, not the legacy HS256 secret.

## Accounts and secrets

- [ ] Supabase: project in Sydney, ES256 keys, `PUBLIC_BASE_URL/auth/callback` in the allowed redirect URLs, email templates in our words.
- [ ] AWS: production KMS key (ECC_NIST_P256, SIGN_VERIFY) in ap-southeast-2. The service's role may only `kms:Sign` and `kms:GetPublicKey` on it. Add it with `plumbline-server keys add`.
- [ ] Fly.io: app `plumbline` in `syd`, with secrets `DATABASE_URL`, `KMS_KEY_ID`, `SUPABASE_URL`, `SUPABASE_ANON_KEY`, `PUBLIC_BASE_URL`, `VERIFY_BASE_URL`, `VIEW_HASH_SECRET`, `INTEGRATION_KEY`, `CHECKPOINT_REPO`, `GITHUB_TOKEN` (fine-grained, contents write on the checkpoint repository only), `CLIENT_IP_HEADER=Fly-Client-IP`.
- [ ] Checkpoint repository: public, branch protection on, no force pushes.
- [ ] Cloudflare Pages: deploy `web/` (verify page, `brand.css`, `site.css`, the security and docs pages) from the public repository; set the `plumbline-api` meta tag to the API origin at deploy.

## The public repository and first release (P6-2)

- [ ] `make oss-export`, review the tree, push it to the public repository.
- [ ] In the public repository: private vulnerability reporting on; a `release` environment with required reviewers; ghcr package `plumbline-agent` public.
- [ ] PyPI: trusted publisher for `plumbline` (workflow `sdk-release.yml`, environment `release`). npm: the `@plumbline` scope and an `NPM_TOKEN` secret.
- [ ] Tag `v0.1.0`, check the draft release (binaries, `SHA256SUMS`, attestations with `gh attestation verify`), publish it. Then tag `sdk-v0.1.0`.
- [ ] Check that the agent snippet in the app works with the published image.

## Pages and content (P6-3)

- [x] Security page: `web/security/index.html`.
- [x] Agent docs page: `web/docs/agent/index.html`.
- [x] Test vectors published with a guide for writing an independent verifier: `spec/vectors/README.md`, in the public repository.
- [x] Show HN draft: `docs/launch/hn-post.md`.
- [ ] Before publishing the security page, confirm each row of "Where data is processed" matches the live deployment, and update "What we do not claim" if anything changes (for example a penetration test).
- [ ] A sample pack and share link for the HN post and the site, made from the Kestrel demo data, labelled as a sample.

## Before the announcement

- [ ] `make check` and `make e2e` green on the release commit; CI green in both repositories.
- [ ] One end-to-end run on production: enrol an agent, send traffic, issue a pack, open the share link in a clean browser, run `plumbline-verify` on the downloaded pack, find the pack in the next day's checkpoint.
- [ ] Milestone 1 is about trial partners and is **not** a launch prerequisite. Never mark `M1: PASSED` until its criteria are met.
