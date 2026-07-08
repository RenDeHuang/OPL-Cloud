package main

import (
	"log"
	"net/http"
	"os"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
	controlserver "opl-cloud/services/control-plane/internal/server"
)

func main() {
	addr := controlPlaneAddr()
	ledgerURL := os.Getenv("LEDGER_URL")
	fabricURL := os.Getenv("FABRIC_URL")

	service := controlplane.NewService(
		clients.NewLedgerHTTPClient(ledgerURL, nil),
		clients.NewFabricHTTPClient(fabricURL, nil),
	)
	store, err := controlserver.FactStoreFromEnv()
	if err != nil {
		log.Fatal(err)
	}
	handler, err := controlserver.NewPersistentServer(service, store)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("control-plane listening on %s", addr)
	if err := http.ListenAndServe(addr, handler); err != nil {
		log.Fatal(err)
	}
}

func controlPlaneAddr() string {
	addr := os.Getenv("CONTROL_PLANE_ADDR")
	if addr == "" && os.Getenv("PORT") != "" {
		addr = ":" + os.Getenv("PORT")
	}
	if addr == "" {
		addr = ":8787"
	}
	return addr
}
