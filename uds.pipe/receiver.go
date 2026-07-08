package main

import (
	"io"
	"log"
	"net"
	"os"
)

const SockAddr = "/tmp/watch/echo.sock"

// echoServer reads from the connection and writes the same data back (echo).
func echoServer(c net.Conn) {
	log.Printf("Client connected [%s]", c.RemoteAddr().Network())
	defer c.Close()
	// Copy data from the client connection to the client connection (echo)
	if _, err := io.Copy(c, c); err != nil && err != io.EOF {
		log.Printf("Error handling connection: %v", err)
	}
	log.Println("Client disconnected")
}

func main() {
	// Clean up old socket file if it exists
	if err := os.RemoveAll(SockAddr); err != nil {
		log.Fatal(err)
	}

	// Listen on a Unix domain socket
	l, err := net.Listen("unix", SockAddr)
	if err != nil {
		log.Fatal("listen error:", err)
	}
	defer l.Close()
	log.Printf("Server listening on %s", SockAddr)

	for {
		// Accept new connections, dispatching them to echoServer in a goroutine
		conn, err := l.Accept()
		if err != nil {
			log.Fatal("accept error:", err)
		}
		go echoServer(conn)
	}
}
