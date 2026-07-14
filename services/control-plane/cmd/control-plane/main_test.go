package main

import (
	"testing"
	"time"
)

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
		"OPL_SUB2API_SUPPORTED_VERSIONS": "0.1.151,0.1.152",
		"OPL_SUB2API_REQUEST_TIMEOUT_MS": "5000",
	}
	getenv := func(key string) string { return values[key] }

	config, err := sub2APIConfigFromEnv(getenv)
	if err != nil {
		t.Fatalf("read complete Sub2API config: %v", err)
	}
	if config.BaseURL != "https://gflabtoken.cn" || len(config.SupportedVersions) != 2 || config.Timeout != 5*time.Second {
		t.Fatalf("Sub2API config = %#v", config)
	}

	for _, key := range []string{"OPL_SUB2API_BASE_URL", "OPL_SUB2API_ADMIN_EMAIL", "OPL_SUB2API_ADMIN_PASSWORD", "OPL_SUB2API_SUPPORTED_VERSIONS"} {
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
