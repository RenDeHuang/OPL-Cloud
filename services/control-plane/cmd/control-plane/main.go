package main

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
	controlserver "opl-cloud/services/control-plane/internal/server"
)

func main() {
	addr := controlPlaneAddr()
	ledgerURL := os.Getenv("LEDGER_URL")
	fabricURL := os.Getenv("FABRIC_URL")
	token, err := internalServiceToken(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	sub2APIConfig, err := sub2APIConfigFromEnv(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	var sub2API clients.Sub2APIClient
	if sub2APIConfig.BaseURL != "" {
		sub2API, err = clients.NewSub2APIHTTPClient(sub2APIConfig, nil)
		if err != nil {
			log.Fatal(err)
		}
	}

	service := controlplane.NewService(
		clients.NewLedgerHTTPClient(ledgerURL, token, nil),
		clients.NewFabricHTTPClient(fabricURL, token, nil),
		sub2API,
	)
	store, err := controlserver.StateStoreFromEnv()
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

func sub2APIConfigFromEnv(getenv func(string) string) (clients.Sub2APIConfig, error) {
	required := []string{
		"OPL_SUB2API_BASE_URL",
		"OPL_SUB2API_ADMIN_EMAIL",
		"OPL_SUB2API_ADMIN_PASSWORD",
		"OPL_SUB2API_SUPPORTED_VERSIONS",
	}
	missing := make([]string, 0, len(required))
	configured := 0
	for _, key := range required {
		if strings.TrimSpace(getenv(key)) == "" {
			missing = append(missing, key)
		} else {
			configured++
		}
	}
	if len(missing) > 0 {
		if getenv("NODE_ENV") == "production" || configured > 0 {
			return clients.Sub2APIConfig{}, fmt.Errorf("missing required Sub2API configuration: %s", strings.Join(missing, ", "))
		}
		return clients.Sub2APIConfig{}, nil
	}
	timeoutMS := 5000
	if raw := strings.TrimSpace(getenv("OPL_SUB2API_REQUEST_TIMEOUT_MS")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 || parsed > 30_000 {
			return clients.Sub2APIConfig{}, errors.New("OPL_SUB2API_REQUEST_TIMEOUT_MS must be between 1 and 30000")
		}
		timeoutMS = parsed
	}
	return clients.Sub2APIConfig{
		BaseURL:           strings.TrimSpace(getenv("OPL_SUB2API_BASE_URL")),
		AdminEmail:        strings.TrimSpace(getenv("OPL_SUB2API_ADMIN_EMAIL")),
		AdminPassword:     getenv("OPL_SUB2API_ADMIN_PASSWORD"),
		SupportedVersions: strings.Split(getenv("OPL_SUB2API_SUPPORTED_VERSIONS"), ","),
		Timeout:           time.Duration(timeoutMS) * time.Millisecond,
	}, nil
}

func internalServiceToken(getenv func(string) string) (string, error) {
	token := getenv("OPL_INTERNAL_SERVICE_TOKEN")
	if getenv("NODE_ENV") == "production" && token == "" {
		return "", errors.New("OPL_INTERNAL_SERVICE_TOKEN is required in production")
	}
	return token, nil
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
