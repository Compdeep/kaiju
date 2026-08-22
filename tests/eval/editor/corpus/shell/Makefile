BINARY := app
VERSION := 0.3.1
GOFLAGS := -trimpath

.PHONY: build test clean lint

build:
	go build $(GOFLAGS) -ldflags "-X main.version=$(VERSION)" -o $(BINARY) ./cmd/app

test:
	go test ./...

lint:
	golangci-lint run

clean:
	rm -f $(BINARY)
