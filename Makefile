.PHONY: build run test test-live fmt image clean

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

clean:
	rm -rf ~/.pylon/jobs/
