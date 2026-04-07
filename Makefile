.PHONY: build run fmt image clean setup test

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

fmt:
	gofmt -w .

build: fmt
	go build -ldflags "-X github.com/pylonto/pylon/cmd.Version=$(VERSION)" -o pylon .

image:
	docker build -t pylon/agent-claude agent/claude/
	docker build -t pylon/agent-mock agent/mock/

run: build
	./pylon start

setup: build
	./pylon setup

doctor: build
	./pylon doctor

test:
	curl -s -X POST localhost:8080/sentry \
		-H "Content-Type: application/json" \
		-d '{"repo": "https://github.com/kelseyhightower/nocode", "ref": "master", "error": "Unhandled promise rejection in router.js line 42"}' | jq .

clean:
	rm -rf ~/.pylon/jobs/ pylon
