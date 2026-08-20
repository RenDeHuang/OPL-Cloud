package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"
)

func TestWorkspaceServiceTargetAcceptsOnlyDNSServiceIdentity(t *testing.T) {
	raw, err := os.ReadFile("../../../../packages/contracts/opl-cloud-workspace-runtime-abi-contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		WorkspaceWebUI struct {
			Port int `json:"port"`
		} `json:"workspaceWebUI"`
	}
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatal(err)
	}
	if workspaceRuntimeWebUIPort != strconv.Itoa(contract.WorkspaceWebUI.Port) {
		t.Fatalf("Control Plane Workspace Runtime port=%s, contract=%d", workspaceRuntimeWebUIPort, contract.WorkspaceWebUI.Port)
	}
	for _, valid := range []string{"opl-runtime-alpha", "opl-compute-alpha"} {
		target, err := workspaceServiceTarget(valid)
		if err != nil || target.String() != "http://"+valid+":"+workspaceRuntimeWebUIPort {
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

func TestWorkspaceProxyStripsPlatformCredentials(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "http://workspace.medopl.cn/w/ws-alpha/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer platform-session")
	req.Header.Set("Cookie", "opl_session=platform-session; opl_ws_active=ws-alpha")
	req.Header.Set("X-OPL-CSRF", "csrf")
	req.Header.Set("X-OPL-CSRF-Token", "csrf-token")
	stripWorkspaceProxyCredentials(req)
	for _, header := range []string{"Authorization", "Cookie", "X-OPL-CSRF", "X-OPL-CSRF-Token"} {
		if got := req.Header.Get(header); got != "" {
			t.Fatalf("proxy forwarded %s=%q", header, got)
		}
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
