package main

import (
	"errors"
	"log"
	"net/http"
	"os"
	"time"

	"opl-cloud/services/fabric/internal/fabric"
	fabrichttp "opl-cloud/services/fabric/internal/http"
	"opl-cloud/services/internal/postgresmigrate"
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
	token, err := internalServiceToken(os.Getenv)
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
	provider, err := selectedProvider(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	handler := fabrichttp.NewServer(fabric.NewServiceWithOperationStore(provider, operationStore), token)
	log.Printf("fabric listening on %s", addr)
	if err := newHTTPServer(addr, handler).ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

func selectedProvider(getenv func(string) string) (fabric.Provider, error) {
	name := getenv("OPL_FABRIC_PROVIDER")
	if name == "" {
		name = "local-docker"
	}
	switch name {
	case "local-docker":
		return fabric.NewLocalDockerProvider(), nil
	case "tencent-tke":
		return fabric.NewTencentProvider(), nil
	default:
		return nil, errors.New("OPL_FABRIC_PROVIDER must be local-docker or tencent-tke")
	}
}

func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr: addr, Handler: handler,
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 10 * time.Minute, IdleTimeout: 2 * time.Minute,
	}
}

func internalServiceToken(getenv func(string) string) (string, error) {
	token := getenv("OPL_INTERNAL_SERVICE_TOKEN")
	if getenv("NODE_ENV") == "production" && token == "" {
		return "", errors.New("OPL_INTERNAL_SERVICE_TOKEN is required in production")
	}
	return token, nil
}

func operationStoreDatabaseURL(getenv func(string) string) (string, error) {
	databaseURL := getenv("DATABASE_URL")
	if getenv("NODE_ENV") == "production" && databaseURL == "" {
		return "", errors.New("DATABASE_URL is required for production Fabric persistence")
	}
	if databaseURL != "" {
		if err := postgresmigrate.ValidateTLS(databaseURL); err != nil {
			return "", err
		}
	}
	return databaseURL, nil
}
