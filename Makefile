.PHONY: build clean test test-coverage fmt lint docker-build run

BINARY  ?= codereview
IMAGE   ?= ghcr.io/divmora/code-review-bot
VERSION ?= 0.0.0-dev
LDFLAGS := -s -w -X github.com/divmora/code-review-bot/internal/version.Version=$(VERSION)

build:
	@go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/codereview

clean:
	@rm -rf bin/ dist/ coverage.out coverage.html

test:
	@go test -race ./...


test-coverage:
	@go test -race -coverprofile=coverage.out -covermode=atomic ./...
	@go tool cover -html=coverage.out -o coverage.html

fmt:
	@gofmt -w .

lint:
	@golangci-lint run ./... || go vet ./...

docker-build:
	@docker build --build-arg VERSION=$(VERSION) -t $(IMAGE):dev .

# Review uncommitted local changes (set CODEREVIEW_MODEL first).
run: build
	@./bin/$(BINARY) review --local
