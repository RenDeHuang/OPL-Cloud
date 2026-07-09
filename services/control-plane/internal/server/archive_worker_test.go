package server

import (
	"context"
	"testing"
)

func TestArchiveRetentionWorkerArchivesTerminalCurrentStateOnly(t *testing.T) {
	app := newControlPlaneAppEmpty()
	app.resources.computes["compute-dead"] = map[string]any{"id": "compute-dead", "status": "destroyed"}
	app.resources.storages["storage-dead"] = map[string]any{"id": "storage-dead", "status": "destroyed"}
	app.resources.attachments["attach-dead"] = map[string]any{"id": "attach-dead", "status": "detached"}
	app.resources.workspaces["ws-dead"] = map[string]any{"id": "ws-dead", "state": "unrecoverable"}
	app.billing.ledger = []map[string]any{{"id": "ledger-kept"}}

	if err := app.runArchiveRetentionOnce(context.Background()); err != nil {
		t.Fatalf("run archive retention: %v", err)
	}
	if len(app.resources.computes) != 0 || len(app.resources.storages) != 0 || len(app.resources.attachments) != 0 || len(app.resources.workspaces) != 0 {
		t.Fatalf("terminal current state remains: computes=%#v storages=%#v attachments=%#v workspaces=%#v", app.resources.computes, app.resources.storages, app.resources.attachments, app.resources.workspaces)
	}
	if len(app.billing.ledger) != 1 {
		t.Fatalf("ledger accounting facts must be retained: %#v", app.billing.ledger)
	}
}
