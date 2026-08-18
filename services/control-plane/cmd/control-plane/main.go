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
	outboundTokens, err := internalServiceTokensFromEnv(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	fabricCapabilityKey, err := fabricCapabilityKeyFromEnv(os.Getenv, outboundTokens.Fabric)
	if err != nil {
		log.Fatal(err)
	}
	ledgerCapabilityKey, err := ledgerCapabilityKeyFromEnv(os.Getenv, outboundTokens.Ledger)
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
		clients.NewLedgerHTTPClientWithCapability(ledgerURL, outboundTokens.Ledger, ledgerCapabilityKey, nil),
		clients.NewFabricHTTPClientWithCapability(fabricURL, outboundTokens.Fabric, fabricCapabilityKey, nil),
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
	if err := newHTTPServer(addr, handler).ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

func ledgerCapabilityKeyFromEnv(getenv func(string) string, ledgerTransportToken string) (string, error) {
	key := strings.TrimSpace(getenv("OPL_LEDGER_CAPABILITY_KEY"))
	if (getenv("NODE_ENV") == "production" || ledgerTransportToken != "" || key != "") && len(key) < 32 {
		return "", errors.New("OPL_LEDGER_CAPABILITY_KEY must contain at least 32 characters when Ledger transport is configured")
	}
	if key != "" && key == ledgerTransportToken {
		return "", errors.New("Ledger capability key must be distinct from its transport token")
	}
	return key, nil
}

func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr: addr, Handler: handler,
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: time.Hour, IdleTimeout: 2 * time.Minute,
	}
}

func sub2APIConfigFromEnv(getenv func(string) string) (clients.Sub2APIConfig, error) {
	mode := strings.TrimSpace(getenv("OPL_DEPLOYMENT_MODE"))
	if mode == "" {
		mode = "platform_owned"
	}
	required := []string{
		"OPL_SUB2API_BASE_URL",
	}
	if mode == "customer_owned" {
		required = append(required, "OPL_SUB2API_USER_EMAIL", "OPL_SUB2API_USER_PASSWORD")
	} else {
		required = append(required, "OPL_SUB2API_ADMIN_EMAIL", "OPL_SUB2API_ADMIN_PASSWORD")
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
		BaseURL:       strings.TrimSpace(getenv("OPL_SUB2API_BASE_URL")),
		AdminEmail:    strings.TrimSpace(getenv("OPL_SUB2API_ADMIN_EMAIL")),
		AdminPassword: getenv("OPL_SUB2API_ADMIN_PASSWORD"),
		UserEmail:     strings.TrimSpace(getenv("OPL_SUB2API_USER_EMAIL")),
		UserPassword:  getenv("OPL_SUB2API_USER_PASSWORD"),
		Timeout:       time.Duration(timeoutMS) * time.Millisecond,
	}, nil
}

type internalServiceTokens struct {
	Fabric string
	Ledger string
}

func internalServiceTokensFromEnv(getenv func(string) string) (internalServiceTokens, error) {
	tokens := internalServiceTokens{
		Fabric: strings.TrimSpace(getenv("OPL_FABRIC_SERVICE_TOKEN")),
		Ledger: strings.TrimSpace(getenv("OPL_LEDGER_SERVICE_TOKEN")),
	}
	configured := 0
	if tokens.Fabric != "" {
		configured++
	}
	if tokens.Ledger != "" {
		configured++
	}
	if getenv("NODE_ENV") == "production" || configured > 0 {
		missing := make([]string, 0, 2)
		if tokens.Fabric == "" {
			missing = append(missing, "OPL_FABRIC_SERVICE_TOKEN")
		}
		if tokens.Ledger == "" {
			missing = append(missing, "OPL_LEDGER_SERVICE_TOKEN")
		}
		if len(missing) > 0 {
			return internalServiceTokens{}, fmt.Errorf("missing required outbound service token: %s", strings.Join(missing, ", "))
		}
	}
	if tokens.Fabric != "" && tokens.Fabric == tokens.Ledger {
		return internalServiceTokens{}, errors.New("Fabric and Ledger outbound service tokens must be distinct")
	}
	return tokens, nil
}

func fabricCapabilityKeyFromEnv(getenv func(string) string, fabricTransportToken string) (string, error) {
	key := strings.TrimSpace(getenv("OPL_FABRIC_CAPABILITY_KEY"))
	if (getenv("NODE_ENV") == "production" || fabricTransportToken != "" || key != "") && len(key) < 32 {
		return "", errors.New("OPL_FABRIC_CAPABILITY_KEY must contain at least 32 characters when Fabric transport is configured")
	}
	if key != "" && key == fabricTransportToken {
		return "", errors.New("Fabric capability key must be distinct from its transport token")
	}
	return key, nil
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
