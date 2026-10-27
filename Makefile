.PHONY: build test vet
build:
	go build -o peerbridge ./cmd/peerbridge
test:
	go test ./...
vet:
	go vet ./...
