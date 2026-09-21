.PHONY: build run clean test templ

BINARY_NAME=blooper
BUILD_DIR=./cmd/blooper

build:
	go build -o $(BINARY_NAME) $(BUILD_DIR)

run: build
	./$(BINARY_NAME)

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
