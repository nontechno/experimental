package main

import (
	"fmt"
	"io"
	"log"
	"os"

	"htmlconf"
)

// Reads HTML from stdin (or a file path arg) and prints Confluence storage
// format to stdout.
//
//	go run ./cmd/htmlconf < page.html
//	go run ./cmd/htmlconf page.html
func main() {
	var input []byte
	var err error

	if len(os.Args) > 1 {
		input, err = os.ReadFile(os.Args[1])
	} else {
		input, err = io.ReadAll(os.Stdin)
	}
	if err != nil {
		log.Fatalf("reading input: %v", err)
	}

	storage, err := htmlconf.Convert(string(input))
	if err != nil {
		log.Fatalf("convert: %v", err)
	}

	fmt.Println(storage)
}
