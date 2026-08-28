BIN     := otel.sink
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

.PHONY: all deps build run test vet fmt clean docker emitter

all: build

deps:            ## resolve and pin dependencies
	go mod tidy

build: deps      ## build ./otel.sink
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BIN) .

run: build       ## run with the sample config
	./$(BIN) -config config.yaml

test:            ## unit tests
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

emitter:         ## run the example instrumented app
	cd examples/emitter && go mod tidy && go run . -endpoint localhost:4317

docker:
	docker build -t otel.sink:$(VERSION) .

clean:
	rm -f $(BIN)
	rm -rf data
