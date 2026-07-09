package server

import (
	"context"
	"testing"
)

func TestArchiveRetentionWorkerArchivesTerminalCurrentStateOnly(t *testing.T) {
	app := newControlPlaneAppEmpty()
	mustStore(t, app.tables.SaveCompute(context.Background(), map[string]any{"id": "compute-dead", "status": "destroyed"}))
	mustStore(t, app.tables.SaveStorage(context.Background(), map[string]any{"id": "storage-dead", "status": "destroyed"}))
	mustStore(t, app.tables.SaveAttachment(context.Background(), map[string]any{"id": "attach-dead", "status": "detached"}))
	mustStore(t, app.tables.SaveWorkspace(context.Background(), map[string]any{"id": "ws-dead", "state": "unrecoverable"}))
	mustStore(t, app.tables.SaveLedgerEntry(context.Background(), map[string]any{"id": "ledger-kept"}))

	if err := app.runArchiveRetentionOnce(context.Background()); err != nil {
		t.Fatalf("run archive retention: %v", err)
	}
	if len(app.listComputes("")) != 0 || len(app.listStorages("")) != 0 || len(app.listAttachments("")) != 0 || len(app.listWorkspaces("")) != 0 {
		t.Fatalf("terminal current state remains")
	}
	ledger, err := app.tables.ListLedger(context.Background(), "")
	mustStore(t, err)
	if len(ledger) != 1 {
		t.Fatalf("ledger accounting facts must be retained: %#v", ledger)
	}
}
