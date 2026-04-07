.PHONY: build run test fmt

fmt:
	gofmt -w .

build: fmt
	go build -o pylon .

run: build
	./pylon

test:
	curl -X POST localhost:8080/hello -d '{"message": "test"}'
