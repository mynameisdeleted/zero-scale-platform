package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
)

func main() {
	port := flag.Int("port", 8080, "Port to listen on")
	name := flag.String("name", "mock-service", "Name of the service")
	flag.Parse()

	log.Printf("Starting mock service %s on port %d...", *name, *port)

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("Received request: %s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status": "healthy", "service": "%s", "message": "Hello from the zero-scale consolidated %s!"}`, *name, *name)
	})

	addr := fmt.Sprintf(":%d", *port)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatalf("Mock service failed: %v", err)
	}
}
