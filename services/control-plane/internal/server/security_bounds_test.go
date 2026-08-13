package server

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestWorkspaceServiceTargetAcceptsOnlyDNSServiceIdentity(t *testing.T) {
	for _, valid := range []string{"opl-runtime-alpha", "opl-compute-alpha"} {
		target, err := workspaceServiceTarget(valid)
		if err != nil || target.String() != "http://"+valid+":3000" {
			t.Fatalf("workspaceServiceTarget(%q) = %v, %v", valid, target, err)
		}
	}
	for _, invalid := range []string{
		"http://127.0.0.1:8080", "https://runtime.example", "127.0.0.1", "runtime:8080",
		"user@runtime", "runtime/path", "Runtime", "-runtime", "runtime-", "runtime\nadmin", "runtime-alpha", "opl-runtime.alpha",
	} {
		if target, err := workspaceServiceTarget(invalid); err == nil {
			t.Fatalf("workspaceServiceTarget(%q) accepted %v", invalid, target)
		}
	}
}

func TestCreateSessionEvictsOldestActiveSessionAtPerUserBound(t *testing.T) {
	store := newMemoryTableStore()
	app, err := newControlPlaneAppWithStore(store)
	if err != nil {
		t.Fatal(err)
	}
	user := map[string]any{"id": "usr-session-bound", "accountId": "acct-session-bound"}
	now := time.Now().UTC()
	oldestKey := ""
	for index := 0; index < 8; index++ {
		key := sessionLookupKey(fmt.Sprintf("existing-%d", index))
		if index == 0 {
			oldestKey = key
		}
		if err := store.SaveSession(context.Background(), map[string]any{
			"id": key, "userId": user["id"], "csrf": "csrf", "expiresAt": now.Add(time.Duration(index+1) * time.Hour).Format(time.RFC3339),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := app.createSession(user, "delegated-user-token"); err != nil {
		t.Fatal(err)
	}
	sessions, err := store.ListSessionsByUser(context.Background(), "usr-session-bound")
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 8 || sessions[oldestKey] != nil {
		t.Fatalf("bounded sessions = %d, oldest present=%v", len(sessions), sessions[oldestKey] != nil)
	}
}

func TestCreateSupportMappingRejectsPerAccountOverflow(t *testing.T) {
	store := newMemoryTableStore()
	app, err := newControlPlaneAppWithStore(store)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 1000; index++ {
		if err := store.SaveSupportMapping(context.Background(), map[string]any{
			"id": fmt.Sprintf("support-%04d", index), "accountId": "acct-support-bound", "externalTicketId": fmt.Sprintf("EXT-%04d", index),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := app.createSupportMapping(map[string]any{"accountId": "acct-support-bound", "externalTicketId": "EXT-OVERFLOW"}); err == nil || err.Error() != "support_mapping_limit_reached" {
		t.Fatalf("overflow error = %v", err)
	}
}

func TestRetentionWorkerDefaultsOnAndAllowsExplicitDisable(t *testing.T) {
	t.Setenv("OPL_ARCHIVE_RETENTION_WORKER_ENABLED", "")
	if !retentionWorkerEnabled() {
		t.Fatal("retention worker defaulted off")
	}
	t.Setenv("OPL_ARCHIVE_RETENTION_WORKER_ENABLED", "false")
	if retentionWorkerEnabled() {
		t.Fatal("retention worker ignored explicit disable")
	}
}
