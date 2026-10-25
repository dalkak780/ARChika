.PHONY: build test race container

build:
	pnpm --dir web install --frozen-lockfile
	pnpm --dir web build
	go build -trimpath -o bin/archika ./cmd/archika

test: build
	go test ./...
	pnpm --dir web check
	pnpm --dir web test

race: build
	go test -race ./...

container:
	docker build -t archika:local .
