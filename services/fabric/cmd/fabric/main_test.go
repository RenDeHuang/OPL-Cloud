package main

import (
	"net/http"
	"testing"
	"time"
)

func TestHTTPServerHasFiniteTimeouts(t *testing.T) {
	server := newHTTPServer(":8082", http.NotFoundHandler())
	if server.ReadHeaderTimeout <= 0 || server.ReadTimeout <= 0 || server.WriteTimeout <= 0 || server.IdleTimeout <= 0 {
		t.Fatalf("HTTP timeouts must all be finite: %#v", server)
	}
	if server.WriteTimeout < 10*time.Minute {
		t.Fatalf("HTTP write timeout = %s, want at least 10m", server.WriteTimeout)
	}
}

func TestOperationStoreDatabaseURLRequiresPostgresInProduction(t *testing.T) {
	getenv := func(key string) string {
		if key == "NODE_ENV" {
			return "production"
		}
		return ""
	}
	if _, err := operationStoreDatabaseURL(getenv); err == nil {
		t.Fatal("production Fabric must reject missing DATABASE_URL")
	}
}

func TestOperationStoreDatabaseURLAllowsMemoryOutsideProduction(t *testing.T) {
	url, err := operationStoreDatabaseURL(func(string) string { return "" })
	if err != nil || url != "" {
		t.Fatalf("development store config = %q, %v", url, err)
	}
}

func TestOperationStoreDatabaseURLRequiresVerifyFullTLS(t *testing.T) {
	for _, databaseURL := range []string{
		"postgresql://opl@db.example/opl",
		"postgresql://opl@db.example/opl?sslmode=disable",
		"postgresql://opl@db.example/opl?sslmode=require",
	} {
		_, err := operationStoreDatabaseURL(func(key string) string {
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
	if got, err := operationStoreDatabaseURL(func(key string) string {
		if key == "DATABASE_URL" {
			return safe
		}
		return ""
	}); err != nil || got != safe {
		t.Fatalf("verified DATABASE_URL = %q, %v", got, err)
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
		t.Fatal("production Fabric must reject missing OPL_INTERNAL_SERVICE_TOKEN")
	}
}
