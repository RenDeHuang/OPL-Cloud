package main

import (
	"log"
	"net/http"
	"os"

	"opl-cloud/services/fabric/internal/fabric"
	fabrichttp "opl-cloud/services/fabric/internal/http"
)

func main() {
	addr := os.Getenv("FABRIC_ADDR")
	if addr == "" {
		addr = ":8082"
	}

	server := fabrichttp.NewServer(fabric.NewService(fabric.NewDryRunProvider()))
	log.Printf("fabric listening on %s", addr)
	if err := http.ListenAndServe(addr, server); err != nil {
		log.Fatal(err)
	}
}
