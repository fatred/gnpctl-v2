.PHONY: build test vet
build:
	CGO_ENABLED=0 go build -o bin/gnpctl ./cmd/gnpctl
test:
	go test -race ./...
vet:
	go vet ./...
