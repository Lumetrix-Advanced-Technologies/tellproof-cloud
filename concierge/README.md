# Concierge CLI (Python, until Milestone 1)

Used in weeks 1–6 to hand-build packs for design partners while we test whether people pay. Frozen after M1: bug fixes only, each with a new vector so the Go code inherits it.

- `verify.py`: offline verifier for one or more packs, oldest first. Checks signature, subject digest, chain and the disclosed trace's Merkle proof.
  `python3 concierge/verify.py spec/vectors/test-public-key.pem spec/vectors/pass/*.dsse.json`
- To build (tasks C-1 to C-4 in `docs/build-plan.md`): `evidencepack.py ingest | evaluate | sign | share`.

Requirements: Python 3.12, `cryptography`, and `boto3` once KMS signing is added. The float handling in `verify.py` covers integers and simple decimals only; keep concierge packs to integers and one-decimal percentages, or port the full RFC 8785 number rules from `docs/architecture.md` §2.1.
