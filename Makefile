.PHONY: build run test test-dry fmt image clean setup

fmt:
	gofmt -w .

build: fmt
	go build -o pylon .

image:
	docker build -t pylon/agent-claude agent/claude/
	docker build -t pylon/agent-mock agent/mock/

run: build
	./pylon

test:
	curl -s -X POST localhost:8080/sentry \
		-H "Content-Type: application/json" \
		-d '{"repo": "https://github.com/kelseyhightower/nocode", "ref": "master", "error": "Unhandled promise rejection in router.js line 42"}' | jq .

test-dry:
	curl -s -X POST localhost:8080/mock \
		-H "Content-Type: application/json" \
		-d '{"repo": "https://github.com/kelseyhightower/nocode", "ref": "master", "error": "Test notification flow"}' | jq .

setup: build
	./pylon --setup

clean:
	rm -rf ~/.pylon/jobs/
