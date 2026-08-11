package server

import (
	"context"
	"testing"
	"time"
)

func TestRetentionWorkerKeepsTerminalResourcesAndAppliesOwnedRetention(t *testing.T) {
	ctx := context.Background()
	store := NewTestEntStateStore(t, t.TempDir()+"/retention.sqlite").(*postgresEntStateStore)
	app, err := newControlPlaneAppWithStore(store)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	mustStore(t, store.SaveCompute(ctx, map[string]any{"id": "compute-terminal", "accountId": "acct-unit", "status": "destroyed"}))
	mustStore(t, store.SaveAuditEvent(ctx, map[string]any{"id": "audit-old", "action": "test", "createdAt": old.Format(time.RFC3339)}))
	mustStore(t, store.SaveSupportMapping(ctx, map[string]any{"id": "support-old", "createdAt": old.Format(time.RFC3339)}))
	if err := store.client.ProductionE2ERecord.Create().SetID("e2e-old").SetReason("retention-test").SetCreatedAt(old).SetUpdatedAt(old).Exec(ctx); err != nil {
		t.Fatal(err)
	}

	if err := app.runRetentionOnce(ctx); err != nil {
		t.Fatalf("run retention: %v", err)
	}
	if computes, err := store.ListComputes(ctx, ""); err != nil || len(computes) != 1 || stringValue(computes[0]["id"]) != "compute-terminal" {
		t.Fatalf("retention changed terminal resource truth: computes=%#v err=%v", computes, err)
	}
	if count, err := store.client.ArchivedAdminAuditEvent.Query().Count(ctx); err != nil || count != 1 {
		t.Fatalf("archived audit count=%d err=%v", count, err)
	}
	if count, err := store.client.SupportTicketMapping.Query().Count(ctx); err != nil || count != 0 {
		t.Fatalf("support count=%d err=%v", count, err)
	}
	if count, err := store.client.ProductionE2ERecord.Query().Count(ctx); err != nil || count != 0 {
		t.Fatalf("production E2E count=%d err=%v", count, err)
	}
}
