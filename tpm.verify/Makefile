build:
	CGO_ENABLED=0 go build -trimpath -o tpm.verify .

test:
	go vet ./...
	go test ./...

run: build
	sudo ./tpm.verify

.PHONY: build test run
