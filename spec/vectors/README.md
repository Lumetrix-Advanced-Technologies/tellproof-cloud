# Test vectors

The contract every verifier must meet: `internal/pack` (Go), `web/verify/verify.js` (browser), `concierge/verify.py` and `reference/verify-reference.test.mjs`.

- `pass/`: three chained packs for the fictional Kestrel Assist (synthetic telemetry). They must verify, in order.
- `fail/`: one envelope per failure mode. Each fails exactly the checks listed in `manifest.json`.
- `manifest.json`: every case, its previous pack (for the chain check) and the expected result object `{signature, subject_digest, chain, merkle, verified}`.
- `test-jwks.json`, `test-public-key.pem`: the issuer key for these vectors.
- `test-private-key.pem`: **TEST ONLY.** It signs nothing but these vectors. Never use it for a real pack, never load it outside tests.
- `make_vectors.py`: regenerates everything with the same key. Only rerun it when the format changes (for example D-008, the predicateType domain), and commit the new files in the same PR as the format change. Never regenerate to make a failing test pass.
- `../jcs-vectors.json`: canonical-JSON cases, including the ones Go's `encoding/json` gets wrong.

Run all of it: `make vectors` (or `node --test reference/verify-reference.test.mjs` on its own).
