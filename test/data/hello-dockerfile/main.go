// hello-dockerfile is the minimal end-to-end fixture for the
// Dockerfile strategy: a tiny HTTP server on :8080 that responds 200
// with "ok\n". Paired with the trivial multi-stage Dockerfile in this
// directory; no compose file, so the agent has to synthesize one.
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	http.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	addr := ":" + port
	log.Printf("listening on %s", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatal(err)
	}
}
