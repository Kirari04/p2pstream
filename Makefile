.PHONY: all build backend-build clean dev docker-build docker-race-test docker-smoke docker-smoke-clean docker-test frontend-build frontend-install generate generate-proto generate-sqlc run sqlc test

# Load .env file if it exists
ifneq (,$(wildcard ./.env))
    include .env
    export
endif

all: build

generate-proto: frontend-install
	@echo "Generating protobuf code..."
	@go tool buf generate

generate-sqlc:
	@echo "Generating sqlc code..."
	@go tool sqlc generate

generate: generate-proto generate-sqlc

sqlc: generate-sqlc

frontend-install:
	@cd web/management && bun install --frozen-lockfile

frontend-build: frontend-install generate-proto
	@cd web/management && bun run build

backend-build:
	@echo "Building p2pstream backend..."
	@mkdir -p bin
	@go build -o bin/p2pstream main.go

build: frontend-build backend-build

dev: frontend-install generate-proto kill
	@echo "Starting p2pstream development mode..."
	@cd web/management && VITE_MANAGEMENT_PROXY_TARGET=https://127.0.0.1:$${MANAGEMENT_PORT:-8081} VITE_MANAGEMENT_PROXY_SECURE=false VITE_HMR_PROTOCOL=wss VITE_HMR_HOST=localhost VITE_HMR_CLIENT_PORT=$${MANAGEMENT_PORT:-8081} bun run dev & FRONTEND_PID=$$!; \
	BOOTSTRAP_AGENT_ID=$${AGENT_ID:-local-agent} BOOTSTRAP_AGENT_NAME="$${AGENT_NAME:-Local Agent}" BOOTSTRAP_AGENT_TOKEN=$${AGENT_TOKEN:-local-agent-token} MANAGEMENT_UI_DEV_PROXY=http://127.0.0.1:5173 ENV=development go tool air -c .air.toml & SERVER_PID=$$!; \
	MGMT_PORT=$${MANAGEMENT_PORT:-8081}; \
	CA_FILE=$${CONFIG_DIR:-p2pstream-data}/certs/management/ca.crt.pem; \
	for i in $$(seq 1 75); do [ -s "$$CA_FILE" ] && curl --cacert "$$CA_FILE" -fsS https://127.0.0.1:$$MGMT_PORT/ >/dev/null 2>&1 && break; sleep 0.2; done; \
	AGENT_ID=$${AGENT_ID:-local-agent} AGENT_TOKEN=$${AGENT_TOKEN:-local-agent-token} MANAGEMENT_URL=https://127.0.0.1:$$MGMT_PORT MANAGEMENT_CA_FILE=$$CA_FILE go run main.go agent & AGENT_PID=$$!; \
	echo "Management UI: https://localhost:$$MGMT_PORT"; \
	trap "kill $$FRONTEND_PID $$SERVER_PID $$AGENT_PID 2>/dev/null; exit 0" INT TERM; \
	wait $$FRONTEND_PID $$SERVER_PID $$AGENT_PID

run: build kill
	@echo "Starting server and agent..."
	@BOOTSTRAP_AGENT_ID=$${AGENT_ID:-local-agent} BOOTSTRAP_AGENT_NAME="$${AGENT_NAME:-Local Agent}" BOOTSTRAP_AGENT_TOKEN=$${AGENT_TOKEN:-local-agent-token} ./bin/p2pstream server & SERVER_PID=$$!; \
	MGMT_PORT=$${MANAGEMENT_PORT:-8081}; \
	CA_FILE=$${CONFIG_DIR:-p2pstream-data}/certs/management/ca.crt.pem; \
	for i in $$(seq 1 75); do [ -s "$$CA_FILE" ] && curl --cacert "$$CA_FILE" -fsS https://127.0.0.1:$$MGMT_PORT/ >/dev/null 2>&1 && break; sleep 0.2; done; \
	AGENT_ID=$${AGENT_ID:-local-agent} AGENT_TOKEN=$${AGENT_TOKEN:-local-agent-token} MANAGEMENT_URL=https://127.0.0.1:$$MGMT_PORT MANAGEMENT_CA_FILE=$$CA_FILE ./bin/p2pstream agent & AGENT_PID=$$!; \
	echo "Management UI: https://localhost:$$MGMT_PORT"; \
	trap "kill $$SERVER_PID $$AGENT_PID 2>/dev/null; exit 0" INT TERM; \
	wait $$SERVER_PID $$AGENT_PID

docker-build:
	@docker build --target runtime -t p2pstream:local .

docker-test:
	@docker build --target test -t p2pstream:test .

docker-race-test:
	@docker build --target race-test -t p2pstream:race-test .

docker-smoke:
	@echo "Starting Docker smoke test. Dynamic listener ports must be published explicitly; this test publishes 18080, 18081, 18088, 18089, and 18443 on the host."
	@docker compose -f docker-compose.test.yml down -v --remove-orphans
	@docker compose -f docker-compose.test.yml up --build --abort-on-container-exit --exit-code-from smoke

docker-smoke-clean:
	@docker compose -f docker-compose.test.yml down -v --remove-orphans

test:
	@go test ./...
	@cd web/management && bun run typecheck

kill:
	@echo "Ensuring previous processes are killed..."
	@-pkill -9 -f 'bin/p2pstream|tmp/p2pstream-dev' || true

clean:
	@echo "Cleaning up..."
	@rm -rf bin/
	@rm -rf tmp/
	@rm -rf web/management/dist/
