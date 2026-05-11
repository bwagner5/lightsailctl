// hello-dockerfile-port3000 verifies the Dockerfile EXPOSE
// auto-detect path: the same trivial server, but with EXPOSE 3000 in
// the Dockerfile, so the agent should pick port 3000 from
// `docker inspect` rather than defaulting to 8080.
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
		port = "3000"
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
