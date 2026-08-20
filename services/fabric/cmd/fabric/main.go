package main

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
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
	authConfig, err := fabricServerAuthFromEnv(os.Getenv)
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
	handler := fabrichttp.NewServerWithAuth(fabric.NewServiceWithOperationStore(provider, operationStore), authConfig)
	log.Printf("fabric listening on %s", addr)
	if err := newHTTPServer(addr, handler).ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

func selectedProvider(getenv func(string) string) (fabric.Provider, error) {
	name := strings.TrimSpace(getenv("OPL_FABRIC_PROVIDER"))
	if name == "" {
		return nil, errors.New("OPL_FABRIC_PROVIDER is required")
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

func fabricServerAuthFromEnv(getenv func(string) string) (fabrichttp.ServerAuthConfig, error) {
	config := fabrichttp.ServerAuthConfig{
		ControlPlaneToken: strings.TrimSpace(getenv("OPL_INTERNAL_SERVICE_TOKEN")),
		RunnerToken:       strings.TrimSpace(getenv("OPL_FABRIC_RUNNER_SERVICE_TOKEN")),
		CapabilityKey:     strings.TrimSpace(getenv("OPL_FABRIC_CAPABILITY_KEY")),
	}
	configured := 0
	for _, value := range []string{config.ControlPlaneToken, config.RunnerToken, config.CapabilityKey} {
		if value != "" {
			configured++
		}
	}
	if getenv("NODE_ENV") == "production" || configured > 0 {
		missing := make([]string, 0, 3)
		if config.ControlPlaneToken == "" {
			missing = append(missing, "OPL_INTERNAL_SERVICE_TOKEN")
		}
		if config.RunnerToken == "" {
			missing = append(missing, "OPL_FABRIC_RUNNER_SERVICE_TOKEN")
		}
		if len(config.CapabilityKey) < 32 {
			missing = append(missing, "OPL_FABRIC_CAPABILITY_KEY (32+ characters)")
		}
		if len(missing) > 0 {
			return fabrichttp.ServerAuthConfig{}, fmt.Errorf("missing required Fabric authorization configuration: %s", strings.Join(missing, ", "))
		}
	}
	if config.ControlPlaneToken != "" && (config.ControlPlaneToken == config.RunnerToken || config.ControlPlaneToken == config.CapabilityKey || config.RunnerToken == config.CapabilityKey) {
		return fabrichttp.ServerAuthConfig{}, errors.New("Fabric transport, runner, and capability credentials must be distinct")
	}
	return config, nil
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
