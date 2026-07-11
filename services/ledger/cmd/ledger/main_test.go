package main

import "testing"

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
