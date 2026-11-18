.PHONY: build run clean test templ setup serve runner desktop desktop-dist

BINARY_NAME=blooper
BUILD_DIR=./cmd/blooper

build:
	go build -o $(BINARY_NAME) $(BUILD_DIR)

run: build
	./$(BINARY_NAME)

# First-time setup: builds, writes config, optionally sets up a runner.
setup:
	./scripts/setup.sh

# Start the server with the config setup.sh wrote. Loads .env for API keys.
serve: build
	set -a; [ -f .env ] && . ./.env; set +a; ./$(BINARY_NAME) serve -config config/blooper.local.yaml

# Run this computer as a runner (see runner.yaml, written by setup.sh).
runner: build
	./$(BINARY_NAME) runner -config runner.yaml

dev:
	go run $(BUILD_DIR)

test:
	go test ./...

clean:
	rm -f $(BINARY_NAME)

# Cross-compile for Linux AMD64
build-linux:
	GOOS=linux GOARCH=amd64 go build -o $(BINARY_NAME)-linux-amd64 $(BUILD_DIR)

# Install templ CLI (run once)
install-templ:
	go install github.com/a-h/templ/cmd/templ@latest

# Generate templ templates
templ:
	templ generate

# Desktop development: native app with bundled coordinator + runner.
desktop:
	cd desktop && npm ci && npm start

# Native installer for the current macOS/Windows machine.
desktop-dist:
	cd desktop && npm ci && npm run dist
