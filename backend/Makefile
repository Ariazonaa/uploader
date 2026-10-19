# Developer convenience targets. Configure via environment (see .env.example).

.PHONY: dev build test vet migrate user tidy

dev:
	go run ./cmd/uploader serve

build:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o uploader ./cmd/uploader

test:
	go test ./...

vet:
	go vet ./...

migrate:
	go run ./cmd/uploader migrate up

# Usage: make user U=admin E=me@example.com P=secretpassword
user:
	go run ./cmd/uploader user create --username "$(U)" --email "$(E)" --password "$(P)"

tidy:
	go mod tidy
