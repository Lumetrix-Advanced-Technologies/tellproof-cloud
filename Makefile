# Plumbline make targets. Requires Go 1.23+, Node 22+ (tests only, no packages), Docker, psql.
SHELL := /bin/bash
GO_PKGS := ./...

.PHONY: check fmt-check vet staticcheck vuln test vectors db db-test run-server run-worker agent-dry concierge-verify

check: fmt-check vet staticcheck vuln test vectors

fmt-check:
	@out=$$(gofmt -l . | grep -v '^reference/' || true); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

vet:
	go vet $(GO_PKGS)

staticcheck:
	@command -v staticcheck >/dev/null || go install honnef.co/go/tools/cmd/staticcheck@2025.1.1
	staticcheck $(GO_PKGS)

vuln:
	@command -v govulncheck >/dev/null || go install golang.org/x/vuln/cmd/govulncheck@v1.1.4
	govulncheck $(GO_PKGS)

test:
	go test -race $(GO_PKGS)

# Every verifier must agree with spec/vectors/manifest.json and spec/jcs-vectors.json.
vectors:
	@if [ -d internal ]; then go test -race -run 'Vector|JCS' ./internal/...; fi
	node --test reference/verify-reference.test.mjs
	@if [ -f web/verify/verify.test.js ]; then node --test web/verify/verify.test.js; fi
	python3 concierge/verify.py spec/vectors/test-public-key.pem spec/vectors/pass/KST-CTA-0001.dsse.json spec/vectors/pass/KST-CTA-0002.dsse.json spec/vectors/pass/KST-CTA-0003.dsse.json

db:
	docker compose up -d db
	@until docker compose exec -T db pg_isready -U postgres >/dev/null 2>&1; do sleep 1; done
	go run ./cmd/server migrate

db-test:
	psql "$$DATABASE_URL_OWNER" -v ON_ERROR_STOP=0 -f migrations/tests/0001_rls_and_append_only.sql

run-server:
	set -a; source .env.local; set +a; go run ./cmd/server serve

run-worker:
	set -a; source .env.local; set +a; go run ./cmd/server work

agent-dry:
	go run ./cmd/agent --config agent.example.json --dry-run

concierge-verify:
	python3 concierge/verify.py spec/vectors/test-public-key.pem spec/vectors/pass/*.dsse.json
