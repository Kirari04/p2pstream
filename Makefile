.PHONY: all build clean run

# Load .env file if it exists
ifneq (,$(wildcard ./.env))
    include .env
    export
endif

all: build

build:
	@echo "Building server..."
	@mkdir -p bin
	@go build -o bin/server cmd/server/main.go
	@echo "Building agent..."
	@go build -o bin/agent cmd/agent/main.go

run: build kill
	@echo "Starting server and agent..."
	@./bin/server & SERVER_PID=$$!; \
	sleep 1; \
	./bin/agent & AGENT_PID=$$!; \
	echo "Both running. Press Ctrl+C to stop."; \
	trap "kill $$SERVER_PID $$AGENT_PID 2>/dev/null; exit 0" INT TERM; \
	wait $$SERVER_PID $$AGENT_PID

kill:
	@echo "Ensuring previous processes are killed..."
	@-pkill -9 -f bin/server || true
	@-pkill -9 -f bin/agent || true

clean:
	@echo "Cleaning up..."
	@rm -rf bin/
	@rm -rf tmp/
