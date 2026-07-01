package main

import (
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

// helloHandler greets the user and echoes the requested path
func rootHandler(w http.ResponseWriter, r *http.Request) {
	host := strings.ToLower(strings.Split(r.Host, ":")[0])
	path := r.URL.Path
	start := time.Now()
	defer func() {
		took := time.Since(start)
		fmt.Printf("[%v] %s: %s\n", took.Microseconds(), host, path)
	}()

	switch host {
	case "acro":
		acroHandler(w, r)
		return
	}
	fmt.Fprintf(w, "unhandled: method [%v], host [%v], path [%v]", r.Method, r.Host, r.URL.Path)
}

func main() {
	loadConfig()
	acroInit()

	// Create a new request multiplexer (router)
	mux := http.NewServeMux()

	// Register the handler function for the root path
	mux.HandleFunc("/", rootHandler)

	// Define the network address
	addr := getConfig("address", ":8080")

	fmt.Printf("Server is starting on http://localhost%s\n", addr)

	// Start the server and log any fatal execution errors
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("Server failed to start: %v", err)
	}
}
