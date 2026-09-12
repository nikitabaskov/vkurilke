.PHONY: build web test check run
web:
	npm --prefix web ci --ignore-scripts --no-fund --no-audit
	npm --prefix web run build

build: web
	CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags="-s -w" -o bin/vkurilke ./cmd/app

test: web
	go test ./...

check: web
	go vet ./...
	go test -race ./...

run: build
	./bin/vkurilke
