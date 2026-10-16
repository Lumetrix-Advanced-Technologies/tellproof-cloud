# Plumbline make targets. Requires Go 1.26+, Node 22+ (tests only, no packages), Docker, psql.
SHELL := /bin/bash
GO_PKGS := ./...
# Lint tools run at a pinned version via `go run`, so a stale binary on PATH is never used
# and go.mod stays free of tool modules. Both need Go 1.26+ (see D-011).
STATICCHECK := honnef.co/go/tools/cmd/staticcheck@2026.2.2
GOVULNCHECK := golang.org/x/vuln/cmd/govulncheck@v1.9.0

.PHONY: check fmt-check vet staticcheck vuln test vectors concierge-test sdk-test db db-test test-db agent-binaries agent-image e2e run-server run-worker agent-dry concierge-verify

check: fmt-check vet staticcheck vuln test vectors concierge-test sdk-test

fmt-check:
	@out=$$(gofmt -l . | grep -v '^reference/' || true); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

vet:
	go vet $(GO_PKGS)
	go vet -tags bedrock $(GO_PKGS)
	go vet -tags e2e ./e2e/

staticcheck:
	go run $(STATICCHECK) $(GO_PKGS)

vuln:
	go run $(GOVULNCHECK) $(GO_PKGS)
	go run $(GOVULNCHECK) -tags bedrock $(GO_PKGS)

test:
	go test -race $(GO_PKGS)
	go test -race -tags bedrock ./internal/usage/...

# Every verifier must agree with spec/vectors/manifest.json, extra-manifest.json, key-manifest.json and spec/jcs-vectors.json.
vectors:
	@if [ -n "$$(go list ./internal/... 2>/dev/null)" ]; then go test -race -run 'Vector|JCS' ./internal/...; fi
	node --test reference/verify-reference.test.mjs
	@if [ -f web/verify/verify.test.js ]; then node --test web/verify/verify.test.js; fi
	python3 concierge/verify.py spec/vectors/test-public-key.pem spec/vectors/pass/KST-CTA-0001.dsse.json spec/vectors/pass/KST-CTA-0002.dsse.json spec/vectors/pass/KST-CTA-0003.dsse.json
	python3 concierge/verify.py spec/vectors/test-public-key.pem spec/vectors/extra/pass-no-disclosure.dsse.json
	python3 -m unittest concierge/tests/test_verify_keys.py

db:
	docker compose up -d db
	@until docker compose exec -T db pg_isready -U postgres >/dev/null 2>&1; do sleep 1; done
	go run ./cmd/server migrate

# Run the Go tests that need Postgres against the compose database (they create
# and drop their own databases). Without PLUMBLINE_TEST_DATABASE_URL they skip.
test-db:
	docker compose up -d db
	@until docker compose exec -T db pg_isready -U postgres >/dev/null 2>&1; do sleep 1; done
	PLUMBLINE_TEST_DATABASE_URL=postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable PLUMBLINE_REQUIRE_DB=1 go test -race ./internal/store/... ./cmd/server/...

db-test:
	psql "$$DATABASE_URL_OWNER" -v ON_ERROR_STOP=0 -f migrations/tests/0001_rls_and_append_only.sql

run-server:
	set -a; source .env.local; set +a; go run ./cmd/server serve

run-worker:
	set -a; source .env.local; set +a; go run ./cmd/server work

agent-dry:
	PLUMBLINE_SALT_SECRET=$${PLUMBLINE_SALT_SECRET:-dry-run-salt-not-a-secret} go run ./cmd/agent --config agent.example.json --dry-run --replay concierge/testdata/otlp-sample.jsonl

concierge-test:
	python3 -m unittest discover -s concierge/tests

sdk-test:
	python3 -m unittest discover -s sdk/python/tests
	node --test sdk/js/test/sdk.test.mjs sdk/js/test/instrument.test.mjs sdk/js/test/real-libs.test.mjs

concierge-verify:
	python3 concierge/verify.py spec/vectors/test-public-key.pem spec/vectors/pass/*.dsse.json

# Static agent binaries for every platform, with checksums (S-5; releases in P6-2).
AGENT_PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64
VERSION ?= dev
agent-binaries:
	@rm -rf dist && mkdir -p dist
	@for p in $(AGENT_PLATFORMS); do os=$${p%/*}; arch=$${p#*/}; ext=; [ $$os = windows ] && ext=.exe; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o dist/plumbline-agent-$$os-$$arch$$ext ./cmd/agent || exit 1; done
	@cd dist && sha256sum plumbline-agent-* > SHA256SUMS && cat SHA256SUMS

agent-image:
	docker build -f cmd/agent/Dockerfile --build-arg VERSION=$(VERSION) -t plumbline-agent:$(VERSION) .

# The whole product end to end (S-6): sample apps -> SDKs -> agent -> server -> pack -> verify page in headless Chrome.
# Needs Postgres (see test-db), python3, node and Chrome.
e2e:
	docker compose up -d db
	@until docker compose exec -T db pg_isready -U postgres >/dev/null 2>&1; do sleep 1; done
	PLUMBLINE_TEST_DATABASE_URL=postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable go test -tags e2e -count=1 -v ./e2e/
