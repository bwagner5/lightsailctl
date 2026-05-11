// hello-go is the zero-config buildpack fixture: a tiny HTTP server
// with no Dockerfile and no compose file. The agent's buildpack
// strategy detects go.mod, runs `pack build` against
// paketobuildpacks/builder-jammy-base, and synthesizes a compose
// file pointing at the resulting image. Paketo's Go buildpack
// builds the binary; the run image (paketobuildpacks/run-jammy-base)
// runs it on PORT=8080.
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
		fmt.Fprintln(w, "ok from buildpacks")
	})
	addr := ":" + port
	log.Printf("listening on %s", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatal(err)
	}
}
