.PHONY: all build backend-build clean dev dev-token-check docker-build docker-race-test docker-smoke docker-smoke-clean docker-server-updater-test docker-test docs-screenshots frontend-build frontend-e2e frontend-sites-e2e frontend-install generate generate-proto generate-sqlc legal-notices run schema-check sqlc test verify verify-clean-tree

# Docker smoke owns its disposable credentials and never loads application .env.
ifeq (,$(filter docker-smoke docker-smoke-clean,$(MAKECMDGOALS)))
# Load .env file if it exists
ifneq (,$(wildcard ./.env))
    include .env
    export
endif
endif

all: build

generate-proto: frontend-install
	@echo "Generating protobuf code..."
	@go tool buf generate --path proto

generate-sqlc:
	@echo "Generating sqlc code..."
	@go tool sqlc generate

generate: generate-proto generate-sqlc

sqlc: generate-sqlc

schema-check:
	@go test ./internal/db -run TestSchemaParity

frontend-install:
	@cd web/management && bun install --frozen-lockfile

frontend-build: frontend-install generate-proto
	@cd web/management && bun run build

frontend-e2e: frontend-install
	@cd web/management && bun run e2e

frontend-sites-e2e: frontend-build
	@cd web/management && bun run e2e:sites

docs-screenshots: frontend-install
	@cd web/management && bun run docs:screenshots

legal-notices: frontend-install
	@scripts/build-legal-notices.sh

backend-build:
	@echo "Building p2pstream backend..."
	@mkdir -p bin
	@go build -o bin/p2pstream main.go

build: frontend-build backend-build

dev-token-check:
	@. ./scripts/local-dev-env.sh

dev: dev-token-check frontend-install generate-proto kill
	@echo "Starting p2pstream development mode..."
	@mkdir -p tmp
	@go build -o ./tmp/p2pstream-agent-dev .
	@. ./scripts/local-dev-env.sh; \
	cd web/management && VITE_MANAGEMENT_PROXY_TARGET=https://127.0.0.1:$${MANAGEMENT_PORT:-8081} VITE_MANAGEMENT_PROXY_SECURE=false bun run dev & FRONTEND_PID=$$!; \
	BOOTSTRAP_AGENT_ID=$${AGENT_ID:-local-agent} BOOTSTRAP_AGENT_NAME="$${AGENT_NAME:-Local Agent}" BOOTSTRAP_AGENT_TOKEN="$$AGENT_TOKEN" MANAGEMENT_BIND_ADDRESS="$$MANAGEMENT_BIND_ADDRESS" MANAGEMENT_UI_DEV_PROXY=http://127.0.0.1:5173 ENV=development go tool air -c .air.toml & SERVER_PID=$$!; \
	MGMT_PORT=$${MANAGEMENT_PORT:-8081}; \
	CA_FILE=$${CONFIG_DIR:-p2pstream-data}/certs/management/ca.crt.pem; \
	for i in $$(seq 1 75); do [ -s "$$CA_FILE" ] && curl --cacert "$$CA_FILE" -fsS https://127.0.0.1:$$MGMT_PORT/ >/dev/null 2>&1 && break; sleep 0.2; done; \
	AGENT_ID=$${AGENT_ID:-local-agent} AGENT_TOKEN="$$AGENT_TOKEN" MANAGEMENT_URL=https://127.0.0.1:$$MGMT_PORT MANAGEMENT_CA_FILE=$$CA_FILE ./tmp/p2pstream-agent-dev agent & AGENT_PID=$$!; \
	echo "Management UI: https://localhost:$$MGMT_PORT"; \
	cleanup() { \
		trap - INT TERM EXIT; \
		repo=$$(pwd); \
		kill $$FRONTEND_PID $$SERVER_PID $$AGENT_PID 2>/dev/null || true; \
		pkill -TERM -P $$SERVER_PID 2>/dev/null || true; \
		pkill -TERM -f "$$repo/tmp/p2pstream-dev server|$$repo/tmp/p2pstream-agent-dev agent" 2>/dev/null || true; \
		sleep 0.5; \
		pkill -KILL -P $$SERVER_PID 2>/dev/null || true; \
		pkill -KILL -f "$$repo/tmp/p2pstream-dev server|$$repo/tmp/p2pstream-agent-dev agent" 2>/dev/null || true; \
	}; \
	trap "cleanup; exit 0" INT TERM; \
	trap "cleanup" EXIT; \
	wait $$FRONTEND_PID $$SERVER_PID $$AGENT_PID

run: dev-token-check build kill
	@echo "Starting server and agent..."
	@. ./scripts/local-dev-env.sh; \
	BOOTSTRAP_AGENT_ID=$${AGENT_ID:-local-agent} BOOTSTRAP_AGENT_NAME="$${AGENT_NAME:-Local Agent}" BOOTSTRAP_AGENT_TOKEN="$$AGENT_TOKEN" MANAGEMENT_BIND_ADDRESS="$$MANAGEMENT_BIND_ADDRESS" ./bin/p2pstream server & SERVER_PID=$$!; \
	MGMT_PORT=$${MANAGEMENT_PORT:-8081}; \
	CA_FILE=$${CONFIG_DIR:-p2pstream-data}/certs/management/ca.crt.pem; \
	for i in $$(seq 1 75); do [ -s "$$CA_FILE" ] && curl --cacert "$$CA_FILE" -fsS https://127.0.0.1:$$MGMT_PORT/ >/dev/null 2>&1 && break; sleep 0.2; done; \
	AGENT_ID=$${AGENT_ID:-local-agent} AGENT_TOKEN="$$AGENT_TOKEN" MANAGEMENT_URL=https://127.0.0.1:$$MGMT_PORT MANAGEMENT_CA_FILE=$$CA_FILE ./bin/p2pstream agent & AGENT_PID=$$!; \
	echo "Management UI: https://localhost:$$MGMT_PORT"; \
	cleanup() { \
		trap - INT TERM EXIT; \
		repo=$$(pwd); \
		kill $$SERVER_PID $$AGENT_PID 2>/dev/null || true; \
		pkill -TERM -f "$$repo/bin/p2pstream server|$$repo/bin/p2pstream agent" 2>/dev/null || true; \
		sleep 0.5; \
		pkill -KILL -f "$$repo/bin/p2pstream server|$$repo/bin/p2pstream agent" 2>/dev/null || true; \
	}; \
	trap "cleanup; exit 0" INT TERM; \
	trap "cleanup" EXIT; \
	wait $$SERVER_PID $$AGENT_PID

docker-build:
	@docker build --target runtime -t p2pstream:local .

docker-test:
	@docker build --target test -t p2pstream:test .

docker-race-test:
	@docker build --target race-test -t p2pstream:race-test .

docker-smoke:
	@scripts/run-docker-smoke.sh

docker-server-updater-test:
	@scripts/test-server-updater.sh

docker-smoke-clean:
	@scripts/run-docker-smoke.sh clean

test:
	@go test ./...
	@cd third_party/yamux && go test ./...
	@cd web/management && bun run typecheck

verify-clean-tree:
	@status="$$(git status --porcelain --untracked-files=all)"; \
	if [ -n "$$status" ]; then \
		echo "verify requires a clean working tree:" >&2; \
		printf '%s\n' "$$status" >&2; \
		exit 1; \
	fi

verify: verify-clean-tree
	@$(MAKE) generate
	@$(MAKE) verify-clean-tree
	@sh -n scripts/setup-server-updater.sh
	@sh -n scripts/local-dev-env.sh
	@for script in scripts/install-agent.sh scripts/uninstall-agent.sh scripts/install-server-updater.sh scripts/server-updater-compose.sh scripts/test-server-updater.sh scripts/run-docker-smoke.sh scripts/test-sites-e2e-server.sh; do \
		bash -n "$$script" || exit 1; \
	done
	@scripts/test-agent-lifecycle.sh
	@python3 -I scripts/test-server-updater-host.py
	@python3 -I scripts/test-server-lifecycle-harness.py
	@python3 -I scripts/test-setup-server-updater.py
	@python3 -I scripts/test-docker-smoke-runner.py
	@python3 -I scripts/test-local-fixtures.py
	@go test ./...
	@cd third_party/yamux && go test ./... && go test -race -short ./...
	@go vet ./...
	@cd web/management && bun run test
	@cd web/management && bun run typecheck
	@cd web/management && bun run build

kill:
	@echo "Ensuring previous processes are killed..."
	@-repo=$$(pwd); \
	pkill -TERM -f '[g]o tool air -c .air.toml|/go-build/.*/[a]ir -c .air.toml|[b]un run --bun vite --host 127.0.0.1 --port 5173|[n]ode .*vite --host 127.0.0.1 --port 5173' 2>/dev/null || true; \
	pkill -TERM -f "$$repo/bin/p2pstream|$$repo/tmp/p2pstream-dev|$$repo/tmp/p2pstream-agent-dev|[g]o run main.go agent|/go-build/.*/[m]ain agent" 2>/dev/null || true; \
	if command -v ss >/dev/null 2>&1; then \
		for port in 8081 8088 8089 5173; do \
			pids=$$(ss -H -ltnp "sport = :$$port" 2>/dev/null | sed -n 's/.*pid=\([0-9][0-9]*\).*/\1/p' | sort -u); \
			[ -z "$$pids" ] || kill -TERM $$pids 2>/dev/null || true; \
		done; \
	fi; \
	sleep 0.5; \
	pkill -KILL -f '[g]o tool air -c .air.toml|/go-build/.*/[a]ir -c .air.toml|[b]un run --bun vite --host 127.0.0.1 --port 5173|[n]ode .*vite --host 127.0.0.1 --port 5173' 2>/dev/null || true; \
	pkill -KILL -f "$$repo/bin/p2pstream|$$repo/tmp/p2pstream-dev|$$repo/tmp/p2pstream-agent-dev|[g]o run main.go agent|/go-build/.*/[m]ain agent" 2>/dev/null || true; \
	if command -v ss >/dev/null 2>&1; then \
		for port in 8081 8088 8089 5173; do \
			pids=$$(ss -H -ltnp "sport = :$$port" 2>/dev/null | sed -n 's/.*pid=\([0-9][0-9]*\).*/\1/p' | sort -u); \
			[ -z "$$pids" ] || kill -KILL $$pids 2>/dev/null || true; \
		done; \
	fi

clean:
	@echo "Cleaning up..."
	@rm -rf bin/
	@rm -rf tmp/
	@rm -rf web/management/dist/
