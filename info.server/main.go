package main

import (
	"fmt"
	"log"
	"net/http"
	"strings"
)

// helloHandler greets the user and echoes the requested path
func helloHandler(w http.ResponseWriter, r *http.Request) {
	host := strings.ToLower(strings.Split(r.Host, ":")[0])
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
	mux.HandleFunc("/", helloHandler)

	// Define the network address
	addr := getConfig("address", ":8080")

	fmt.Printf("Server is starting on http://localhost%s\n", addr)

	// Start the server and log any fatal execution errors
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("Server failed to start: %v", err)
	}
}
