BINARY=kaiju
VERSION?=0.3.0
LDFLAGS=-ldflags "-s -w -X main.version=$(VERSION)"

.PHONY: build build-linux build-darwin build-windows clean test web

web:
	cd web && npm install && npm run build

build: web
	touch internal/gateway/embed.go
	go build $(LDFLAGS) -o $(BINARY) ./cmd/kaiju

build-linux: web
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o $(BINARY)-linux-amd64 ./cmd/kaiju
	GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o $(BINARY)-linux-arm64 ./cmd/kaiju

build-darwin: web
	GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o $(BINARY)-darwin-amd64 ./cmd/kaiju
	GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o $(BINARY)-darwin-arm64 ./cmd/kaiju

build-windows: web
	GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o $(BINARY)-windows-amd64.exe ./cmd/kaiju

all: build-linux build-darwin build-windows

clean:
	rm -f $(BINARY) $(BINARY)-*

test:
	go test ./...
