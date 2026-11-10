.PHONY: all fmt vet build test race demo clean

all: fmt vet build race

fmt:
	gofmt -l -w .

vet:
	go vet ./...

build:
	go build ./...

test:
	go test -count=1 ./...

race:
	go test -race -count=1 ./...

demo:
	go run ./cmd/demo -n 100 -w 5 -fail 10

clean:
	go clean -cache -testcache
