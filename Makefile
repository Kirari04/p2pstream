.PHONY: all build clean run

all: build

build:
	@echo "Building server..."
	@mkdir -p bin
	@go build -o bin/server cmd/server/main.go
	@echo "Building agent..."
	@go build -o bin/agent cmd/agent/main.go

run: build
	@echo "Starting server and agent..."
	@./bin/server & SERVER_PID=$$!; \
	sleep 1; \
	./bin/agent & AGENT_PID=$$!; \
	echo "Both running. Press Ctrl+C to stop."; \
	trap "kill $$SERVER_PID $$AGENT_PID 2>/dev/null; exit 0" INT TERM; \
	wait $$SERVER_PID $$AGENT_PID

clean:
	@echo "Cleaning up..."
	@rm -rf bin/
	@rm -rf tmp/
