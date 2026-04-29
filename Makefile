.PHONY: all build clean generate-sqlc sqlc run

# Load .env file if it exists
ifneq (,$(wildcard ./.env))
    include .env
    export
endif

all: build

build:
	@echo "Building p2pstream..."
	@mkdir -p bin
	@go build -o bin/p2pstream main.go

generate-sqlc:
	@echo "Generating sqlc code..."
	@go tool sqlc generate

sqlc: generate-sqlc

run: build kill
	@echo "Starting server and agent..."
	@./bin/p2pstream server & SERVER_PID=$$!; \
	sleep 1; \
	./bin/p2pstream agent & AGENT_PID=$$!; \
	echo "Both running. Press Ctrl+C to stop."; \
	trap "kill $$SERVER_PID $$AGENT_PID 2>/dev/null; exit 0" INT TERM; \
	wait $$SERVER_PID $$AGENT_PID

kill:
	@echo "Ensuring previous processes are killed..."
	@-pkill -9 -f bin/p2pstream || true

clean:
	@echo "Cleaning up..."
	@rm -rf bin/
	@rm -rf tmp/
