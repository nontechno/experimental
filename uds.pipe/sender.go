package main

import (
	"io"
	"log"
	"net"
	"os"
)

func main() {
	conn, err := net.Dial("unix", "/tmp/mysock.sock")
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	// copies stdin (piped input) straight to the socket
	if _, err := io.Copy(conn, os.Stdin); err != nil {
		log.Fatal(err)
	}
}