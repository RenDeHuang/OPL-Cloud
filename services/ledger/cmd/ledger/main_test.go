package main

import (
	"net/http"
	"testing"
)

func TestHTTPServerHasFiniteTimeouts(t *testing.T) {
	server := newHTTPServer(":8081", http.NotFoundHandler())
	if server.ReadHeaderTimeout <= 0 || server.ReadTimeout <= 0 || server.WriteTimeout <= 0 || server.IdleTimeout <= 0 {
		t.Fatalf("HTTP timeouts must all be finite: %#v", server)
	}
}

func TestStoreDatabaseURLRequiresPostgresInProduction(t *testing.T) {
	getenv := func(key string) string {
		if key == "NODE_ENV" {
			return "production"
		}
		return ""
	}
	if _, err := storeDatabaseURL(getenv); err == nil {
		t.Fatal("production Ledger must reject missing DATABASE_URL")
	}
}

func TestStoreDatabaseURLAllowsMemoryOutsideProduction(t *testing.T) {
	url, err := storeDatabaseURL(func(string) string { return "" })
	if err != nil || url != "" {
		t.Fatalf("development store config = %q, %v", url, err)
	}
}

func TestStoreDatabaseURLRequiresVerifyFullTLS(t *testing.T) {
	for _, databaseURL := range []string{
		"postgresql://opl@db.example/opl",
		"postgresql://opl@db.example/opl?sslmode=disable",
		"postgresql://opl@db.example/opl?sslmode=require",
	} {
		_, err := storeDatabaseURL(func(key string) string {
			if key == "DATABASE_URL" {
				return databaseURL
			}
			return ""
		})
		if err == nil {
			t.Fatalf("unsafe DATABASE_URL %q accepted", databaseURL)
		}
	}
	const safe = "postgresql://opl@db.example/opl?sslmode=verify-full"
	if got, err := storeDatabaseURL(func(key string) string {
		if key == "DATABASE_URL" {
			return safe
		}
		return ""
	}); err != nil || got != safe {
		t.Fatalf("verified DATABASE_URL = %q, %v", got, err)
	}
}

func TestStoreDatabaseURLAllowsOnlyExplicitLoopbackPostgresWithTestGate(t *testing.T) {
	const local = "postgresql://postgres@127.0.0.1:5432/opl_ledger_test?sslmode=disable"
	for _, test := range []struct {
		name    string
		env     map[string]string
		wantErr bool
	}{
		{name: "test gate", env: map[string]string{"OPL_POSTGRES_TESTS": "1"}},
		{name: "gate absent", env: map[string]string{}, wantErr: true},
		{name: "production", env: map[string]string{"NODE_ENV": "production", "OPL_POSTGRES_TESTS": "1"}, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := storeDatabaseURL(func(key string) string {
				if key == "DATABASE_URL" {
					return local
				}
				return test.env[key]
			})
			if test.wantErr && err == nil {
				t.Fatalf("unsafe local DATABASE_URL accepted: %q", got)
			}
			if !test.wantErr && (err != nil || got != local) {
				t.Fatalf("local test DATABASE_URL = %q, %v", got, err)
			}
		})
	}
	for _, unsafe := range []string{
		"postgresql://postgres@db.example/opl?sslmode=disable",
		"postgresql://postgres@127.0.0.1/opl?sslmode=require",
		"postgresql://postgres@127.0.0.1/opl?sslmode=disable&host=10.0.0.1",
	} {
		if localPostgresTestDatabaseURL(func(key string) string {
			if key == "OPL_POSTGRES_TESTS" {
				return "1"
			}
			return ""
		}, unsafe) {
			t.Fatalf("unsafe local test DATABASE_URL %q accepted", unsafe)
		}
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
		t.Fatal("production Ledger must reject missing OPL_INTERNAL_SERVICE_TOKEN")
	}
}

func TestLedgerCapabilityKeyIsRequiredAndSeparatedInProduction(t *testing.T) {
	values := map[string]string{"NODE_ENV": "production", "OPL_LEDGER_CAPABILITY_KEY": "ledger-capability-key-with-at-least-32-characters"}
	key, err := ledgerCapabilityKey(func(name string) string { return values[name] }, "ledger-transport-token")
	if err != nil || key != values["OPL_LEDGER_CAPABILITY_KEY"] {
		t.Fatalf("key=%q err=%v", key, err)
	}
	delete(values, "OPL_LEDGER_CAPABILITY_KEY")
	if _, err := ledgerCapabilityKey(func(name string) string { return values[name] }, "ledger-transport-token"); err == nil {
		t.Fatal("missing capability key accepted")
	}
	values["OPL_LEDGER_CAPABILITY_KEY"] = "ledger-transport-token"
	if _, err := ledgerCapabilityKey(func(name string) string { return values[name] }, "ledger-transport-token"); err == nil {
		t.Fatal("capability key reused transport token")
	}
}
