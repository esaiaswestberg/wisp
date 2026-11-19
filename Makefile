.PHONY: test build run image compose-up

test:
	CGO_ENABLED=0 go test -tags sqlite_fts5 ./...

build:
	CGO_ENABLED=0 go build -tags sqlite_fts5 -trimpath -ldflags="-s -w" ./cmd/blog

run:
	go run -tags sqlite_fts5 ./cmd/blog

image:
	docker build -t wisp:local .

compose-up:
	docker compose up -d --build
