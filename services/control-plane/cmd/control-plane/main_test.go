package main

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestGatewayPublicEndpoint(t *testing.T) {
	values := map[string]string{"NODE_ENV": "production", "OPL_GATEWAY_PUBLIC_BASE_URL": " https://api.medopl.example/v1/ "}
	if got := gatewayPublicBaseURLFromEnv(func(key string) string { return values[key] }); got != "https://api.medopl.example/v1" {
		t.Fatalf("public Gateway base URL = %q", got)
	}
}

func TestGatewayPublicEndpointRequiresHTTPS(t *testing.T) {
	for _, raw := range []string{
		"http://api.medopl.example/v1", "api.medopl.example/v1", "https://user@example.com/v1",
		"https://api.medopl.example/v1?token=secret", "https://api.medopl.example/v1#secret",
	} {
		t.Run(strings.ReplaceAll(raw, "/", "_"), func(t *testing.T) {
			values := map[string]string{"NODE_ENV": "production", "OPL_GATEWAY_PUBLIC_BASE_URL": raw}
			if got := gatewayPublicBaseURLFromEnv(func(key string) string { return values[key] }); got != "" {
				t.Fatalf("invalid production public URL %q accepted as %q", raw, got)
			}
		})
	}
}

func TestGatewayPublicEndpointHasNoInternalFallback(t *testing.T) {
	for _, values := range []map[string]string{
		{"NODE_ENV": "production", "OPL_SUB2API_BASE_URL": "https://internal.example"},
		{"NODE_ENV": "production", "OPL_SUB2API_BASE_URL": "https://internal.example", "OPL_GATEWAY_PUBLIC_BASE_URL": "https://internal.example/v1"},
		{"NODE_ENV": "production", "OPL_SUB2API_BASE_URL": "https://internal.example", "OPL_GATEWAY_PUBLIC_BASE_URL": "https://gflabtoken.cn/v1"},
	} {
		if got := gatewayPublicBaseURLFromEnv(func(key string) string { return values[key] }); got != "" {
			t.Fatalf("public endpoint leaked internal/fallback URL %q", got)
		}
	}
}

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

func TestInternalServiceTokenRequiredInProduction(t *testing.T) {
	getenv := func(key string) string {
		if key == "NODE_ENV" {
			return "production"
		}
		return ""
	}
	if _, err := internalServiceToken(getenv); err == nil {
		t.Fatal("production Control Plane must reject missing OPL_INTERNAL_SERVICE_TOKEN")
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
