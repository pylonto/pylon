.PHONY: build run test fmt image clean

fmt:
	gofmt -w .

build: fmt
	go build -o pylon .

image:
	docker build -t pylon/agent-claude agent/claude/

run: build
	./pylon

test:
	curl -X POST localhost:8080/sentry \
		-H "Content-Type: application/json" \
		-d '{"repo": "https://github.com/expressjs/express", "ref": "master", "error": "Unhandled promise rejection in router.js line 42"}'

clean:
	rm -rf ~/.pylon/jobs/
