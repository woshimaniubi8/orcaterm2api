BINARY := orcaterm2api
VERSION := 0.1.0

.PHONY: build test vet run clean docker

build:
	go build -trimpath -ldflags "-s -w" -o $(BINARY)$(if $(OS),.exe,) ./cmd/server

test:
	go test ./...

vet:
	go vet ./...

run:
	go run ./cmd/server -config config.json

clean:
	rm -f $(BINARY) $(BINARY).exe

docker:
	docker build -t orcaterm2api:$(VERSION) .
