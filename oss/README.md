# Plumbline: evidence agent, verifier and SDKs

Plumbline turns an AI product's runtime telemetry into **signed, chained evidence packs** that a bank, insurer or auditor can check themselves, offline and without trusting the vendor or Lumetrix. This repository holds the parts that run on the vendor's side or in the reviewer's hands, so anyone can read exactly what they do:

| Part | What it does |
| --- | --- |
| `plumbline-agent` (`cmd/agent`) | Runs next to the vendor's AI product. Receives OpenTelemetry spans, **drops all message content at parse time**, keeps raw trace IDs local (only salted commitments leave), and sends signed hourly aggregates. `--dry-run` prints exactly what would be sent. |
| SDKs (`sdk/python`, `sdk/js`) | One line, `plumbline.init()`, instruments OpenAI, Anthropic and Bedrock clients and streams spans to the agent. They never read message content and have no dependencies. |
| `plumbline-verify` (`cmd/verify`) | Verifies a pack and its chain offline against the issuer's published keys (JWKS), and proves a pack is in a public daily checkpoint. |
| Verify page (`web/verify`) | The same checks in the browser with Web Crypto, from a share link or from local files. |
| `concierge/verify.py` | A third, independent verifier in Python. |
| `spec/` | Pack and batch schemas, evidence objectives, and the test vectors every verifier must agree on. |

The control plane that evaluates objectives and signs packs is a hosted service and is not in this repository. You do not need to trust it: a pack verifies only if its signature, its chain and its Merkle commitments all hold.

## Install

Download a release from the Releases page, then check it:

```bash
sha256sum --ignore-missing -c SHA256SUMS
gh attestation verify plumbline-verify-linux-amd64 --repo lumetrix-advanced-technologies/plumbline
```

Every binary, `SHA256SUMS` and the release notes carry a signed build-provenance attestation that ties them to this repository's source and release workflow.

The agent also ships as a container, `ghcr.io/lumetrix-advanced-technologies/plumbline-agent`. Use the `plumbline-agent-bedrock-*` binaries if you need Amazon Bedrock usage from CloudWatch.

From source (Go 1.26+):

```bash
go install github.com/lumetrix-advanced-technologies/plumbline/cmd/verify@latest
go install github.com/lumetrix-advanced-technologies/plumbline/cmd/agent@latest
```

## Verify a pack

```bash
plumbline-verify --jwks issuer-jwks.json pack.dsse.json
```

Pass several packs oldest first to check the chain between them; `--json` prints a report. To prove a pack is in a daily checkpoint:

```bash
plumbline-verify checkpoint --jwks issuer-jwks.json [--prev yesterday.json] checkpoint.json pack.dsse.json
```

Exit status 0 means every check passed, 1 a check failed, 2 a usage or input error.

## Run the agent

```bash
docker run -d --name plumbline-agent --restart unless-stopped -p 127.0.0.1:4318:4318 \
  -v plumbline-agent:/data -e PLUMBLINE_SERVER=https://<server> \
  ghcr.io/lumetrix-advanced-technologies/plumbline-agent:latest connect <enrolment-token>
```

Then add one line to the app: `import plumbline; plumbline.init()` (Python) or `import * as plumbline from '@plumbline/sdk'; plumbline.init();` (Node.js). To see what the agent would send, without sending anything:

```bash
make agent-dry
```

## Develop

```bash
make check    # gofmt, vet, staticcheck, govulncheck, tests, every verifier on every vector, SDK tests
```

The tests need Node 22+ and Python 3.10+ with `cryptography`. Go code uses only the standard library, plus the AWS SDK in the `bedrock` build.

Rules that every change keeps:

- **No content leaves the customer.** No code path in the agent or the SDKs may send prompt or completion text, span content or raw trace IDs.
- **The verifiers agree.** Every verifier must give the same result on every file in `spec/vectors/` and `spec/jcs-vectors.json`. New vectors are added, never changed.

## Security

See [SECURITY.md](SECURITY.md) to report a vulnerability privately.

## Licence

Apache License 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
