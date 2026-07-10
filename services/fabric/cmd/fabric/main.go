package main

import (
	"errors"
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

	databaseURL, err := operationStoreDatabaseURL(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	operationStore := fabric.OperationStore(fabric.NewMemoryOperationStore())
	if databaseURL != "" {
		store, err := fabric.NewPostgresOperationStore(databaseURL)
		if err != nil {
			log.Fatal(err)
		}
		operationStore = store
	}
	server := fabrichttp.NewServer(fabric.NewServiceWithOperationStore(fabric.NewTencentProvider(), operationStore))
	log.Printf("fabric listening on %s", addr)
	if err := http.ListenAndServe(addr, server); err != nil {
		log.Fatal(err)
	}
}

func operationStoreDatabaseURL(getenv func(string) string) (string, error) {
	databaseURL := getenv("DATABASE_URL")
	if getenv("NODE_ENV") == "production" && databaseURL == "" {
		return "", errors.New("DATABASE_URL is required for production Fabric persistence")
	}
	return databaseURL, nil
}
