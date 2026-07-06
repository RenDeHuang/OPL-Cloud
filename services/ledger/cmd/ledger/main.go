package main

import (
	"log"
	"net/http"
	"os"

	ledgerhttp "opl-cloud/services/ledger/internal/http"
	"opl-cloud/services/ledger/internal/ledger"
)

func main() {
	addr := os.Getenv("LEDGER_ADDR")
	if addr == "" {
		addr = ":8081"
	}

	server := ledgerhttp.NewServer(ledger.NewMemoryStore())
	log.Printf("ledger listening on %s", addr)
	if err := http.ListenAndServe(addr, server); err != nil {
		log.Fatal(err)
	}
}
