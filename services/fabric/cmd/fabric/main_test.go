package main

import "testing"

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
