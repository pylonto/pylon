.PHONY: build dev run fmt lint image clean setup hooks test cover cover-html smoke

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

fmt:
	gofmt -w .

lint:
	golangci-lint run ./...

build: fmt
	go build -ldflags "-X github.com/pylonto/pylon/cmd.Version=$(VERSION)" -o pylon .

AGENT_SOURCES := $(wildcard agent/*/Dockerfile agent/*/entrypoint.sh)

dev: fmt .image-stamp
	go build -ldflags "-X github.com/pylonto/pylon/cmd.Version=$(VERSION)" -o $(shell which pylon) .
	systemctl --user restart pylon
	@echo "Deployed $(VERSION) to $$(which pylon) and restarted daemon"

.image-stamp: $(AGENT_SOURCES)
	docker build --no-cache -t ghcr.io/pylonto/agent-claude agent/claude/
	docker build --no-cache -t ghcr.io/pylonto/agent-opencode agent/opencode/
	docker build --no-cache -t pylon/agent-mock agent/mock/
	@touch .image-stamp

image: .image-stamp

run: build
	./pylon start

setup: build hooks
	./pylon setup

hooks:
	git config core.hooksPath .githooks

doctor: build
	./pylon doctor

test:
	go test ./... -race -count=1

cover:
	go test ./... -race -coverprofile=coverage.out
	go tool cover -func=coverage.out

cover-html:
	go test ./... -race -coverprofile=coverage.out
	go tool cover -html=coverage.out -o coverage.html

smoke:
	curl -s -X POST localhost:8080/sentry \
		-H "Content-Type: application/json" \
		-d '{"repo": "https://github.com/kelseyhightower/nocode", "ref": "master", "error": "Unhandled promise rejection in router.js line 42"}' | jq .

clean:
	rm -rf ~/.pylon/jobs/ pylon
