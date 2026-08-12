package main

import (
	"net/http"
	"testing"
	"time"
)

func TestHTTPServerHasFiniteTimeoutsAndPreservesWorkspaceConnections(t *testing.T) {
	server := newHTTPServer(":8787", http.NotFoundHandler())
	if server.ReadHeaderTimeout <= 0 || server.ReadTimeout <= 0 || server.WriteTimeout <= 0 || server.IdleTimeout <= 0 {
		t.Fatalf("HTTP timeouts must all be finite: %#v", server)
	}
	if server.WriteTimeout < time.Hour {
		t.Fatalf("WriteTimeout = %s, want at least one hour for Workspace WebSocket upgrades", server.WriteTimeout)
	}
}

func TestControlPlaneAddrMatchesProductionPortContract(t *testing.T) {
	t.Setenv("CONTROL_PLANE_ADDR", "")
	t.Setenv("PORT", "")
	if got := controlPlaneAddr(); got != ":8787" {
		t.Fatalf("controlPlaneAddr() = %q, want :8787", got)
	}

	t.Setenv("PORT", "8787")
	if got := controlPlaneAddr(); got != ":8787" {
		t.Fatalf("controlPlaneAddr() with PORT = %q, want :8787", got)
	}

	t.Setenv("CONTROL_PLANE_ADDR", ":9000")
	if got := controlPlaneAddr(); got != ":9000" {
		t.Fatalf("controlPlaneAddr() with CONTROL_PLANE_ADDR = %q, want :9000", got)
	}
}

func TestOutboundServiceTokensAreRequiredAndIsolated(t *testing.T) {
	values := map[string]string{
		"NODE_ENV":                 "production",
		"OPL_FABRIC_SERVICE_TOKEN": "fabric-token",
		"OPL_LEDGER_SERVICE_TOKEN": "ledger-token",
	}
	getenv := func(key string) string { return values[key] }

	tokens, err := internalServiceTokensFromEnv(getenv)
	if err != nil {
		t.Fatalf("read isolated outbound tokens: %v", err)
	}
	if tokens.Fabric != "fabric-token" || tokens.Ledger != "ledger-token" {
		t.Fatalf("outbound tokens = %#v", tokens)
	}

	for _, key := range []string{"OPL_FABRIC_SERVICE_TOKEN", "OPL_LEDGER_SERVICE_TOKEN"} {
		t.Run("missing "+key, func(t *testing.T) {
			if _, err := internalServiceTokensFromEnv(func(candidate string) string {
				if candidate == key {
					return ""
				}
				return values[candidate]
			}); err == nil {
				t.Fatalf("production Control Plane accepted missing %s", key)
			}
		})
	}

	values["OPL_LEDGER_SERVICE_TOKEN"] = values["OPL_FABRIC_SERVICE_TOKEN"]
	if _, err := internalServiceTokensFromEnv(getenv); err == nil {
		t.Fatal("Control Plane accepted one shared Fabric/Ledger identity token")
	}
}

func TestOutboundServiceTokensRejectPartialDevelopmentConfiguration(t *testing.T) {
	values := map[string]string{"OPL_FABRIC_SERVICE_TOKEN": "fabric-token"}
	if _, err := internalServiceTokensFromEnv(func(key string) string { return values[key] }); err == nil {
		t.Fatal("Control Plane accepted a partial outbound token configuration")
	}
}

func TestFabricCapabilityKeyIsRequiredAndSeparatedInProduction(t *testing.T) {
	values := map[string]string{
		"NODE_ENV": "production", "OPL_FABRIC_CAPABILITY_KEY": "capability-key-with-at-least-32-characters",
	}
	getenv := func(key string) string { return values[key] }
	key, err := fabricCapabilityKeyFromEnv(getenv, "fabric-transport-token")
	if err != nil || key != values["OPL_FABRIC_CAPABILITY_KEY"] {
		t.Fatalf("Fabric capability key=%q err=%v", key, err)
	}
	delete(values, "OPL_FABRIC_CAPABILITY_KEY")
	if _, err := fabricCapabilityKeyFromEnv(getenv, "fabric-transport-token"); err == nil {
		t.Fatal("production Control Plane accepted a missing Fabric capability key")
	}
	values["OPL_FABRIC_CAPABILITY_KEY"] = "same-secret-with-at-least-32-characters"
	if _, err := fabricCapabilityKeyFromEnv(getenv, values["OPL_FABRIC_CAPABILITY_KEY"]); err == nil {
		t.Fatal("Control Plane accepted a Fabric capability key reused as a transport token")
	}
	if _, err := fabricCapabilityKeyFromEnv(func(string) string { return "" }, "configured-development-transport"); err == nil {
		t.Fatal("Control Plane accepted configured Fabric transport without a capability key")
	}
}

func TestSub2APIConfigRequiredAndBoundedInProduction(t *testing.T) {
	values := map[string]string{
		"NODE_ENV":                       "production",
		"OPL_SUB2API_BASE_URL":           "https://gflabtoken.cn",
		"OPL_SUB2API_ADMIN_EMAIL":        "opl-control-plane@example.test",
		"OPL_SUB2API_ADMIN_PASSWORD":     "secret",
		"OPL_SUB2API_REQUEST_TIMEOUT_MS": "5000",
	}
	getenv := func(key string) string { return values[key] }

	config, err := sub2APIConfigFromEnv(getenv)
	if err != nil {
		t.Fatalf("read complete Sub2API config: %v", err)
	}
	if config.BaseURL != "https://gflabtoken.cn" || config.Timeout != 5*time.Second {
		t.Fatalf("Sub2API config = %#v", config)
	}

	for _, key := range []string{"OPL_SUB2API_BASE_URL", "OPL_SUB2API_ADMIN_EMAIL", "OPL_SUB2API_ADMIN_PASSWORD"} {
		t.Run(key, func(t *testing.T) {
			missing := func(candidate string) string {
				if candidate == key {
					return ""
				}
				return values[candidate]
			}
			if _, err := sub2APIConfigFromEnv(missing); err == nil {
				t.Fatalf("production config should reject missing %s", key)
			}
		})
	}
}
