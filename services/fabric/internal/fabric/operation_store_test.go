package fabric

import (
	"strings"
	"testing"
)

func TestPostgresOperationSchemaDefinesFabricOperationsAuditTable(t *testing.T) {
	schema := PostgresOperationSchemaSQL()
	for _, marker := range []string{
		"CREATE TABLE IF NOT EXISTS fabric_operations",
		"operation_id TEXT NOT NULL",
		"caller_service TEXT NOT NULL",
		"resource_kind TEXT NOT NULL",
		"provider_request_id TEXT NOT NULL DEFAULT ''",
		"request_hash TEXT NOT NULL DEFAULT ''",
		"redacted_provider_payload TEXT NOT NULL DEFAULT '{}'",
		"CREATE INDEX IF NOT EXISTS fabric_operations_resource_idx",
	} {
		if !strings.Contains(schema, marker) {
			t.Fatalf("schema missing %q", marker)
		}
	}
	if strings.Contains(schema, "JSONB") {
		t.Fatalf("fabric schema must not keep JSONB fact columns")
	}
}
