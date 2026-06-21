BINARY := musiccontestbot

.PHONY: build build-pi test fmt vet

build:
	go build -o bin/$(BINARY) ./cmd/musiccontestbot

build-pi:
	GOOS=linux GOARCH=arm GOARM=6 CGO_ENABLED=0 go build -o bin/$(BINARY)-pi ./cmd/musiccontestbot

test:
	go test ./...

fmt:
	gofmt -l .

vet:
	go vet ./...
