package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	_ "github.com/mattn/go-sqlite3"

	controlplaneenttest "opl-cloud/services/control-plane/ent/enttest"
	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/domain"
)

func NewTestEntStateStore(t *testing.T, path string) StateStore {
	t.Helper()
	client := controlplaneenttest.Open(t, dialect.SQLite, path+"?_fk=1")
	t.Cleanup(func() { _ = client.Close() })
	return &postgresEntStateStore{client: client}
}

func TestWorkspaceRenewalStateRoundTrips(t *testing.T) {
	for _, tc := range []struct {
		name string
		new  func(*testing.T) controlPlaneTableStore
	}{
		{name: "memory", new: func(*testing.T) controlPlaneTableStore { return newMemoryTableStore() }},
		{name: "sqlite", new: func(t *testing.T) controlPlaneTableStore {
			return NewTestEntStateStore(t, t.TempDir()+"/workspace-renewal.sqlite")
		}},
		{name: "postgres", new: newPostgresWorkspaceRenewalStore},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store := tc.new(t)
			row := canonicalWorkspaceRenewalRow(true)
			if err := store.SaveWorkspace(ctx, row); err != nil {
				t.Fatalf("save Workspace renewal state: %v", err)
			}
			rows, err := store.ListWorkspaces(ctx, "acct-renewal")
			if err != nil || len(rows) != 1 {
				t.Fatalf("list Workspace renewal state: rows=%#v err=%v", rows, err)
			}
			for _, key := range []string{
				"autoRenew", "authorizedBy", "authorizedAt", "packageId", "storageGb", "priceVersion", "currency", "billingUnit",
				"computeUsdMicros", "storageUsdMicros", "totalUsdMicros", "periodStart", "paidThrough",
				"nextRenewalAt", "billingAnchorDay", "renewalStatus", "computeAllocationId", "storageId",
			} {
				if !reflect.DeepEqual(rows[0][key], row[key]) {
					t.Fatalf("Workspace renewal %s = %#v (%T), want %#v (%T): %#v", key, rows[0][key], rows[0][key], row[key], row[key], rows[0])
				}
			}
		})
	}
}

func TestWorkspaceRenewalStateRejectsMissingField(t *testing.T) {
	for _, tc := range []struct {
		name string
		new  func(*testing.T) controlPlaneTableStore
	}{
		{name: "memory", new: func(*testing.T) controlPlaneTableStore { return newMemoryTableStore() }},
		{name: "sqlite", new: func(t *testing.T) controlPlaneTableStore {
			return NewTestEntStateStore(t, t.TempDir()+"/workspace-renewal-missing.sqlite")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := tc.new(t)
			row := canonicalWorkspaceRenewalRow(true)
			delete(row, "authorizedAt")
			if err := store.SaveWorkspace(context.Background(), row); err == nil {
				t.Fatal("Workspace renewal state missing authorizedAt was accepted")
			}
			rows, err := store.ListWorkspaces(context.Background(), "acct-renewal")
			if err != nil || len(rows) != 0 {
				t.Fatalf("invalid Workspace renewal state persisted: rows=%#v err=%v", rows, err)
			}
		})
	}
}

func TestWorkspaceRenewalStateRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "auto renew type", mutate: func(row map[string]any) { row["autoRenew"] = "true" }},
		{name: "authorized by type", mutate: func(row map[string]any) { row["authorizedBy"] = 7 }},
		{name: "authorized actor mismatch", mutate: func(row map[string]any) { row["authorizedBy"] = "usr-other" }},
		{name: "authorized at invalid", mutate: func(row map[string]any) { row["authorizedAt"] = "yesterday" }},
		{name: "price version", mutate: func(row map[string]any) { row["priceVersion"] = "pilot-next" }},
		{name: "currency", mutate: func(row map[string]any) { row["currency"] = "CNY" }},
		{name: "billing unit", mutate: func(row map[string]any) { row["billingUnit"] = "hour" }},
		{name: "fractional compute money", mutate: func(row map[string]any) { row["computeUsdMicros"] = 50_000_000.5 }},
		{name: "compute price", mutate: func(row map[string]any) { row["computeUsdMicros"] = int64(49_000_000) }},
		{name: "storage price", mutate: func(row map[string]any) { row["storageUsdMicros"] = int64(2_580_001) }},
		{name: "total mismatch", mutate: func(row map[string]any) { row["totalUsdMicros"] = int64(52_580_001) }},
		{name: "period start invalid", mutate: func(row map[string]any) { row["periodStart"] = "2026-07-17" }},
		{name: "period empty", mutate: func(row map[string]any) { row["paidThrough"] = row["periodStart"] }},
		{name: "next renewal", mutate: func(row map[string]any) { row["nextRenewalAt"] = row["paidThrough"] }},
		{name: "anchor low", mutate: func(row map[string]any) { row["billingAnchorDay"] = int64(0) }},
		{name: "anchor high", mutate: func(row map[string]any) { row["billingAnchorDay"] = int64(32) }},
		{name: "renewal status", mutate: func(row map[string]any) { row["renewalStatus"] = "renewed" }},
		{name: "full manual review", mutate: func(row map[string]any) {
			row["renewalStatus"], row["manualReviewReason"] = "manual_review", "legacy_billing_state_mismatch"
		}},
		{name: "running current compute missing", mutate: func(row map[string]any) {
			row["state"], row["status"], row["currentComputeAllocationId"] = "running", "running", ""
		}},
		{name: "compute pointer mismatch", mutate: func(row map[string]any) { row["computeAllocationId"] = "compute-other" }},
		{name: "storage id empty", mutate: func(row map[string]any) { row["storageId"] = "" }},
	}
	for _, storeCase := range []struct {
		name string
		new  func(*testing.T) controlPlaneTableStore
	}{
		{name: "memory", new: func(*testing.T) controlPlaneTableStore { return newMemoryTableStore() }},
		{name: "sqlite", new: func(t *testing.T) controlPlaneTableStore {
			return NewTestEntStateStore(t, t.TempDir()+"/workspace-renewal-invalid.sqlite")
		}},
	} {
		t.Run(storeCase.name, func(t *testing.T) {
			for _, test := range tests {
				t.Run(test.name, func(t *testing.T) {
					store := storeCase.new(t)
					row := canonicalWorkspaceRenewalRow(true)
					test.mutate(row)
					if err := store.SaveWorkspace(context.Background(), row); err == nil {
						t.Fatal("invalid Workspace renewal state was accepted")
					}
					rows, err := store.ListWorkspaces(context.Background(), "acct-renewal")
					if err != nil || len(rows) != 0 {
						t.Fatalf("invalid Workspace renewal state persisted: rows=%#v err=%v", rows, err)
					}
				})
			}
		})
	}
}

func TestWorkspaceRenewalStateDecoderFailsClosed(t *testing.T) {
	workspace := canonicalWorkspaceRenewalRow(true)
	encoded, err := encodeWorkspaceBillingState(workspace)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeWorkspaceBillingState(encoded, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded["totalUsdMicros"].(int64); !ok {
		t.Fatalf("decoded money did not remain int64: %#v (%T)", decoded["totalUsdMicros"], decoded["totalUsdMicros"])
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(encoded), &object); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(map[string]any)
		suffix string
	}{
		{name: "unknown", mutate: func(row map[string]any) { row["providerData"] = map[string]any{"secret": "forbidden"} }},
		{name: "missing", mutate: func(row map[string]any) { delete(row, "authorizedBy") }},
		{name: "wrong type", mutate: func(row map[string]any) { row["autoRenew"] = "true" }},
		{name: "fractional money", mutate: func(row map[string]any) { row["storageUsdMicros"] = 2_580_000.5 }},
		{name: "pointer mismatch", mutate: func(row map[string]any) { row["computeAllocationId"] = "compute-other" }},
		{name: "trailing JSON", mutate: func(map[string]any) {}, suffix: `{}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invalid := cloneMap(object)
			test.mutate(invalid)
			payload, err := json.Marshal(invalid)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeWorkspaceBillingState(string(payload)+test.suffix, workspace); err == nil {
				t.Fatal("invalid raw Workspace renewal JSON was accepted")
			}
		})
	}
}

func TestWorkspaceRenewalStateDecoderAllowsOnlyContractAbsentObject(t *testing.T) {
	workspace := canonicalWorkspaceRenewalRow(false)
	for _, encoded := range []string{"", " ", "\n\t"} {
		if _, err := decodeWorkspaceBillingState(encoded, workspace); err == nil {
			t.Fatalf("blank billing JSON %q was accepted", encoded)
		}
	}
	if decoded, err := decodeWorkspaceBillingState("{}", workspace); err != nil || decoded != nil {
		t.Fatalf("contract absent object decoded=%#v err=%v", decoded, err)
	}
}

func TestWorkspaceRenewalStateSupportsProAndDisabledIntent(t *testing.T) {
	row := canonicalWorkspaceRenewalRow(false)
	row["packageId"], row["storageGb"] = "pro", int64(100)
	row["computeUsdMicros"], row["storageUsdMicros"], row["totalUsdMicros"] = int64(214_280_000), int64(25_800_000), int64(240_080_000)
	encoded, err := encodeWorkspaceBillingState(row)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeWorkspaceBillingState(encoded, row)
	if err != nil || decoded["autoRenew"] != false || decoded["authorizedBy"] != "" || decoded["authorizedAt"] != "" || decoded["packageId"] != "pro" || decoded["storageGb"] != int64(100) || decoded["totalUsdMicros"] != int64(240_080_000) {
		t.Fatalf("Pro disabled canonical state=%#v err=%v", decoded, err)
	}
}

func TestWorkspaceRenewalOwnerLifecycleDisablesCanonicalIntent(t *testing.T) {
	for _, storeCase := range []struct {
		name string
		new  func(*testing.T) controlPlaneTableStore
	}{
		{name: "memory", new: func(*testing.T) controlPlaneTableStore { return newMemoryTableStore() }},
		{name: "postgres", new: newPostgresWorkspaceRenewalStore},
	} {
		for _, status := range []string{"disabled", "deleted"} {
			t.Run(storeCase.name+"/"+status, func(t *testing.T) {
				ctx := context.Background()
				store := storeCase.new(t)
				owner := map[string]any{"id": "usr-renewal", "email": "renewal-owner@example.com", "accountId": "acct-renewal", "role": "owner", "status": "active"}
				if err := store.SaveUser(ctx, owner); err != nil {
					t.Fatal(err)
				}
				if err := store.SaveCompute(ctx, map[string]any{"id": "compute-renewal", "accountId": "acct-renewal", "ownerUserId": "usr-renewal", "autoRenew": true}); err != nil {
					t.Fatal(err)
				}
				if err := store.SaveStorage(ctx, map[string]any{"id": "storage-renewal", "accountId": "acct-renewal", "ownerUserId": "usr-renewal", "autoRenew": true}); err != nil {
					t.Fatal(err)
				}
				workspace := canonicalWorkspaceRenewalRow(true)
				if err := store.SaveWorkspace(ctx, workspace); err != nil {
					t.Fatal(err)
				}
				owner["status"] = status
				if err := store.ApplyUserLifecycle(ctx, owner); err != nil {
					t.Fatal(err)
				}
				workspaces, _ := store.ListWorkspaces(ctx, "acct-renewal")
				computes, _ := store.ListComputes(ctx, "acct-renewal")
				storages, _ := store.ListStorages(ctx, "acct-renewal")
				if len(workspaces) != 1 || workspaces[0]["autoRenew"] != false || workspaces[0]["authorizedBy"] != workspace["authorizedBy"] || workspaces[0]["authorizedAt"] != workspace["authorizedAt"] ||
					workspaces[0]["periodStart"] != workspace["periodStart"] || workspaces[0]["paidThrough"] != workspace["paidThrough"] || workspaces[0]["totalUsdMicros"] != workspace["totalUsdMicros"] ||
					len(computes) != 1 || computes[0]["autoRenew"] != false || len(storages) != 1 || storages[0]["autoRenew"] != false {
					t.Fatalf("%s lifecycle intent state: Workspace=%#v compute=%#v storage=%#v", status, workspaces, computes, storages)
				}
			})
		}
	}
}

func TestWorkspaceRenewalOwnerLifecycleLeavesCorruptStateFailClosed(t *testing.T) {
	t.Run("memory", func(t *testing.T) {
		store := newMemoryTableStore()
		owner := map[string]any{"id": "usr-corrupt", "email": "corrupt-owner@example.com", "accountId": "acct-corrupt", "role": "owner", "status": "active"}
		if err := store.SaveUser(context.Background(), owner); err != nil {
			t.Fatal(err)
		}
		store.workspaces["ws-corrupt"] = map[string]any{"id": "ws-corrupt", "accountId": "acct-corrupt", "ownerUserId": "usr-corrupt", "autoRenew": true, "renewalStatus": "active"}
		owner["status"] = "disabled"
		if err := store.ApplyUserLifecycle(context.Background(), owner); err != nil {
			t.Fatal(err)
		}
		rows, _ := store.ListWorkspaces(context.Background(), "acct-corrupt")
		if len(rows) != 1 || rows[0]["autoRenew"] != true || validateWorkspaceBillingState(rows[0]) == nil {
			t.Fatalf("corrupt memory state was rewritten or accepted: %#v", rows)
		}
	})

	t.Run("postgres", func(t *testing.T) {
		store := newPostgresWorkspaceRenewalStore(t).(*postgresEntStateStore)
		ctx := context.Background()
		owner := map[string]any{"id": "usr-renewal", "email": "corrupt-pg-owner@example.com", "accountId": "acct-renewal", "role": "owner", "status": "active"}
		if err := store.SaveUser(ctx, owner); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveWorkspace(ctx, canonicalWorkspaceRenewalRow(true)); err != nil {
			t.Fatal(err)
		}
		const corrupt = `{"autoRenew":true`
		if _, err := store.client.Workspace.UpdateOneID("ws-renewal").SetBillingStateJSON(corrupt).Save(ctx); err != nil {
			t.Fatal(err)
		}
		owner["status"] = "disabled"
		if err := store.ApplyUserLifecycle(ctx, owner); err != nil {
			t.Fatal(err)
		}
		entity, err := store.client.Workspace.Get(ctx, "ws-renewal")
		if err != nil || entity.BillingStateJSON != corrupt {
			t.Fatalf("corrupt PostgreSQL state changed: state=%q err=%v", entity.BillingStateJSON, err)
		}
		rows, err := store.ListWorkspaces(ctx, "acct-renewal")
		if err != nil || len(rows) != 1 || rows[0]["autoRenew"] != nil || rows[0]["renewalStatus"] != nil {
			t.Fatalf("corrupt PostgreSQL state projected as usable: rows=%#v err=%v", rows, err)
		}
	})
}

func TestWorkspaceRenewalStateSurvivesComputeSuspension(t *testing.T) {
	for _, tc := range []struct {
		name string
		new  func(*testing.T) controlPlaneTableStore
	}{
		{name: "memory", new: func(*testing.T) controlPlaneTableStore { return newMemoryTableStore() }},
		{name: "postgres", new: newPostgresWorkspaceRenewalStore},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := tc.new(t)
			workspace := canonicalWorkspaceRenewalRow(true)
			workspace["state"], workspace["status"] = "running", "running"
			if err := store.SaveWorkspace(context.Background(), workspace); err != nil {
				t.Fatal(err)
			}
			rows, err := store.ListWorkspaces(context.Background(), "acct-renewal")
			if err != nil || len(rows) != 1 {
				t.Fatalf("load Workspace: rows=%#v err=%v", rows, err)
			}
			rows[0]["state"], rows[0]["status"] = "suspended", "suspended"
			rows[0]["currentComputeAllocationId"], rows[0]["autoRenew"] = "", false
			if err := store.SaveWorkspace(context.Background(), rows[0]); err != nil {
				t.Fatalf("suspend Workspace: %v", err)
			}
			rows, err = store.ListWorkspaces(context.Background(), "acct-renewal")
			if err != nil || len(rows) != 1 || rows[0]["currentComputeAllocationId"] != nil && rows[0]["currentComputeAllocationId"] != "" ||
				rows[0]["computeAllocationId"] != "compute-renewal" || rows[0]["autoRenew"] != false || rows[0]["totalUsdMicros"] != int64(52_580_000) {
				t.Fatalf("suspended canonical Workspace rows=%#v err=%v", rows, err)
			}
			app, err := newControlPlaneAppWithStore(store)
			if err != nil {
				t.Fatal(err)
			}
			response := app.workspaceResponse(rows[0])
			if response["openable"] == true || stringValue(response["currentComputeAllocationId"]) != "" {
				t.Fatalf("suspended Workspace response=%#v", response)
			}
		})
	}
}

func TestWorkspaceRenewalGatewayRequiresCanonicalCoverage(t *testing.T) {
	ctx := context.Background()
	store := newMemoryTableStore()
	if err := store.SaveCompute(ctx, map[string]any{
		"id": "compute-renewal", "accountId": "acct-renewal", "workspaceId": "ws-renewal", "status": "running", "billingStatus": "active", "paidThrough": "2026-08-20T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveStorage(ctx, map[string]any{
		"id": "storage-renewal", "accountId": "acct-renewal", "workspaceId": "ws-renewal", "status": "available", "billingStatus": "active", "paidThrough": "2026-08-20T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAttachment(ctx, map[string]any{
		"id": "attachment-renewal", "accountId": "acct-renewal", "workspaceId": "ws-renewal",
		"computeAllocationId": "compute-renewal", "storageId": "storage-renewal", "status": "attached",
	}); err != nil {
		t.Fatal(err)
	}
	app, err := newControlPlaneAppWithStore(store)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	base := canonicalWorkspaceRenewalRow(true)
	base["state"], base["status"] = "running", "running"
	base["attachmentId"], base["currentAttachmentId"] = "attachment-renewal", "attachment-renewal"
	base["runtime"] = map[string]any{"serviceName": "workspace-renewal", "status": "running", "ready": true}
	tests := []struct {
		name, reason string
		open         bool
		mutate       func(map[string]any)
	}{
		{name: "valid", open: true, mutate: func(map[string]any) {}},
		{name: "missing", reason: "workspace_billing_state_invalid", mutate: func(row map[string]any) {
			for _, key := range workspaceBillingStateRequiredKeys {
				if key != "storageId" {
					delete(row, key)
				}
			}
		}},
		{name: "invalid", reason: "workspace_billing_state_invalid", mutate: func(row map[string]any) { row["totalUsdMicros"] = int64(1) }},
		{name: "manual review", reason: "workspace_billing_manual_review", mutate: func(row map[string]any) {
			for _, key := range workspaceBillingStateRequiredKeys {
				switch key {
				case "autoRenew", "packageId", "renewalStatus", "computeAllocationId", "storageId":
				default:
					delete(row, key)
				}
			}
			row["autoRenew"] = false
			row["renewalStatus"], row["manualReviewReason"] = "manual_review", "legacy_billing_state_mismatch"
		}},
		{name: "expired", reason: "workspace_billing_period_expired", mutate: func(row map[string]any) {
			row["periodStart"], row["paidThrough"], row["nextRenewalAt"] = "2026-06-19T01:02:03Z", "2026-07-19T01:02:03Z", "2026-07-18T01:02:03Z"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := cloneMap(base)
			test.mutate(workspace)
			response, reason := app.workspaceAccessResponse(workspace, now)
			if reason != test.reason || (response["openable"] == true) != test.open {
				t.Fatalf("Workspace access openable=%#v reason=%q response=%#v", response["openable"], reason, response)
			}
		})
	}
}

func TestWorkspaceRenewalManualReviewMarkerRoundTrips(t *testing.T) {
	marker := map[string]any{
		"autoRenew": false, "renewalStatus": "manual_review", "manualReviewReason": "legacy_billing_state_mismatch",
	}
	encoded, err := encodeWorkspaceBillingState(marker)
	if err != nil {
		t.Fatalf("encode marker: %v", err)
	}
	var shape map[string]any
	if err := json.Unmarshal([]byte(encoded), &shape); err != nil || !reflect.DeepEqual(shape, marker) {
		t.Fatalf("encoded marker=%s shape=%#v err=%v", encoded, shape, err)
	}

	for _, tc := range []struct {
		name string
		new  func(*testing.T) controlPlaneTableStore
	}{
		{name: "memory", new: func(*testing.T) controlPlaneTableStore { return newMemoryTableStore() }},
		{name: "sqlite", new: func(t *testing.T) controlPlaneTableStore {
			return NewTestEntStateStore(t, t.TempDir()+"/workspace-renewal-marker.sqlite")
		}},
		{name: "postgres", new: newPostgresWorkspaceRenewalStore},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := tc.new(t)
			row := map[string]any{
				"id": "ws-marker", "accountId": "acct-marker", "ownerAccountId": "acct-marker", "ownerUserId": "usr-marker",
				"currentComputeAllocationId": "compute-marker", "storageId": "storage-marker",
				"autoRenew": false, "renewalStatus": "manual_review", "manualReviewReason": "legacy_billing_state_mismatch",
			}
			if err := store.SaveWorkspace(context.Background(), row); err != nil {
				t.Fatalf("save marker: %v", err)
			}
			rows, err := store.ListWorkspaces(context.Background(), "acct-marker")
			if err != nil || len(rows) != 1 || rows[0]["autoRenew"] != false || rows[0]["renewalStatus"] != "manual_review" || rows[0]["manualReviewReason"] != "legacy_billing_state_mismatch" {
				t.Fatalf("marker roundtrip rows=%#v err=%v", rows, err)
			}
			app, err := newControlPlaneAppWithStore(store)
			if err != nil {
				t.Fatal(err)
			}
			if _, reason := app.workspaceAccessResponse(rows[0], time.Now().UTC()); reason != "workspace_billing_manual_review" {
				t.Fatalf("marker gateway reason=%q row=%#v", reason, rows[0])
			}
		})
	}
}

func TestWorkspaceProviderAcceptanceBillingExceptionIsNarrow(t *testing.T) {
	app := newControlPlaneApp()
	workspaceID, computeID, storageID := primaryWorkspaceID(providerAcceptanceAccountID), providerAcceptanceComputeID(), providerAcceptanceStorageID()
	mustStore(t, app.tables.SaveCompute(context.Background(), map[string]any{
		"id": computeID, "accountId": providerAcceptanceAccountID, "workspaceId": workspaceID,
		"status": "running", "billingStatus": "active", "paidThrough": "2099-01-01T00:00:00Z",
	}))
	mustStore(t, app.tables.SaveStorage(context.Background(), map[string]any{
		"id": storageID, "accountId": providerAcceptanceAccountID, "workspaceId": workspaceID,
		"status": "available", "billingStatus": "active", "paidThrough": "2099-01-01T00:00:00Z",
	}))
	attachmentID := "attachment-verification"
	mustStore(t, app.tables.SaveAttachment(context.Background(), map[string]any{
		"id": attachmentID, "accountId": providerAcceptanceAccountID, "workspaceId": workspaceID,
		"computeAllocationId": computeID, "storageId": storageID, "status": "attached",
	}))
	base := map[string]any{
		"id": workspaceID, "accountId": providerAcceptanceAccountID, "ownerAccountId": providerAcceptanceAccountID,
		"ownerUserId": "usr-verification", "customerProduct": false, "verificationSlotId": providerAcceptanceSlotID,
		"computeAllocationId": computeID, "currentComputeAllocationId": computeID, "storageId": storageID,
		"attachmentId": attachmentID, "currentAttachmentId": attachmentID, "state": "running", "status": "running",
	}
	response, reason := app.workspaceAccessResponse(base, time.Now().UTC())
	if reason != "" || response["openable"] != true {
		t.Fatalf("fixed Provider Acceptance slot openable=%#v reason=%q", response["openable"], reason)
	}
	for _, mutate := range []func(map[string]any){
		func(row map[string]any) { row["customerProduct"] = true },
		func(row map[string]any) { row["verificationSlotId"] = "verification-slot-other" },
		func(row map[string]any) { row["accountId"], row["ownerAccountId"] = "acct-other", "acct-other" },
		func(row map[string]any) { row["computeAllocationId"] = "" },
		func(row map[string]any) { row["currentComputeAllocationId"] = "compute-other" },
		func(row map[string]any) { row["currentComputeAllocationId"] = "" },
		func(row map[string]any) { row["storageId"] = "storage-other" },
	} {
		workspace := cloneMap(base)
		mutate(workspace)
		if response, reason := app.workspaceAccessResponse(workspace, time.Now().UTC()); reason != "workspace_billing_state_invalid" || response["openable"] == true {
			t.Fatalf("non-slot billing exception openable=%#v reason=%q row=%#v", response["openable"], reason, workspace)
		}
	}
	ordinary := workspaceResponse(map[string]any{"computeAllocationId": "compute-accepted", "currentComputeAllocationId": ""})
	if stringValue(ordinary["currentComputeAllocationId"]) != "" {
		t.Fatalf("ordinary response revived accepted compute pointer: %#v", ordinary)
	}
}

func TestWorkspaceCustomerResponseUsesExplicitAllowlist(t *testing.T) {
	row := canonicalWorkspaceRenewalRow(true)
	row["name"], row["state"], row["status"], row["url"] = "Workspace", "running", "running", "https://workspace.example/w/ws-renewal/"
	row["provider"], row["providerRequestId"], row["receiptId"], row["billingStateJSON"] = "tencent", "provider-request-secret", "receipt-secret", "raw-billing-secret"
	row["rawProviderResponse"] = map[string]any{"adminToken": "nested-provider-secret"}
	row["runtime"] = map[string]any{"serviceName": "runtime-renewal", "status": "running", "ready": true, "rawProvider": "nested-runtime-secret"}
	row["access"] = map[string]any{"username": "opl", "credentialStatus": "configured", "credentialVersion": "v1", "secretRef": "secret-ref-secret", "password": "password-secret"}

	response := workspaceResponse(row)
	if response["id"] != "ws-renewal" || response["name"] != "Workspace" || response["totalUsdMicros"] != int64(52_580_000) || nested(response, "runtime", "serviceName") != "runtime-renewal" {
		t.Fatalf("customer fields missing: %#v", response)
	}
	raw := string(mustJSON(response))
	for _, forbidden := range []string{"provider-request-secret", "receipt-secret", "raw-billing-secret", "nested-provider-secret", "nested-runtime-secret", "secret-ref-secret", "password-secret"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("customer response leaked %q: %s", forbidden, raw)
		}
	}
	response["name"] = "changed"
	if row["name"] != "Workspace" {
		t.Fatalf("customer response reused internal row: %#v", row)
	}
}

func TestAttachmentDetachDisablesCanonicalWorkspaceRenewal(t *testing.T) {
	app := newControlPlaneApp()
	workspace := canonicalWorkspaceRenewalRow(true)
	workspace["state"], workspace["status"] = "running", "running"
	workspace["attachmentId"], workspace["currentAttachmentId"] = "attachment-renewal", "attachment-renewal"
	mustStore(t, app.tables.SaveWorkspace(context.Background(), workspace))
	if err := app.clearWorkspacesForAttachment("attachment-renewal"); err != nil {
		t.Fatal(err)
	}
	stored := storedWorkspace(t, app, "ws-renewal")
	if stored["autoRenew"] != false || stored["authorizedBy"] != workspace["authorizedBy"] || stored["authorizedAt"] != workspace["authorizedAt"] ||
		stored["periodStart"] != workspace["periodStart"] || stored["paidThrough"] != workspace["paidThrough"] || stored["priceVersion"] != workspace["priceVersion"] ||
		stored["computeAllocationId"] != workspace["computeAllocationId"] || stored["storageId"] != workspace["storageId"] {
		t.Fatalf("detached canonical Workspace=%#v", stored)
	}
}

func TestSaveWorkspaceStaleProjectionCannotReviveInactiveLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name string
		new  func(*testing.T) controlPlaneTableStore
	}{
		{name: "memory", new: func(*testing.T) controlPlaneTableStore { return newMemoryTableStore() }},
		{name: "postgres", new: newPostgresWorkspaceRenewalStore},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store := tc.new(t)
			active := canonicalWorkspaceRenewalRow(true)
			active["state"], active["status"], active["runtimeId"] = "running", "running", "runtime-old"
			if err := store.SaveWorkspace(ctx, active); err != nil {
				t.Fatal(err)
			}
			rows, _ := store.ListWorkspaces(ctx, "acct-renewal")
			stale := cloneMap(rows[0])
			inactive := cloneMap(rows[0])
			inactive["state"], inactive["status"], inactive["currentComputeAllocationId"], inactive["autoRenew"] = "suspended", "suspended", "", false
			if err := store.SaveWorkspace(ctx, inactive); err != nil {
				t.Fatal(err)
			}
			stale["runtimeId"] = "runtime-new"
			if err := store.SaveWorkspace(ctx, stale); err != nil {
				t.Fatal(err)
			}
			rows, _ = store.ListWorkspaces(ctx, "acct-renewal")
			if len(rows) != 1 || rows[0]["state"] != "suspended" || stringValue(rows[0]["currentComputeAllocationId"]) != "" || rows[0]["autoRenew"] != false {
				t.Fatalf("stale projection revived lifecycle: %#v", rows)
			}
		})
	}
}

func TestSaveWorkspaceStaleProjectionCannotReenableOwnerRenewal(t *testing.T) {
	for _, test := range []struct {
		name string
		new  func(*testing.T) controlPlaneTableStore
	}{
		{name: "memory", new: func(*testing.T) controlPlaneTableStore { return newMemoryTableStore() }},
		{name: "postgres", new: newPostgresWorkspaceRenewalStore},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store := test.new(t)
			owner := map[string]any{
				"id": "usr-renewal", "email": "renewal-owner@example.com", "accountId": "acct-renewal", "role": "owner", "status": "active",
			}
			mustStore(t, store.SaveUser(ctx, owner))
			workspace := canonicalWorkspaceRenewalRow(true)
			workspace["state"], workspace["status"] = "running", "running"
			mustStore(t, store.SaveWorkspace(ctx, workspace))
			rows, err := store.ListWorkspaces(ctx, "acct-renewal")
			if err != nil || len(rows) != 1 {
				t.Fatalf("load stale Workspace: rows=%#v err=%v", rows, err)
			}
			stale := cloneMap(rows[0])
			owner["status"] = "disabled"
			mustStore(t, store.ApplyUserLifecycle(ctx, owner))
			stale["name"] = "stale replay"
			mustStore(t, store.SaveWorkspace(ctx, stale))
			rows, err = store.ListWorkspaces(ctx, "acct-renewal")
			if err != nil || len(rows) != 1 || rows[0]["autoRenew"] != false ||
				rows[0]["authorizedBy"] != workspace["authorizedBy"] || rows[0]["authorizedAt"] != workspace["authorizedAt"] ||
				rows[0]["periodStart"] != workspace["periodStart"] || rows[0]["paidThrough"] != workspace["paidThrough"] {
				t.Fatalf("stale Workspace restored owner renewal: rows=%#v err=%v", rows, err)
			}
		})
	}
}

func TestWorkspaceRenewalInactiveLifecycleRejectsEnabledIntent(t *testing.T) {
	for _, storeCase := range []struct {
		name string
		new  func(*testing.T) controlPlaneTableStore
	}{
		{name: "memory", new: func(*testing.T) controlPlaneTableStore { return newMemoryTableStore() }},
		{name: "postgres", new: newPostgresWorkspaceRenewalStore},
	} {
		for _, state := range []string{"suspended", "data_deleted", "unrecoverable"} {
			t.Run(storeCase.name+"/"+state, func(t *testing.T) {
				workspace := canonicalWorkspaceRenewalRow(true)
				workspace["state"], workspace["status"], workspace["currentComputeAllocationId"] = state, state, ""
				if err := storeCase.new(t).SaveWorkspace(context.Background(), workspace); !errors.Is(err, errInvalidWorkspaceBillingState) {
					t.Fatalf("inactive auto-renew error=%v, want %v", err, errInvalidWorkspaceBillingState)
				}
			})
		}
	}
}

func TestWorkspaceRenewalClaimIsAtomicAndRejectsDifferentRequestHash(t *testing.T) {
	for _, storeCase := range []struct {
		name string
		new  func(*testing.T) controlPlaneTableStore
	}{
		{name: "memory", new: func(*testing.T) controlPlaneTableStore { return newMemoryTableStore() }},
		{name: "postgres", new: newPostgresWorkspaceRenewalStore},
	} {
		t.Run(storeCase.name, func(t *testing.T) {
			ctx := context.Background()
			store := storeCase.new(t)
			workspace := canonicalWorkspaceRenewalRow(true)
			workspace["state"], workspace["status"] = "running", "running"
			mustStore(t, store.SaveWorkspace(ctx, workspace))
			operation, err := newWorkspaceRenewalOperation(workspace, time.Date(2026, 8, 16, 0, 0, 0, 0, time.UTC))
			if err != nil {
				t.Fatal(err)
			}
			operation.LeaseToken, operation.LeaseExpiresAt = "claim-token", "2026-08-16T00:05:00Z"
			operations, _ := store.ListRuntimeOperations(ctx)
			claim := workspaceRenewalClaimCAS{
				WorkspaceID: operation.WorkspaceID, AccountID: operation.AccountID, ExpectedPaidThrough: stringValue(workspace["paidThrough"]), ExpectedAutoRenew: true,
				ExpectedOperationsVersion: runtimeOperationsVersion(operations, operation.WorkspaceID), DesiredOperation: workspaceRenewalOperationRow(operation),
			}
			start := make(chan struct{})
			results := make(chan error, 2)
			var wg sync.WaitGroup
			for range 2 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					results <- store.ClaimWorkspaceRenewal(ctx, claim)
				}()
			}
			close(start)
			wg.Wait()
			close(results)
			won, conflicted := 0, 0
			for err := range results {
				switch {
				case err == nil:
					won++
				case errors.Is(err, errWorkspaceRenewalCASConflict):
					conflicted++
				default:
					t.Fatalf("claim error=%v", err)
				}
			}
			operations, err = store.ListRuntimeOperations(ctx)
			if err != nil || won != 1 || conflicted != 1 || len(operations) != 1 || stringValue(operations[0]["id"]) != operation.ID {
				t.Fatalf("claims won=%d conflicts=%d operations=%#v err=%v", won, conflicted, operations, err)
			}
			persisted, err := decodeWorkspaceRenewalOperation(operations[0])
			if err != nil {
				t.Fatal(err)
			}
			persisted.RequestHash = "different-request-hash"
			conflictingClaim := workspaceRenewalClaimCAS{
				WorkspaceID: operation.WorkspaceID, AccountID: operation.AccountID, ExpectedPaidThrough: stringValue(workspace["paidThrough"]), ExpectedAutoRenew: true,
				ExpectedOperationsVersion: runtimeOperationsVersion(operations, operation.WorkspaceID), ExpectedOperationResult: stringValue(operations[0]["result"]),
				DesiredOperation: workspaceRenewalOperationRow(persisted),
			}
			if err := store.ClaimWorkspaceRenewal(ctx, conflictingClaim); !errors.Is(err, errIdempotencyConflict) {
				t.Fatalf("different request hash error=%v, want %v", err, errIdempotencyConflict)
			}
		})
	}
}

func TestPersistWorkspaceRenewalMergesPatchWithoutOverwritingConcurrentWorkspaceState(t *testing.T) {
	for _, storeCase := range []struct {
		name string
		new  func(*testing.T) controlPlaneTableStore
	}{
		{name: "memory", new: func(*testing.T) controlPlaneTableStore { return newMemoryTableStore() }},
		{name: "postgres", new: newPostgresWorkspaceRenewalStore},
	} {
		t.Run(storeCase.name, func(t *testing.T) {
			ctx := context.Background()
			store := storeCase.new(t)
			workspace := canonicalWorkspaceRenewalRow(true)
			workspace["state"], workspace["status"], workspace["name"] = "running", "running", "stale-name"
			workspace["currentAttachmentId"], workspace["runtimeId"] = "attachment-stale", "runtime-stale"
			workspace["runtime"], workspace["runtimeServiceName"], workspace["serviceName"] = map[string]any{"serviceName": "runtime-service-stale"}, "runtime-root-stale", "service-stale"
			workspace["access"] = map[string]any{
				"account": "account-stale", "username": "user-stale", "credentialStatus": "ready",
				"credentialVersion": "credential-stale", "secretRef": "secret-stale",
			}
			mustStore(t, store.SaveWorkspace(ctx, workspace))

			rows, err := store.ListWorkspaces(ctx, "acct-renewal")
			if err != nil || len(rows) != 1 {
				t.Fatalf("load stale Workspace: rows=%#v err=%v", rows, err)
			}
			stale := cloneMap(rows[0])
			operation, err := newWorkspaceRenewalOperation(stale, time.Date(2026, 8, 16, 1, 2, 3, 0, time.UTC))
			if err != nil {
				t.Fatal(err)
			}
			operations, err := store.ListRuntimeOperations(ctx)
			if err != nil {
				t.Fatal(err)
			}
			claim := workspaceRenewalClaimCAS{
				WorkspaceID: operation.WorkspaceID, AccountID: operation.AccountID, ExpectedPaidThrough: operation.PaidThrough, ExpectedAutoRenew: true,
				ExpectedOperationsVersion: runtimeOperationsVersion(operations, operation.WorkspaceID), DesiredOperation: workspaceRenewalOperationRow(operation),
			}
			mustStore(t, store.ClaimWorkspaceRenewal(ctx, claim))
			operation.PersistedResult = stringValue(claim.DesiredOperation["result"])

			concurrent := cloneMap(stale)
			concurrent["autoRenew"], concurrent["authorizedBy"], concurrent["authorizedAt"] = false, "", ""
			concurrent["state"], concurrent["status"], concurrent["name"] = "suspended", "suspended", "concurrent-name"
			concurrent["currentComputeAllocationId"], concurrent["currentAttachmentId"], concurrent["runtimeId"] = "", "", "runtime-concurrent"
			concurrent["runtime"], concurrent["runtimeServiceName"], concurrent["serviceName"] = map[string]any{"serviceName": "runtime-service-concurrent"}, "runtime-root-concurrent", "service-concurrent"
			concurrent["access"] = map[string]any{
				"account": "account-concurrent", "username": "user-concurrent", "credentialStatus": "rotating",
				"credentialVersion": "credential-concurrent", "secretRef": "secret-concurrent",
			}
			mustStore(t, store.SaveWorkspace(ctx, concurrent))
			rows, err = store.ListWorkspaces(ctx, "acct-renewal")
			if err != nil || len(rows) != 1 {
				t.Fatalf("load concurrent Workspace: rows=%#v err=%v", rows, err)
			}
			concurrent = cloneMap(rows[0])

			workspacePatch := map[string]any{
				"periodStart": operation.PaidThrough, "paidThrough": operation.RenewedThrough,
				"nextRenewalAt": "2026-09-16T01:02:03Z", "renewalStatus": "active",
			}
			operation.Status, operation.Phase, operation.EntitlementCommitted = "verifying", "receipt", true
			update := workspaceRenewalPersistCAS{
				OperationID: operation.ID, ExpectedOperationResult: operation.PersistedResult,
				DesiredOperation: workspaceRenewalOperationRow(operation), WorkspaceID: operation.WorkspaceID,
				ExpectedWorkspacePaidThrough: operation.PaidThrough, WorkspacePatch: workspacePatch,
			}
			conflict := update
			conflict.ExpectedWorkspacePaidThrough = "2026-08-18T01:02:03Z"
			if err := store.PersistWorkspaceRenewal(ctx, conflict); !errors.Is(err, errWorkspaceRenewalCASConflict) {
				t.Fatalf("stale paidThrough error=%v, want %v", err, errWorkspaceRenewalCASConflict)
			}
			invalid := update
			invalid.WorkspacePatch = map[string]any{"runtimeId": "stale-runtime"}
			if err := store.PersistWorkspaceRenewal(ctx, invalid); !errors.Is(err, errInvalidWorkspaceRenewalPatch) {
				t.Fatalf("invalid patch error=%v, want %v", err, errInvalidWorkspaceRenewalPatch)
			}
			mustStore(t, store.PersistWorkspaceRenewal(ctx, update))

			rows, err = store.ListWorkspaces(ctx, "acct-renewal")
			if err != nil || len(rows) != 1 {
				t.Fatalf("load persisted Workspace: rows=%#v err=%v", rows, err)
			}
			got := rows[0]
			if got["periodStart"] != operation.PaidThrough || got["paidThrough"] != operation.RenewedThrough || got["nextRenewalAt"] != workspacePatch["nextRenewalAt"] || got["renewalStatus"] != "active" {
				t.Fatalf("renewal billing patch not persisted: %#v", got)
			}
			for _, key := range []string{"autoRenew", "authorizedBy", "authorizedAt", "state", "status", "name", "currentComputeAllocationId", "currentAttachmentId", "runtimeId", "runtimeServiceName", "serviceName"} {
				if got[key] != concurrent[key] {
					t.Fatalf("renewal persist overwrote concurrent %s: got=%#v want=%#v Workspace=%#v", key, got[key], concurrent[key], got)
				}
			}
			if string(mustJSON(mapField(got, "runtime"))) != string(mustJSON(mapField(concurrent, "runtime"))) || string(mustJSON(mapField(got, "access"))) != string(mustJSON(mapField(concurrent, "access"))) {
				t.Fatalf("renewal persist overwrote concurrent runtime/access: got=%#v want=%#v", got, concurrent)
			}
		})
	}
}

func TestPostgresWorkspaceRenewalPersistAndOwnerDisableUseSameLockOrder(t *testing.T) {
	store, db := newPostgresWorkspaceRenewalStoreWithDB(t)
	ctx := context.Background()
	workspace := canonicalWorkspaceRenewalRow(true)
	workspace["state"], workspace["status"] = "running", "running"
	mustStore(t, store.SaveWorkspace(ctx, workspace))
	operation, err := newWorkspaceRenewalOperation(workspace, time.Date(2026, 8, 16, 1, 2, 3, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	operations, err := store.ListRuntimeOperations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	claim := workspaceRenewalClaimCAS{
		WorkspaceID: operation.WorkspaceID, AccountID: operation.AccountID, ExpectedPaidThrough: operation.PaidThrough, ExpectedAutoRenew: true,
		ExpectedOperationsVersion: runtimeOperationsVersion(operations, operation.WorkspaceID), DesiredOperation: workspaceRenewalOperationRow(operation),
	}
	mustStore(t, store.ClaimWorkspaceRenewal(ctx, claim))
	operation.PersistedResult = stringValue(claim.DesiredOperation["result"])

	workspaces, err := store.ListWorkspaces(ctx, operation.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	operations, err = store.ListRuntimeOperations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	intent, _, err := planWorkspaceRenewalIntent(recordByID(workspaces, operation.WorkspaceID), map[string]any{"id": operation.OwnerUserID}, operations, false, "owner-disable-during-persist", time.Date(2026, 8, 16, 1, 3, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	operation.Status, operation.Phase, operation.EntitlementCommitted = "verifying", "receipt", true
	persist := workspaceRenewalPersistCAS{
		OperationID: operation.ID, ExpectedOperationResult: operation.PersistedResult, DesiredOperation: workspaceRenewalOperationRow(operation),
		WorkspaceID: operation.WorkspaceID, ExpectedWorkspacePaidThrough: operation.PaidThrough,
		WorkspacePatch: map[string]any{
			"periodStart": operation.PaidThrough, "paidThrough": operation.RenewedThrough,
			"nextRenewalAt": "2026-09-16T01:02:03Z", "renewalStatus": "active",
		},
	}

	var schema string
	if err := db.QueryRowContext(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	newStore := func(applicationName string) *postgresEntStateStore {
		stateStore, err := NewPostgresEntStateStore(controlPlaneTestPostgresURL(t, "postgres", schema) + " application_name=" + applicationName)
		if err != nil {
			t.Fatal(err)
		}
		result := stateStore.(*postgresEntStateStore)
		t.Cleanup(func() { _ = result.client.Close() })
		return result
	}
	ownerStore := newStore("task7_owner_disable")
	workerStore := newStore("task7_worker_persist")

	blocker, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback() }()
	var lockedID string
	if err := blocker.QueryRowContext(ctx, `SELECT id FROM control_plane_workspaces WHERE id = $1 FOR UPDATE`, operation.WorkspaceID).Scan(&lockedID); err != nil {
		t.Fatal(err)
	}
	type transactionResult struct {
		name string
		err  error
	}
	results := make(chan transactionResult, 2)
	go func() {
		results <- transactionResult{name: "owner", err: ownerStore.ApplyWorkspaceRenewalIntent(ctx, intent)}
	}()
	waitForPostgresApplicationLock(t, db, "task7_owner_disable")
	go func() {
		results <- transactionResult{name: "worker", err: workerStore.PersistWorkspaceRenewal(ctx, persist)}
	}()
	waitForPostgresApplicationLock(t, db, "task7_worker_persist")
	if err := blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		select {
		case result := <-results:
			if result.err != nil {
				t.Fatalf("%s transaction failed: %v", result.name, result.err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("owner disable and renewal persist did not converge")
		}
	}
	workspaces, err = store.ListWorkspaces(ctx, operation.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	got := recordByID(workspaces, operation.WorkspaceID)
	if got["autoRenew"] != false || stringValue(got["authorizedBy"]) != "" || stringValue(got["authorizedAt"]) != "" || got["paidThrough"] != operation.RenewedThrough {
		t.Fatalf("concurrent owner intent and entitlement patch did not merge: %#v", got)
	}
}

func waitForPostgresApplicationLock(t *testing.T, db *sql.DB, applicationName string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var waiting bool
		if err := db.QueryRow(`SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE application_name = $1 AND wait_event_type = 'Lock')`, applicationName).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("PostgreSQL transaction %s did not wait on the expected row lock", applicationName)
}

func TestPostgresSaveWorkspaceUpdatesWithoutDeleteInsert(t *testing.T) {
	store, db := newPostgresWorkspaceRenewalStoreWithDB(t)
	ctx := context.Background()
	original := canonicalWorkspaceRenewalRow(false)
	original["name"] = "original"
	if err := store.SaveWorkspace(ctx, original); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		CREATE FUNCTION reject_workspace_insert() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'injected workspace insert failure'; END $$;
		CREATE TRIGGER reject_workspace_insert BEFORE INSERT ON control_plane_workspaces
		FOR EACH ROW EXECUTE FUNCTION reject_workspace_insert();
	`); err != nil {
		t.Fatal(err)
	}
	changed := cloneMap(original)
	changed["name"] = "changed"
	if err := store.SaveWorkspace(ctx, changed); err != nil {
		t.Fatalf("Workspace update used delete/insert: %v", err)
	}
	rows, err := store.ListWorkspaces(ctx, "acct-renewal")
	if err != nil || len(rows) != 1 || rows[0]["name"] != "changed" {
		t.Fatalf("Workspace update rows=%#v err=%v", rows, err)
	}
}

func seedPostgresWorkspaceActivation(t *testing.T, store *postgresEntStateStore, ownerStatus string, mutateAttachment func(map[string]any)) map[string]any {
	t.Helper()
	ctx := context.Background()
	mustStore(t, store.SaveWorkspace(ctx, map[string]any{
		"id": "ws-renewal", "accountId": "acct-renewal", "ownerAccountId": "acct-renewal", "ownerUserId": "usr-renewal",
		"state": "provisioning", "status": "provisioning",
	}))
	mustStore(t, store.SaveUser(ctx, map[string]any{
		"id": "usr-renewal", "email": "renewal-owner@example.com", "accountId": "acct-renewal", "role": "owner", "status": ownerStatus,
	}))
	mustStore(t, store.SaveCompute(ctx, map[string]any{
		"id": "compute-renewal", "accountId": "acct-renewal", "ownerUserId": "usr-renewal", "workspaceId": "ws-renewal",
		"status": "running", "billingStatus": "active", "paidThrough": "2026-09-17T01:02:03Z",
	}))
	mustStore(t, store.SaveStorage(ctx, map[string]any{
		"id": "storage-renewal", "accountId": "acct-renewal", "ownerUserId": "usr-renewal", "workspaceId": "ws-renewal",
		"status": "available", "billingStatus": "active", "paidThrough": "2026-09-17T01:02:03Z",
	}))
	attachment := map[string]any{
		"id": "attachment-renewal", "accountId": "acct-renewal", "workspaceId": "ws-renewal",
		"computeAllocationId": "compute-renewal", "storageId": "storage-renewal", "status": "attached",
	}
	mutateAttachment(attachment)
	mustStore(t, store.SaveAttachment(ctx, attachment))
	workspace := canonicalWorkspaceRenewalRow(true)
	workspace["state"], workspace["status"] = "running", "running"
	workspace["attachmentId"], workspace["currentAttachmentId"] = "attachment-renewal", "attachment-renewal"
	return workspace
}

func TestPostgresActivateWorkspaceRevalidatesOwnerAndAttachment(t *testing.T) {

	t.Run("disabled owner disables renewal atomically", func(t *testing.T) {
		store, _ := newPostgresWorkspaceRenewalStoreWithDB(t)
		workspace := seedPostgresWorkspaceActivation(t, store, "disabled", func(map[string]any) {})
		activated, err := store.ActivateWorkspace(context.Background(), workspace)
		if err != nil {
			t.Fatal(err)
		}
		rows, listErr := store.ListWorkspaces(context.Background(), "acct-renewal")
		if listErr != nil || activated["autoRenew"] != false || len(rows) != 1 || rows[0]["state"] != "running" || rows[0]["autoRenew"] != false {
			t.Fatalf("disabled-owner activation=%#v rows=%#v err=%v", activated, rows, listErr)
		}
	})

	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "detached", mutate: func(row map[string]any) { row["status"] = "detached" }},
		{name: "identity mismatch", mutate: func(row map[string]any) { row["storageId"] = "storage-other" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, _ := newPostgresWorkspaceRenewalStoreWithDB(t)
			workspace := seedPostgresWorkspaceActivation(t, store, "active", test.mutate)
			if _, err := store.ActivateWorkspace(context.Background(), workspace); !errors.Is(err, errWorkspaceActivationConflict) {
				t.Fatalf("activation error = %v, want %v", err, errWorkspaceActivationConflict)
			}
			rows, err := store.ListWorkspaces(context.Background(), "acct-renewal")
			if err != nil || len(rows) != 1 || rows[0]["state"] != "provisioning" || workspaceAcceptedBillingState(rows[0]) != nil {
				t.Fatalf("rejected activation changed draft: rows=%#v err=%v", rows, err)
			}
		})
	}
}

func TestPostgresActivateWorkspaceWaitsForLockedAttachment(t *testing.T) {
	store, db := newPostgresWorkspaceRenewalStoreWithDB(t)
	workspace := seedPostgresWorkspaceActivation(t, store, "active", func(map[string]any) {})
	ctx := context.Background()
	blocker, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback() }()
	result, err := blocker.ExecContext(ctx, `UPDATE control_plane_storage_attachments SET status = 'detached' WHERE id = 'attachment-renewal'`)
	if err != nil {
		t.Fatal(err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		t.Fatalf("lock Attachment rows=%d err=%v", affected, err)
	}
	type activationResult struct {
		row map[string]any
		err error
	}
	done := make(chan activationResult, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		row, err := store.ActivateWorkspace(ctx, workspace)
		done <- activationResult{row: row, err: err}
	}()
	<-started
	select {
	case early := <-done:
		t.Fatalf("activation returned before locked detach committed: row=%#v err=%v", early.row, early.err)
	case <-time.After(time.Second):
	}
	if err := blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case activated := <-done:
		if !errors.Is(activated.err, errWorkspaceActivationConflict) || activated.row != nil {
			t.Fatalf("activation after detach = row=%#v err=%v", activated.row, activated.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("activation remained blocked after detach commit")
	}
	rows, err := store.ListWorkspaces(ctx, "acct-renewal")
	if err != nil || len(rows) != 1 || rows[0]["state"] != "provisioning" || workspaceAcceptedBillingState(rows[0]) != nil {
		t.Fatalf("detached activation changed draft: rows=%#v err=%v", rows, err)
	}
}

func TestPostgresSaveWorkspaceReReadsAfterLockedLifecycleChange(t *testing.T) {
	store, db := newPostgresWorkspaceRenewalStoreWithDB(t)
	ctx := context.Background()
	workspace := canonicalWorkspaceRenewalRow(true)
	workspace["state"], workspace["status"] = "running", "running"
	workspace["attachmentId"], workspace["currentAttachmentId"] = "attachment-renewal", "attachment-renewal"
	mustStore(t, store.SaveWorkspace(ctx, workspace))
	rows, err := store.ListWorkspaces(ctx, "acct-renewal")
	if err != nil || len(rows) != 1 {
		t.Fatalf("load stale Workspace: rows=%#v err=%v", rows, err)
	}
	stale := cloneMap(rows[0])
	stale["name"] = "stale replay"
	blocker, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback() }()
	result, err := blocker.ExecContext(ctx, `
		UPDATE control_plane_workspaces
		SET state = 'suspended', status = 'suspended', current_compute_allocation_id = '', current_attachment_id = '',
		    billing_state_json = jsonb_set(billing_state_json::jsonb, '{autoRenew}', 'false'::jsonb)::text
		WHERE id = 'ws-renewal'
	`)
	if err != nil {
		t.Fatal(err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		t.Fatalf("lock Workspace rows=%d err=%v", affected, err)
	}
	done := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		done <- store.SaveWorkspace(ctx, stale)
	}()
	<-started
	select {
	case err := <-done:
		t.Fatalf("stale save returned before locked lifecycle change committed: %v", err)
	case <-time.After(time.Second):
	}
	if err := blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("stale save after lifecycle commit: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stale save remained blocked after lifecycle commit")
	}
	rows, err = store.ListWorkspaces(ctx, "acct-renewal")
	if err != nil || len(rows) != 1 || rows[0]["state"] != "suspended" || rows[0]["status"] != "suspended" || rows[0]["autoRenew"] != false ||
		stringValue(rows[0]["currentComputeAllocationId"]) != "" || stringValue(rows[0]["currentAttachmentId"]) != "" ||
		rows[0]["authorizedBy"] != workspace["authorizedBy"] || rows[0]["authorizedAt"] != workspace["authorizedAt"] ||
		rows[0]["periodStart"] != workspace["periodStart"] || rows[0]["paidThrough"] != workspace["paidThrough"] {
		t.Fatalf("stale save overwrote lifecycle change: rows=%#v err=%v", rows, err)
	}
}

func TestWorkspaceRenewalManualReviewMarkerDecoderIsExact(t *testing.T) {
	workspace := map[string]any{"ownerUserId": "usr-marker", "currentComputeAllocationId": "compute-marker", "storageId": "storage-marker"}
	for _, encoded := range []string{
		`{"autoRenew":false,"renewalStatus":"manual_review"}`,
		`{"autoRenew":false,"renewalStatus":"manual_review","manualReviewReason":"legacy_billing_state_mismatch","authorizedBy":""}`,
		`{"autoRenew":"false","renewalStatus":"manual_review","manualReviewReason":"legacy_billing_state_mismatch"}`,
		`{"autoRenew":true,"renewalStatus":"manual_review","manualReviewReason":"legacy_billing_state_mismatch"}`,
		`{"autoRenew":false,"renewalStatus":"active","manualReviewReason":"legacy_billing_state_mismatch"}`,
		`{"autoRenew":false,"renewalStatus":"manual_review","manualReviewReason":"unknown"}`,
	} {
		if _, err := decodeWorkspaceBillingState(encoded, workspace); err == nil {
			t.Fatalf("invalid marker accepted: %s", encoded)
		}
	}
}

func newPostgresWorkspaceRenewalStore(t *testing.T) controlPlaneTableStore {
	store, _ := newPostgresWorkspaceRenewalStoreWithDB(t)
	return store
}

func newPostgresWorkspaceRenewalStoreWithDB(t *testing.T) (*postgresEntStateStore, *sql.DB) {
	t.Helper()
	admin := openControlPlaneTestPostgres(t)
	schema := fmt.Sprintf("control_plane_workspace_renewal_%d", time.Now().UnixNano())
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
		_ = admin.Close()
	})
	stateStore, err := NewPostgresEntStateStore(controlPlaneTestPostgresURL(t, "postgres", schema))
	if err != nil {
		t.Fatal(err)
	}
	store := stateStore.(*postgresEntStateStore)
	t.Cleanup(func() { _ = store.client.Close() })
	db, err := sql.Open("postgres", controlPlaneTestPostgresURL(t, "postgres", schema))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Ping(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return store, db
}

func canonicalWorkspaceRenewalRow(autoRenew bool) map[string]any {
	authorizedBy, authorizedAt := "", ""
	if autoRenew {
		authorizedBy, authorizedAt = "usr-renewal", "2026-07-17T01:02:03Z"
	}
	return map[string]any{
		"id": "ws-renewal", "accountId": "acct-renewal", "ownerAccountId": "acct-renewal", "ownerUserId": "usr-renewal",
		"currentComputeAllocationId": "compute-renewal", "storageId": "storage-renewal", "packageId": "basic", "storageGb": int64(10),
		"autoRenew": autoRenew, "authorizedBy": authorizedBy, "authorizedAt": authorizedAt,
		"priceVersion": pricingCatalogVersion, "currency": pricingCurrency, "billingUnit": pricingBillingUnit,
		"computeUsdMicros": int64(50_000_000), "storageUsdMicros": int64(2_580_000), "totalUsdMicros": int64(52_580_000),
		"periodStart": "2026-07-17T01:02:03Z", "paidThrough": "2026-08-17T01:02:03Z", "nextRenewalAt": "2026-08-16T01:02:03Z",
		"billingAnchorDay": int64(17), "renewalStatus": "active", "computeAllocationId": "compute-renewal",
	}
}

func TestEntStateStoreSub2APIMappingAndMonthlyEntitlementRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := NewTestEntStateStore(t, t.TempDir()+"/monthly.sqlite")
	if err := store.SaveAccount(ctx, map[string]any{"id": "acct-monthly", "status": "active", "sub2apiUserId": int64(41)}); err != nil {
		t.Fatalf("save account mapping: %v", err)
	}
	accounts, err := store.ListAccounts(ctx, "acct-monthly")
	if err != nil {
		t.Fatalf("list accounts: %v", err)
	}
	account := recordByID(accounts, "acct-monthly")
	if int64(numberField(account, "sub2apiUserId", 0)) != 41 {
		t.Fatalf("account mapping = %#v", account)
	}

	monthly := map[string]any{
		"accountId":                  "acct-monthly",
		"billingStatus":              "active",
		"billingOperationId":         "billing-op-41",
		"billingOperationStartedAt":  "2026-07-14T00:00:00Z",
		"sub2apiRedeemCode":          "opl:test:billing-op-41:charge:v1",
		"pricingVersion":             pricingCatalogVersion,
		"monthlyPriceCnyCents":       int64(35000),
		"chargeUsdMicros":            int64(50_000_000),
		"billingAnchorDay":           int64(14),
		"periodStart":                "2026-07-14T00:00:00Z",
		"paidThrough":                "2026-08-14T00:00:00Z",
		"autoRenew":                  true,
		"lastRenewalAttemptAt":       "2026-07-14T00:00:00Z",
		"lastBillingError":           "",
		"lastReceiptId":              "receipt-41",
		"postChargeBalanceUsdMicros": int64(0),
		"postChargeBalanceKnown":     true,
		"requestedPeriodMonths":      int64(1),
		"periodMonths":               int64(1),
		"verificationSlotId":         "verification-slot-01",
		"customerProduct":            false,
		"costTags":                   map[string]string{"opl_account_id": "acct-monthly", "opl_workspace_id": "ws-monthly"},
	}
	compute := mergeMaps(monthly, map[string]any{"id": "compute-monthly", "packageId": "basic", "nodePoolId": "np-slot-01", "instanceType": "SA5.MEDIUM4"})
	storage := mergeMaps(monthly, map[string]any{"id": "storage-monthly", "packageId": "basic", "sizeGb": 30, "pvName": "pv-slot-01", "persistentVolumeName": "pv-slot-01"})
	if err := store.SaveCompute(ctx, compute); err != nil {
		t.Fatalf("save monthly compute: %v", err)
	}
	if err := store.SaveStorage(ctx, storage); err != nil {
		t.Fatalf("save monthly storage: %v", err)
	}

	computes, err := store.ListComputes(ctx, "acct-monthly")
	if err != nil {
		t.Fatalf("list monthly compute: %v", err)
	}
	storages, err := store.ListStorages(ctx, "acct-monthly")
	if err != nil {
		t.Fatalf("list monthly storage: %v", err)
	}
	for kind, row := range map[string]map[string]any{
		"compute": recordByID(computes, "compute-monthly"),
		"storage": recordByID(storages, "storage-monthly"),
	} {
		if row["billingOperationId"] != "billing-op-41" || int64(numberField(row, "monthlyPriceCnyCents", 0)) != 35000 || int64(numberField(row, "chargeUsdMicros", 0)) != 50_000_000 || row["paidThrough"] != "2026-08-14T00:00:00Z" || row["autoRenew"] != true {
			t.Fatalf("%s monthly fields = %#v", kind, row)
		}
		if row["postChargeBalanceKnown"] != true || int64(numberField(row, "postChargeBalanceUsdMicros", 0)) != 0 {
			t.Fatalf("%s zero post-charge balance is not known: %#v", kind, row)
		}
		if int64(numberField(row, "requestedPeriodMonths", 0)) != 1 || int64(numberField(row, "periodMonths", 0)) != 1 || row["verificationSlotId"] != "verification-slot-01" || row["customerProduct"] != false {
			t.Fatalf("%s verifier classification fields = %#v", kind, row)
		}
		if tags := mapField(row, "costTags"); tags["opl_account_id"] != "acct-monthly" || tags["opl_workspace_id"] != "ws-monthly" {
			t.Fatalf("%s cost tags = %#v", kind, tags)
		}
		if kind == "compute" && (row["nodePoolId"] != "np-slot-01" || row["instanceType"] != "SA5.MEDIUM4") {
			t.Fatalf("compute provider fields = %#v", row)
		}
		if kind == "storage" && (row["pvName"] != "pv-slot-01" || row["persistentVolumeName"] != "pv-slot-01") {
			t.Fatalf("storage provider fields = %#v", row)
		}
	}
}

func TestAccountStoresRejectDuplicateSub2APIUserMapping(t *testing.T) {
	ctx := context.Background()
	for name, store := range map[string]StateStore{
		"memory": newMemoryTableStore(),
		"ent":    NewTestEntStateStore(t, t.TempDir()+"/account-mapping.sqlite"),
	} {
		t.Run(name, func(t *testing.T) {
			if err := store.SaveAccount(ctx, map[string]any{"id": "acct-one", "status": "active", "sub2apiUserId": int64(41)}); err != nil {
				t.Fatal(err)
			}
			if err := store.SaveAccount(ctx, map[string]any{"id": "acct-two", "status": "active", "sub2apiUserId": int64(41)}); err == nil || err.Error() != "sub2api_account_mapping_conflict" {
				t.Fatalf("duplicate mapping error = %v", err)
			}
		})
	}
}

func TestMemoryAccountStoreSerializesDuplicateSub2APIUserMapping(t *testing.T) {
	store := newMemoryTableStore()
	start := make(chan struct{})
	errorsByAccount := make(chan error, 2)
	var workers sync.WaitGroup
	for _, accountID := range []string{"acct-one", "acct-two"} {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			errorsByAccount <- store.SaveAccount(context.Background(), map[string]any{"id": accountID, "status": "active", "sub2apiUserId": int64(41)})
		}()
	}
	close(start)
	workers.Wait()
	close(errorsByAccount)

	succeeded, conflicted := 0, 0
	for err := range errorsByAccount {
		switch {
		case err == nil:
			succeeded++
		case err.Error() == "sub2api_account_mapping_conflict":
			conflicted++
		default:
			t.Fatalf("unexpected save error: %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent mapping results: succeeded=%d conflicted=%d", succeeded, conflicted)
	}
}

func TestEntStateStoreBillingOperationReplayConflictsOnAmountOrPeriod(t *testing.T) {
	ctx := context.Background()
	for name, store := range map[string]StateStore{
		"memory": newMemoryTableStore(),
		"ent":    NewTestEntStateStore(t, t.TempDir()+"/billing-claim.sqlite"),
	} {
		t.Run(name, func(t *testing.T) {
			operation := map[string]any{
				"id":                   "compute-claim-41",
				"accountId":            "acct-alpha",
				"packageId":            "basic",
				"billingStatus":        "preparing",
				"billingOperationId":   "billing-op-claim-41",
				"pricingVersion":       pricingCatalogVersion,
				"monthlyPriceCnyCents": int64(35000),
				"chargeUsdMicros":      int64(50_000_000),
				"periodStart":          "2026-07-14T00:00:00Z",
				"paidThrough":          "2026-08-14T00:00:00Z",
			}
			claimed, fresh, err := store.ClaimResourceBillingOperation(ctx, "compute", operation)
			if err != nil || !fresh || claimed["billingOperationId"] != operation["billingOperationId"] {
				t.Fatalf("first claim fresh=%v row=%#v err=%v", fresh, claimed, err)
			}
			if _, fresh, err := store.ClaimResourceBillingOperation(ctx, "compute", operation); err != nil || fresh {
				t.Fatalf("same operation replay fresh=%v err=%v", fresh, err)
			}
			for field, value := range map[string]any{
				"chargeUsdMicros": int64(49_000_000),
				"paidThrough":     "2026-09-14T00:00:00Z",
			} {
				conflict := cloneMap(operation)
				conflict[field] = value
				if field == "chargeUsdMicros" {
					snapshot := mapField(conflict, "priceSnapshot")
					snapshot[field] = value
					conflict["priceSnapshot"] = snapshot
				}
				if _, _, err := store.ClaimResourceBillingOperation(ctx, "compute", conflict); !errors.Is(err, errIdempotencyConflict) {
					t.Fatalf("%s conflict error = %v", field, err)
				}
			}
		})
	}
}

func TestStateStoreNewBillingOperationClearsPreviousReceipt(t *testing.T) {
	ctx := context.Background()
	stores := []struct {
		name  string
		store StateStore
	}{
		{name: "memory", store: newMemoryTableStore()},
		{name: "ent", store: NewTestEntStateStore(t, t.TempDir()+"/billing-receipt-reset.sqlite")},
	}
	for _, tc := range stores {
		t.Run(tc.name, func(t *testing.T) {
			old := monthlyActiveResource("storage", "storage-receipt-reset", time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC))
			old["billingStatus"] = "retained"
			old["sub2apiChargeConfirmation"] = map[string]any{
				"code": old["sub2apiRedeemCode"], "userId": int64(41), "chargeUsdMicros": old["chargeUsdMicros"], "status": "used",
			}
			if err := tc.store.SaveStorage(ctx, old); err != nil {
				t.Fatal(err)
			}
			operation := cloneMap(old)
			operation["billingStatus"], operation["billingOperationId"] = "charge_pending", "billing-reactivation"
			operation["sub2apiRedeemCode"] = monthlyRedeemCode("test", "billing-reactivation")
			operation["lastReceiptId"] = ""
			delete(operation, "sub2apiChargeConfirmation")

			claimed, fresh, err := tc.store.ClaimResourceBillingOperation(ctx, "storage", operation)
			_, confirmationExists := claimed["sub2apiChargeConfirmation"]
			if err != nil || !fresh || stringValue(claimed["lastReceiptId"]) != "" || confirmationExists {
				t.Fatalf("new operation fresh=%v row=%#v err=%v", fresh, claimed, err)
			}
			rows, err := tc.store.ListStorages(ctx, "acct-monthly")
			persisted := recordByID(rows, "storage-receipt-reset")
			_, confirmationExists = persisted["sub2apiChargeConfirmation"]
			if err != nil || stringValue(persisted["lastReceiptId"]) != "" || confirmationExists {
				t.Fatalf("persisted new operation rows=%#v err=%v", rows, err)
			}

			claimed["lastReceiptId"] = "receipt-reactivation"
			claimed["sub2apiChargeConfirmation"] = map[string]any{
				"code": claimed["sub2apiRedeemCode"], "userId": int64(41), "chargeUsdMicros": claimed["chargeUsdMicros"], "status": "used",
			}
			if err := tc.store.SaveStorage(ctx, claimed); err != nil {
				t.Fatal(err)
			}
			rows, err = tc.store.ListStorages(ctx, "acct-monthly")
			persisted = recordByID(rows, "storage-receipt-reset")
			if err != nil || !monthlyChargeConfirmationMatches(mapField(persisted, "sub2apiChargeConfirmation"), stringValue(operation["sub2apiRedeemCode"]), 41, int64(numberField(operation, "chargeUsdMicros", 0))) {
				t.Fatalf("persisted same operation row=%#v err=%v", persisted, err)
			}
			replayed, fresh, err := tc.store.ClaimResourceBillingOperation(ctx, "storage", operation)
			if err != nil || fresh || stringValue(replayed["lastReceiptId"]) != "receipt-reactivation" ||
				!monthlyChargeConfirmationMatches(mapField(replayed, "sub2apiChargeConfirmation"), stringValue(operation["sub2apiRedeemCode"]), 41, int64(numberField(operation, "chargeUsdMicros", 0))) {
				t.Fatalf("same operation replay fresh=%v row=%#v err=%v", fresh, replayed, err)
			}
		})
	}
}

func recordByID(rows []map[string]any, id string) map[string]any {
	for _, row := range rows {
		if stringValue(row["id"]) == id {
			return row
		}
	}
	return nil
}

func TestEntStateStoreNeverPersistsWorkspacePassword(t *testing.T) {
	store := NewTestEntStateStore(t, t.TempDir()+"/workspace-secret.sqlite")
	if err := store.SaveWorkspace(context.Background(), map[string]any{
		"id": "ws-alpha", "accountId": "acct-alpha",
		"access": map[string]any{"username": "opl", "password": "must-not-persist", "secretRef": "opl-compute-alpha-env"},
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := store.ListWorkspaces(context.Background(), "acct-alpha")
	if err != nil {
		t.Fatal(err)
	}
	if password := stringValue(nested(rows[0], "access", "password")); password != "" {
		t.Fatalf("Workspace password persisted: %q", password)
	}
}

func TestEntStateStorePersistsWorkspaceVerificationClassification(t *testing.T) {
	store := NewTestEntStateStore(t, t.TempDir()+"/workspace-classification.sqlite")
	if err := store.SaveWorkspace(context.Background(), map[string]any{
		"id": "ws-slot", "accountId": "acct-alpha", "verificationSlotId": "verification-slot-01", "customerProduct": false,
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := store.ListWorkspaces(context.Background(), "acct-alpha")
	if err != nil || len(rows) != 1 {
		t.Fatalf("list Workspaces: rows=%#v err=%v", rows, err)
	}
	if rows[0]["verificationSlotId"] != "verification-slot-01" || rows[0]["customerProduct"] != false {
		t.Fatalf("Workspace verification classification = %#v", rows[0])
	}
	if err := store.SaveWorkspace(context.Background(), map[string]any{"id": "ws-customer", "accountId": "acct-beta"}); err != nil {
		t.Fatal(err)
	}
	customers, err := store.ListWorkspaces(context.Background(), "acct-beta")
	if err != nil || len(customers) != 1 || customers[0]["customerProduct"] != true {
		t.Fatalf("customer Workspace default = %#v err=%v", customers, err)
	}
}

func TestEntStateStoreWorkspaceResumeCommitRollsBackAllFacts(t *testing.T) {
	ctx := context.Background()
	store := NewTestEntStateStore(t, t.TempDir()+"/resume-transaction.sqlite")
	original := map[string]any{"id": "workspace-alpha", "accountId": "acct-alpha", "state": "suspended", "status": "suspended"}
	if err := store.SaveWorkspace(ctx, original); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	running := cloneMap(original)
	running["state"], running["status"] = "running", "running"
	err := store.CommitWorkspaceResume(ctx, running, map[string]any{"id": "audit-resume", "action": "workspace.resume", "resourceKind": "workspace", "resourceId": "workspace-alpha", "result": "succeeded"}, map[string]any{"action": "workspace.resume"})
	if err == nil {
		t.Fatal("resume commit with invalid operation unexpectedly succeeded")
	}
	workspaces, _ := store.ListWorkspaces(ctx, "")
	audits, _ := store.ListAuditEvents(ctx, "")
	operations, _ := store.ListRuntimeOperations(ctx)
	if len(workspaces) != 1 || workspaces[0]["state"] != "suspended" || len(audits) != 0 || len(operations) != 0 {
		t.Fatalf("failed resume commit was not atomic: workspaces=%#v audits=%#v operations=%#v", workspaces, audits, operations)
	}
}

func TestEntStateStoreWorkspaceResumeClaimIsRetryableAndExclusive(t *testing.T) {
	ctx := context.Background()
	store := NewTestEntStateStore(t, t.TempDir()+"/resume-claim.sqlite")
	if err := store.SaveWorkspace(ctx, map[string]any{"id": "workspace-alpha", "accountId": "acct-alpha", "state": "suspended", "status": "suspended"}); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	lease := time.Now().UTC().Add(time.Minute)
	operation := map[string]any{"id": "resume-alpha", "operationId": "resume-alpha", "workspaceId": "workspace-alpha", "resourceId": "workspace-alpha", "resourceKind": "workspace_runtime", "action": "workspace.resume", "status": "started", "result": encodeWorkspaceResumeOperation(workspaceResumeOperationResult{RequestHash: "hash-alpha", LeaseExpiresAt: &lease})}
	if _, replayed, err := store.ClaimWorkspaceResume(ctx, "workspace-alpha", operation); err != nil || replayed {
		t.Fatalf("claim = replayed:%v err:%v", replayed, err)
	}
	if _, _, err := store.ClaimWorkspaceResume(ctx, "workspace-alpha", operation); !errors.Is(err, errWorkspaceResumeInProgress) {
		t.Fatalf("same-key concurrent claim error = %v", err)
	}
	different := cloneMap(operation)
	different["id"], different["operationId"] = "resume-other", "resume-other"
	different["result"] = encodeWorkspaceResumeOperation(workspaceResumeOperationResult{RequestHash: "hash-other", LeaseExpiresAt: &lease})
	if _, _, err := store.ClaimWorkspaceResume(ctx, "workspace-alpha", different); !errors.Is(err, errWorkspaceResumeInProgress) {
		t.Fatalf("different-key concurrent claim error = %v", err)
	}
	if err := store.FailWorkspaceResume(ctx, "workspace-alpha", "resume-alpha", "fabric_failed"); err != nil {
		t.Fatalf("fail claim: %v", err)
	}
	workspaces, _ := store.ListWorkspaces(ctx, "")
	operations, _ := store.ListRuntimeOperations(ctx)
	if len(workspaces) != 1 || workspaces[0]["state"] != "suspended" || len(operations) != 1 || operations[0]["status"] != "retryable" {
		t.Fatalf("retryable state: workspaces=%#v operations=%#v", workspaces, operations)
	}
	if _, replayed, err := store.ClaimWorkspaceResume(ctx, "workspace-alpha", operation); err != nil || replayed {
		t.Fatalf("retry claim = replayed:%v err:%v", replayed, err)
	}
}

func TestMemoryWorkspaceCreateClaimIsAtomic(t *testing.T) {
	store := newMemoryTableStore()
	start := make(chan struct{})
	errorsByRequest := make(chan error, 20)
	var workers sync.WaitGroup
	for index := range 20 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			workspace, operation := workspaceCreateClaimForTest(fmt.Sprintf("hash-%d", index), fmt.Sprintf("attachment-%d", index))
			errorsByRequest <- store.ClaimWorkspaceCreate(context.Background(), workspace, operation)
		}()
	}
	close(start)
	workers.Wait()
	close(errorsByRequest)

	claimed, conflicted := 0, 0
	for err := range errorsByRequest {
		switch {
		case err == nil:
			claimed++
		case errors.Is(err, errPrimaryWorkspaceExists):
			conflicted++
		default:
			t.Fatalf("unexpected claim error: %v", err)
		}
	}
	workspaces, _ := store.ListWorkspaces(context.Background(), "acct-alpha")
	operations, _ := store.ListRuntimeOperations(context.Background())
	if claimed != 1 || conflicted != 19 || len(workspaces) != 1 || len(operations) != 1 {
		t.Fatalf("claims=%d conflicts=%d workspaces=%#v operations=%#v", claimed, conflicted, workspaces, operations)
	}
}

func TestEntWorkspaceCreateClaimSurvivesRestart(t *testing.T) {
	path := t.TempDir() + "/workspace-create-claim.sqlite"
	first := NewTestEntStateStore(t, path)
	workspace, operation := workspaceCreateClaimForTest("hash-first", "attachment-first")
	if err := first.ClaimWorkspaceCreate(context.Background(), workspace, operation); err != nil {
		t.Fatalf("first claim: %v", err)
	}

	restarted := NewTestEntStateStore(t, path)
	workspace, operation = workspaceCreateClaimForTest("hash-second", "attachment-second")
	if err := restarted.ClaimWorkspaceCreate(context.Background(), workspace, operation); !errors.Is(err, errPrimaryWorkspaceExists) {
		t.Fatalf("restart claim error=%v", err)
	}
	workspaces, _ := restarted.ListWorkspaces(context.Background(), "acct-alpha")
	operations, _ := restarted.ListRuntimeOperations(context.Background())
	if len(workspaces) != 1 || len(operations) != 1 || operations[0]["status"] != "started" {
		t.Fatalf("restart claim facts: workspaces=%#v operations=%#v", workspaces, operations)
	}
}

func TestWorkspaceCreateClaimRetriesExpiredSameAcceptedSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name string
		new  func(*testing.T) controlPlaneTableStore
	}{
		{name: "memory", new: func(*testing.T) controlPlaneTableStore { return newMemoryTableStore() }},
		{name: "sqlite", new: func(t *testing.T) controlPlaneTableStore {
			return NewTestEntStateStore(t, t.TempDir()+"/workspace-create-retry.sqlite")
		}},
		{name: "postgres", new: newPostgresWorkspaceRenewalStore},
	} {
		t.Run(tc.name, func(t *testing.T) { testWorkspaceCreateClaimRetriesExpiredSameAcceptedSnapshot(t, tc.new(t)) })
	}
}

func testWorkspaceCreateClaimRetriesExpiredSameAcceptedSnapshot(t *testing.T, store controlPlaneTableStore) {
	t.Helper()
	workspace, operation := workspaceCreateClaimForTest("hash-first", "attachment-first")
	expired := time.Now().UTC().Add(-time.Minute)
	result, err := decodeWorkspaceCreateOperation(operation)
	if err != nil {
		t.Fatal(err)
	}
	result.LeaseExpiresAt = &expired
	result.AcceptedBillingState = map[string]any{"autoRenew": false, "renewalStatus": "active"}
	operation["result"] = encodeWorkspaceCreateOperation(result)
	if err := store.ClaimWorkspaceCreate(context.Background(), workspace, operation); err != nil {
		t.Fatal(err)
	}

	mismatchWorkspace, mismatchOperation := workspaceCreateClaimForTest("hash-first", "attachment-first")
	mismatchResult, err := decodeWorkspaceCreateOperation(mismatchOperation)
	if err != nil {
		t.Fatal(err)
	}
	mismatchResult.AcceptedBillingState = map[string]any{"autoRenew": true, "renewalStatus": "active"}
	mismatchOperation["result"] = encodeWorkspaceCreateOperation(mismatchResult)
	if err := store.ClaimWorkspaceCreate(context.Background(), mismatchWorkspace, mismatchOperation); !errors.Is(err, errPrimaryWorkspaceExists) {
		t.Fatalf("changed accepted billing snapshot error=%v", err)
	}

	retryWorkspace, retryOperation := workspaceCreateClaimForTest("hash-first", "attachment-first")
	lease := time.Now().UTC().Add(time.Minute)
	retryResult, err := decodeWorkspaceCreateOperation(retryOperation)
	if err != nil {
		t.Fatal(err)
	}
	retryResult.LeaseExpiresAt = &lease
	retryResult.AcceptedBillingState = cloneMap(result.AcceptedBillingState)
	retryOperation["result"] = encodeWorkspaceCreateOperation(retryResult)
	if err := store.ClaimWorkspaceCreate(context.Background(), retryWorkspace, retryOperation); err != nil {
		t.Fatalf("retry same expired claim: %v", err)
	}
	if err := store.ClaimWorkspaceCreate(context.Background(), retryWorkspace, retryOperation); !errors.Is(err, errPrimaryWorkspaceExists) {
		t.Fatalf("active retry claim error=%v", err)
	}

	changedWorkspace, changedOperation := workspaceCreateClaimForTest("hash-changed", "attachment-first")
	if err := store.ClaimWorkspaceCreate(context.Background(), changedWorkspace, changedOperation); !errors.Is(err, errPrimaryWorkspaceExists) {
		t.Fatalf("changed retry claim error=%v", err)
	}
	secondWorkspace, secondOperation := workspaceCreateClaimForAccountForTest("acct-alpha", "workspace-second", "hash-second", "attachment-second")
	if err := store.ClaimWorkspaceCreate(context.Background(), secondWorkspace, secondOperation); !errors.Is(err, errPrimaryWorkspaceExists) {
		t.Fatalf("second Workspace claim error=%v", err)
	}
}

func TestWorkspaceCreateClaimRecoversLegacyMissingAcceptedSnapshot(t *testing.T) {
	for _, storeCase := range []struct {
		name string
		new  func(*testing.T) controlPlaneTableStore
	}{
		{name: "memory", new: func(*testing.T) controlPlaneTableStore { return newMemoryTableStore() }},
		{name: "postgres", new: newPostgresWorkspaceRenewalStore},
	} {
		for _, operationCase := range []struct {
			name   string
			status string
			expire bool
		}{
			{name: "retryable", status: "retryable"},
			{name: "expired-started", status: "started", expire: true},
		} {
			t.Run(storeCase.name+"/"+operationCase.name, func(t *testing.T) {
				ctx := context.Background()
				store := storeCase.new(t)
				workspace, legacyOperation := workspaceCreateClaimForTest("hash-legacy", "attachment-legacy")
				legacyResult, err := decodeWorkspaceCreateOperation(legacyOperation)
				if err != nil {
					t.Fatal(err)
				}
				legacyResult.Workspace.PackageID = "basic"
				workspace["packageId"] = legacyResult.Workspace.PackageID
				workspace["computeAllocationId"] = legacyResult.Workspace.ComputeID
				workspace["attachmentId"] = legacyResult.Workspace.AttachmentID
				if operationCase.expire {
					expired := time.Now().UTC().Add(-time.Minute)
					legacyResult.LeaseExpiresAt = &expired
				}
				legacyOperation["status"] = operationCase.status
				legacyOperation["result"] = encodeWorkspaceCreateOperation(legacyResult)
				if err := store.ClaimWorkspaceCreate(ctx, workspace, legacyOperation); err != nil {
					t.Fatalf("seed legacy claim: %v", err)
				}

				billingRow := canonicalWorkspaceRenewalRow(false)
				billingRow["ownerUserId"] = legacyResult.Workspace.OwnerID
				billingRow["currentComputeAllocationId"] = legacyResult.Workspace.ComputeID
				billingRow["computeAllocationId"] = legacyResult.Workspace.ComputeID
				billingRow["storageId"] = legacyResult.Workspace.VolumeID
				acceptedBillingState := workspaceAcceptedBillingState(billingRow)
				if acceptedBillingState == nil {
					t.Fatal("test billing state is not canonical")
				}
				canonicalWorkspace := workspaceProjectionBillingRow(legacyResult.Workspace, acceptedBillingState)
				lease := time.Now().UTC().Add(time.Minute)
				claimResult := legacyResult
				claimResult.LeaseExpiresAt = &lease
				claimResult.AcceptedBillingState = cloneMap(acceptedBillingState)
				claimOperation := workspaceCreateOperationRow(stringValue(legacyOperation["id"]), "started", claimResult)
				if err := store.ClaimWorkspaceCreate(ctx, canonicalWorkspace, claimOperation); !errors.Is(err, errPrimaryWorkspaceExists) {
					t.Fatalf("missing persisted billing truth error=%v", err)
				}
				manualWorkspace := workspaceProjectionBillingRow(legacyResult.Workspace, map[string]any{
					"autoRenew": false, "renewalStatus": "manual_review", "manualReviewReason": workspaceBillingLegacyMismatch,
				})
				if err := store.SaveWorkspace(ctx, manualWorkspace); err != nil {
					t.Fatalf("persist manual-review Workspace truth: %v", err)
				}
				if err := store.ClaimWorkspaceCreate(ctx, canonicalWorkspace, claimOperation); !errors.Is(err, errPrimaryWorkspaceExists) {
					t.Fatalf("manual-review persisted billing truth error=%v", err)
				}
				if err := store.SaveWorkspace(ctx, canonicalWorkspace); err != nil {
					t.Fatalf("persist migrated Workspace truth: %v", err)
				}

				changedHash := claimResult
				changedHash.RequestHash = "hash-other"
				if err := store.ClaimWorkspaceCreate(ctx, canonicalWorkspace, workspaceCreateOperationRow(stringValue(legacyOperation["id"]), "started", changedHash)); !errors.Is(err, errPrimaryWorkspaceExists) {
					t.Fatalf("changed request hash error=%v", err)
				}
				changedBilling := cloneMap(acceptedBillingState)
				changedBilling["autoRenew"] = true
				changedBilling["authorizedBy"] = legacyResult.Workspace.OwnerID
				changedBilling["authorizedAt"] = "2026-07-17T01:02:03Z"
				changedSnapshot := claimResult
				changedSnapshot.AcceptedBillingState = changedBilling
				if err := store.ClaimWorkspaceCreate(ctx, workspaceProjectionBillingRow(legacyResult.Workspace, changedBilling), workspaceCreateOperationRow(stringValue(legacyOperation["id"]), "started", changedSnapshot)); !errors.Is(err, errPrimaryWorkspaceExists) {
					t.Fatalf("changed accepted billing snapshot error=%v", err)
				}

				if err := store.ClaimWorkspaceCreate(ctx, canonicalWorkspace, claimOperation); err != nil {
					t.Fatalf("recover legacy claim: %v", err)
				}
				operations, err := store.ListRuntimeOperations(ctx)
				if err != nil || len(operations) != 1 || operations[0]["status"] != "started" {
					t.Fatalf("recovered operations=%#v err=%v", operations, err)
				}
				recovered, err := decodeWorkspaceCreateOperation(operations[0])
				if err != nil || workspaceCreateClaimIdentity(recovered) != workspaceCreateClaimIdentity(claimResult) {
					t.Fatalf("recovered claim result=%#v err=%v", recovered, err)
				}
				workspaces, err := store.ListWorkspaces(ctx, legacyResult.Workspace.AccountID)
				if err != nil || len(workspaces) != 1 || workspaceAcceptedBillingState(workspaces[0]) == nil {
					t.Fatalf("recovered Workspaces=%#v err=%v", workspaces, err)
				}
			})
		}
	}
}

func TestWorkspaceCreateClaimLegacyUpgradeRequiresActiveBillingTruth(t *testing.T) {
	for _, storeCase := range []struct {
		name string
		new  func(*testing.T) controlPlaneTableStore
	}{
		{name: "memory", new: func(*testing.T) controlPlaneTableStore { return newMemoryTableStore() }},
		{name: "postgres", new: newPostgresWorkspaceRenewalStore},
	} {
		for _, operationCase := range []struct {
			name   string
			status string
			expire bool
		}{
			{name: "retryable", status: "retryable"},
			{name: "expired-started", status: "started", expire: true},
		} {
			for _, billingCase := range []struct {
				name    string
				state   map[string]any
				persist bool
			}{
				{name: "empty", state: map[string]any{}},
				{name: "manual-review", state: map[string]any{"autoRenew": false, "renewalStatus": "manual_review", "manualReviewReason": workspaceBillingLegacyMismatch}, persist: true},
			} {
				t.Run(storeCase.name+"/"+operationCase.name+"/"+billingCase.name, func(t *testing.T) {
					ctx := context.Background()
					store := storeCase.new(t)
					workspace, legacyOperation := workspaceCreateClaimForTest("hash-legacy-invalid", "attachment-legacy-invalid")
					legacyResult, err := decodeWorkspaceCreateOperation(legacyOperation)
					if err != nil {
						t.Fatal(err)
					}
					legacyResult.Workspace.PackageID = "basic"
					workspace["packageId"] = legacyResult.Workspace.PackageID
					workspace["computeAllocationId"] = legacyResult.Workspace.ComputeID
					workspace["attachmentId"] = legacyResult.Workspace.AttachmentID
					if operationCase.expire {
						expired := time.Now().UTC().Add(-time.Minute)
						legacyResult.LeaseExpiresAt = &expired
					}
					legacyOperation["status"] = operationCase.status
					legacyOperation["result"] = encodeWorkspaceCreateOperation(legacyResult)
					if err := store.ClaimWorkspaceCreate(ctx, workspace, legacyOperation); err != nil {
						t.Fatalf("seed legacy claim: %v", err)
					}
					requestedWorkspace := workspaceProjectionBillingRow(legacyResult.Workspace, billingCase.state)
					if billingCase.persist {
						if err := store.SaveWorkspace(ctx, requestedWorkspace); err != nil {
							t.Fatalf("persist %s Workspace truth: %v", billingCase.name, err)
						}
					}
					claimResult := legacyResult
					lease := time.Now().UTC().Add(time.Minute)
					claimResult.LeaseExpiresAt = &lease
					claimResult.AcceptedBillingState = cloneMap(billingCase.state)
					claimOperation := workspaceCreateOperationRow(stringValue(legacyOperation["id"]), "started", claimResult)
					if billingCase.name == "empty" {
						var payload map[string]any
						if err := json.Unmarshal([]byte(stringValue(claimOperation["result"])), &payload); err != nil {
							t.Fatal(err)
						}
						payload["acceptedBillingState"] = map[string]any{}
						encoded, err := json.Marshal(payload)
						if err != nil {
							t.Fatal(err)
						}
						claimOperation["result"] = string(encoded)
					}
					before, err := store.ListRuntimeOperations(ctx)
					if err != nil {
						t.Fatal(err)
					}
					claimErr := store.ClaimWorkspaceCreate(ctx, requestedWorkspace, claimOperation)
					after, err := store.ListRuntimeOperations(ctx)
					if err != nil {
						t.Fatal(err)
					}
					if !errors.Is(claimErr, errPrimaryWorkspaceExists) || !reflect.DeepEqual(after, before) {
						t.Fatalf("legacy %s claim error=%v before=%#v after=%#v", billingCase.name, claimErr, before, after)
					}
				})
			}
		}
	}
}

func TestWorkspaceCreateClaimBindsOuterAndProjectedIdentity(t *testing.T) {
	for _, storeCase := range []struct {
		name string
		new  func(*testing.T) controlPlaneTableStore
	}{
		{name: "memory", new: func(*testing.T) controlPlaneTableStore { return newMemoryTableStore() }},
		{name: "postgres", new: newPostgresWorkspaceRenewalStore},
	} {
		for _, attack := range []string{
			"requested-outer-account", "requested-outer-workspace", "stored-outer-account", "stored-outer-workspace",
			"result-workspace", "result-account",
		} {
			t.Run(storeCase.name+"/"+attack, func(t *testing.T) {
				ctx := context.Background()
				store := storeCase.new(t)
				workspace, storedOperation := workspaceCreateClaimForTest("hash-identity", "attachment-identity")
				storedResult, err := decodeWorkspaceCreateOperation(storedOperation)
				if err != nil {
					t.Fatal(err)
				}
				storedResult.Workspace.PackageID = "basic"
				billingRow := canonicalWorkspaceRenewalRow(false)
				billingRow["ownerUserId"] = storedResult.Workspace.OwnerID
				billingRow["currentComputeAllocationId"] = storedResult.Workspace.ComputeID
				billingRow["computeAllocationId"] = storedResult.Workspace.ComputeID
				billingRow["storageId"] = storedResult.Workspace.VolumeID
				acceptedBillingState := workspaceAcceptedBillingState(billingRow)
				if acceptedBillingState == nil {
					t.Fatal("test billing state is not canonical")
				}
				canonicalWorkspace := workspaceProjectionBillingRow(storedResult.Workspace, acceptedBillingState)
				workspace = canonicalWorkspace
				storedOperation["status"] = "retryable"
				switch attack {
				case "stored-outer-account":
					storedOperation["accountId"] = "acct-other"
				case "stored-outer-workspace":
					storedOperation["workspaceId"] = "workspace-other"
				case "result-workspace":
					storedResult.Workspace.ID = "workspace-other"
				case "result-account":
					storedResult.Workspace.AccountID = "acct-other"
					storedResult.AcceptedBillingState = cloneMap(acceptedBillingState)
				}
				storedOperation["result"] = encodeWorkspaceCreateOperation(storedResult)
				if err := store.SaveWorkspace(ctx, workspace); err != nil {
					t.Fatalf("seed Workspace: %v", err)
				}
				if err := store.SaveRuntimeOperation(ctx, storedOperation); err != nil {
					t.Fatalf("seed mismatched operation: %v", err)
				}

				claimResult := storedResult
				lease := time.Now().UTC().Add(time.Minute)
				claimResult.LeaseExpiresAt = &lease
				claimResult.AcceptedBillingState = cloneMap(acceptedBillingState)
				claimOperation := workspaceCreateOperationRow(stringValue(storedOperation["id"]), "started", claimResult)
				claimOperation["accountId"] = stringValue(canonicalWorkspace["accountId"])
				claimOperation["workspaceId"] = stringValue(canonicalWorkspace["id"])
				requestedWorkspace := canonicalWorkspace
				switch attack {
				case "requested-outer-account":
					claimOperation["accountId"] = "acct-other"
				case "requested-outer-workspace":
					claimOperation["workspaceId"] = "workspace-other"
				}
				before, err := store.ListRuntimeOperations(ctx)
				if err != nil {
					t.Fatal(err)
				}
				claimErr := store.ClaimWorkspaceCreate(ctx, requestedWorkspace, claimOperation)
				after, err := store.ListRuntimeOperations(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if !errors.Is(claimErr, errPrimaryWorkspaceExists) || !reflect.DeepEqual(after, before) {
					t.Fatalf("identity attack %s error=%v before=%#v after=%#v", attack, claimErr, before, after)
				}
			})
		}
	}
}

func TestPostgresPrimaryWorkspaceAndVerifierFactsSurviveRestart(t *testing.T) {
	admin := openControlPlaneTestPostgres(t)
	schema := fmt.Sprintf("control_plane_primary_workspace_%d", time.Now().UnixNano())
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
		_ = admin.Close()
	})
	databaseURL := controlPlaneTestPostgresURL(t, "postgres", schema)

	stateStore, err := NewPostgresEntStateStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	first := stateStore.(*postgresEntStateStore)
	workspace, operation := workspaceCreateClaimForTest("postgres-first", "attachment-first")
	workspace["verificationSlotId"], workspace["customerProduct"] = "verification-slot-01", false
	if err := first.ClaimWorkspaceCreate(context.Background(), workspace, operation); err != nil {
		t.Fatalf("claim primary Workspace: %v", err)
	}
	costTags := map[string]string{"opl_account_id": "acct-alpha", "opl_workspace_id": stringValue(workspace["id"])}
	if err := first.SaveCompute(context.Background(), map[string]any{
		"id": "compute-slot", "accountId": "acct-alpha", "workspaceId": workspace["id"], "costTags": costTags,
		"nodePoolId": "np-slot-01", "instanceType": "SA5.MEDIUM4", "requestedPeriodMonths": 1, "periodMonths": 1,
		"verificationSlotId": "verification-slot-01", "customerProduct": false,
	}); err != nil {
		t.Fatal(err)
	}
	if err := first.SaveStorage(context.Background(), map[string]any{
		"id": "storage-slot", "accountId": "acct-alpha", "workspaceId": workspace["id"], "costTags": costTags,
		"requestedPeriodMonths": 1, "periodMonths": 1, "verificationSlotId": "verification-slot-01", "customerProduct": false,
		"pvName": "pv-slot-01", "persistentVolumeName": "pv-slot-01",
	}); err != nil {
		t.Fatal(err)
	}
	if err := first.client.Close(); err != nil {
		t.Fatal(err)
	}

	restartedState, err := NewPostgresEntStateStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	restarted := restartedState.(*postgresEntStateStore)
	t.Cleanup(func() { _ = restarted.client.Close() })
	workspaces, _ := restarted.ListWorkspaces(context.Background(), "acct-alpha")
	computes, _ := restarted.ListComputes(context.Background(), "acct-alpha")
	storages, _ := restarted.ListStorages(context.Background(), "acct-alpha")
	if len(workspaces) != 1 || workspaces[0]["verificationSlotId"] != "verification-slot-01" || workspaces[0]["customerProduct"] != false {
		t.Fatalf("restarted Workspaces=%#v", workspaces)
	}
	compute, storage := recordByID(computes, "compute-slot"), recordByID(storages, "storage-slot")
	if compute["nodePoolId"] != "np-slot-01" || compute["instanceType"] != "SA5.MEDIUM4" || mapField(compute, "costTags")["opl_account_id"] != "acct-alpha" {
		t.Fatalf("restarted compute=%#v", compute)
	}
	if storage["pvName"] != "pv-slot-01" || storage["persistentVolumeName"] != "pv-slot-01" || mapField(storage, "costTags")["opl_workspace_id"] != workspace["id"] {
		t.Fatalf("restarted storage=%#v", storage)
	}
	secondWorkspace, secondOperation := workspaceCreateClaimForAccountForTest("acct-alpha", "ws-other", "postgres-second", "attachment-second")
	if err := restarted.ClaimWorkspaceCreate(context.Background(), secondWorkspace, secondOperation); !errors.Is(err, errPrimaryWorkspaceExists) {
		t.Fatalf("second primary claim error=%v", err)
	}

	start := make(chan struct{})
	errorsByRequest := make(chan error, 10)
	var workers sync.WaitGroup
	for index := range 10 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			workspaceID := fmt.Sprintf("ws-race-%d", index)
			row, op := workspaceCreateClaimForAccountForTest("acct-race", workspaceID, fmt.Sprintf("race-%d", index), fmt.Sprintf("attachment-%d", index))
			errorsByRequest <- restarted.ClaimWorkspaceCreate(context.Background(), row, op)
		}()
	}
	close(start)
	workers.Wait()
	close(errorsByRequest)
	claimed, conflicted := 0, 0
	for err := range errorsByRequest {
		if err == nil {
			claimed++
		} else if errors.Is(err, errPrimaryWorkspaceExists) {
			conflicted++
		} else {
			t.Fatalf("Postgres concurrent claim error=%v", err)
		}
	}
	if claimed != 1 || conflicted != 9 {
		t.Fatalf("Postgres concurrent claims=%d conflicts=%d", claimed, conflicted)
	}
}

func workspaceCreateClaimForTest(requestHash, attachmentID string) (map[string]any, map[string]any) {
	return workspaceCreateClaimForAccountForTest("acct-alpha", primaryWorkspaceID("acct-alpha"), requestHash, attachmentID)
}

func workspaceCreateClaimForAccountForTest(accountID, workspaceID, requestHash, attachmentID string) (map[string]any, map[string]any) {
	projection := domain.WorkspaceProjection{ID: workspaceID, AccountID: accountID, OwnerID: "usr-owner", Name: "Primary", ComputeID: "compute-alpha", VolumeID: "storage-alpha", AttachmentID: attachmentID, Status: "provisioning"}
	workspace := map[string]any{
		"id": workspaceID, "accountId": accountID, "ownerAccountId": accountID, "ownerUserId": "usr-owner", "name": "Primary",
		"state": "provisioning", "status": "provisioning", "storageId": "storage-alpha", "currentComputeAllocationId": "compute-alpha", "currentAttachmentId": attachmentID,
	}
	operation := workspaceCreateOperationRow("create-"+stableID(workspaceID)[:18], "started", workspaceCreateOperationResult{RequestHash: requestHash, Workspace: projection})
	return workspace, operation
}

func TestEntStateStorePersistsExecutionIdentityAndApproval(t *testing.T) {
	store := NewTestEntStateStore(t, t.TempDir()+"/execution.sqlite")
	ctx := context.Background()
	if err := store.SaveProjectTaskSyncHead(ctx, map[string]any{
		"id":             "project-alpha",
		"kind":           "project",
		"organizationId": "org-alpha",
		"workspaceId":    "workspace-alpha",
		"localAliasId":   "local-project-alpha",
		"version":        int64(1),
		"status":         "active",
	}); err != nil {
		t.Fatalf("save project identity: %v", err)
	}
	if err := store.SaveProjectTaskSyncHead(ctx, map[string]any{
		"id":             "task-alpha",
		"kind":           "task",
		"organizationId": "org-alpha",
		"workspaceId":    "workspace-alpha",
		"projectId":      "project-alpha",
		"localAliasId":   "local-task-alpha",
		"version":        int64(1),
		"status":         "draft",
	}); err != nil {
		t.Fatalf("save task identity: %v", err)
	}
	if err := store.SaveExecutionRequest(ctx, map[string]any{
		"id":             "request-alpha",
		"organizationId": "org-alpha",
		"workspaceId":    "workspace-alpha",
		"projectId":      "project-alpha",
		"taskId":         "task-alpha",
		"actorUserId":    "usr-alpha",
		"approvalId":     "approval-alpha",
		"approvalStatus": "approved",
		"approvedBy":     "usr-reviewer",
		"status":         "approved",
		"environmentRef": "environment-alpha",
		"idempotencyKey": "request-once",
		"version":        int64(2),
	}); err != nil {
		t.Fatalf("save execution request: %v", err)
	}

	heads, err := store.ListProjectTaskSyncHeads(ctx)
	headsByID := map[string]map[string]any{}
	for _, head := range heads {
		headsByID[stringValue(head["id"])] = head
	}
	if err != nil || len(heads) != 2 || headsByID["project-alpha"]["projectId"] != "project-alpha" || headsByID["task-alpha"]["taskId"] != "task-alpha" {
		t.Fatalf("unexpected sync heads: %#v, %v", heads, err)
	}
	requests, err := store.ListExecutionRequests(ctx)
	if err != nil || len(requests) != 1 || requests[0]["requestId"] != "request-alpha" || requests[0]["approvalStatus"] != "approved" || requests[0]["version"] != int64(2) {
		t.Fatalf("unexpected execution requests: %#v, %v", requests, err)
	}
}

func TestEntStateStorePersistsWorkspaceSyncEvents(t *testing.T) {
	path := t.TempDir() + "/workspace-sync.sqlite"
	store := NewTestEntStateStore(t, path).(*postgresEntStateStore)
	ctx := context.Background()
	events := []map[string]any{
		{
			"id":             "mutation-alpha",
			"operationId":    "operation-alpha",
			"workspaceId":    "workspace-alpha",
			"cursor":         int64(1001),
			"entityKind":     "project",
			"projectId":      "project-alpha",
			"clientId":       "client-alpha",
			"actorUserId":    "user-alpha",
			"baseVersion":    int64(1),
			"serverVersion":  int64(2),
			"operation":      "replace",
			"status":         "accepted",
			"payload":        map[string]any{"title": "Cloud title"},
			"contentDigest":  "sha256:alpha",
			"idempotencyKey": "mutation-once",
			"requestHash":    "hash-alpha",
			"occurredAt":     "2026-07-11T00:00:00Z",
		},
		{
			"id":             "mutation-conflict",
			"operationId":    "operation-conflict",
			"workspaceId":    "workspace-alpha",
			"cursor":         int64(1002),
			"entityKind":     "project",
			"projectId":      "project-alpha",
			"clientId":       "client-beta",
			"actorUserId":    "user-beta",
			"baseVersion":    int64(1),
			"serverVersion":  int64(2),
			"operation":      "replace",
			"status":         "conflict",
			"payload":        map[string]any{"current": map[string]any{"title": "Cloud title"}, "incoming": map[string]any{"title": "Offline title"}},
			"idempotencyKey": "mutation-conflict-once",
			"requestHash":    "hash-conflict",
			"conflictId":     "conflict-alpha",
			"occurredAt":     "2026-07-11T00:01:00Z",
		},
	}
	for _, event := range events {
		if err := store.SaveWorkspaceSyncEvent(ctx, event); err != nil {
			t.Fatalf("save sync event: %v", err)
		}
	}
	if err := store.client.Close(); err != nil {
		t.Fatalf("close sync event store: %v", err)
	}
	store = NewTestEntStateStore(t, path).(*postgresEntStateStore)

	stored, err := store.ListWorkspaceSyncEvents(ctx, "workspace-alpha", 0, 10)
	if err != nil {
		t.Fatalf("list sync events: %v", err)
	}
	if len(stored) != 2 || stored[0]["id"] != "mutation-alpha" || stored[1]["conflictId"] != "conflict-alpha" {
		t.Fatalf("unexpected sync events: %#v", stored)
	}
	payload, ok := stored[1]["payload"].(map[string]any)
	if !ok || payload["current"] == nil || payload["incoming"] == nil || stored[0]["cursor"] != int64(1001) || stored[0]["requestHash"] != "hash-alpha" || stored[0]["operationId"] != "operation-alpha" || stored[0]["actorUserId"] != "user-alpha" || stored[0]["occurredAt"] != "2026-07-11T00:00:00Z" {
		t.Fatalf("sync event fields were not preserved: %#v", stored)
	}
}

func TestEntStateStoreUpdatesExecutionRequestWithoutRecreatingIt(t *testing.T) {
	store := NewTestEntStateStore(t, t.TempDir()+"/execution-update.sqlite").(*postgresEntStateStore)
	ctx := context.Background()
	row := map[string]any{
		"id":             "request-alpha",
		"organizationId": "org-alpha",
		"workspaceId":    "workspace-alpha",
		"projectId":      "project-alpha",
		"taskId":         "task-alpha",
		"actorUserId":    "usr-alpha",
		"status":         "awaiting_approval",
		"idempotencyKey": "request-once",
	}
	if err := store.SaveExecutionRequest(ctx, row); err != nil {
		t.Fatalf("save execution request: %v", err)
	}
	before, err := store.client.ExecutionRequest.Get(ctx, "request-alpha")
	if err != nil {
		t.Fatal(err)
	}
	row["status"] = "approved"
	row["approvalStatus"] = "approved"
	if err := store.SaveExecutionRequest(ctx, row); err != nil {
		t.Fatalf("update execution request: %v", err)
	}
	after, err := store.client.ExecutionRequest.Get(ctx, "request-alpha")
	if err != nil {
		t.Fatal(err)
	}
	if !after.CreatedAt.Equal(before.CreatedAt) || after.Status != "approved" {
		t.Fatalf("request was recreated instead of updated: before=%#v after=%#v", before, after)
	}
}

func TestEntStateStoreUpdatesResourcesWithoutRecreatingThem(t *testing.T) {
	store := NewTestEntStateStore(t, t.TempDir()+"/resource-update.sqlite").(*postgresEntStateStore)
	ctx := context.Background()
	createdAt := "2026-07-01T00:00:00Z"

	compute := map[string]any{
		"id": "compute-alpha", "accountId": "acct-alpha", "status": "provisioning",
		"lastProviderSyncError": "provider temporarily unavailable", "createdAt": createdAt,
	}
	if err := store.SaveCompute(ctx, compute); err != nil {
		t.Fatal(err)
	}
	delete(compute, "createdAt")
	compute["status"], compute["lastProviderSyncError"] = "running", ""
	if err := store.SaveCompute(ctx, compute); err != nil {
		t.Fatal(err)
	}
	storedCompute, err := store.client.ComputeAllocation.Get(ctx, "compute-alpha")
	if err != nil {
		t.Fatal(err)
	}
	if storedCompute.CreatedAt.Format(time.RFC3339) != createdAt || storedCompute.Status != "running" || storedCompute.LastProviderSyncError != "" {
		t.Fatalf("compute was recreated or not updated: %#v", storedCompute)
	}

	storage := map[string]any{
		"id": "storage-alpha", "accountId": "acct-alpha", "status": "creating",
		"lastProviderSyncError": "provider temporarily unavailable", "createdAt": createdAt,
	}
	if err := store.SaveStorage(ctx, storage); err != nil {
		t.Fatal(err)
	}
	delete(storage, "createdAt")
	storage["status"], storage["lastProviderSyncError"] = "available", ""
	if err := store.SaveStorage(ctx, storage); err != nil {
		t.Fatal(err)
	}
	storedStorage, err := store.client.StorageVolume.Get(ctx, "storage-alpha")
	if err != nil {
		t.Fatal(err)
	}
	if storedStorage.CreatedAt.Format(time.RFC3339) != createdAt || storedStorage.Status != "available" || storedStorage.LastProviderSyncError != "" {
		t.Fatalf("storage was recreated or not updated: %#v", storedStorage)
	}
}

func TestEntStateStoreRejectsExecutionIdentityOverwrite(t *testing.T) {
	store := NewTestEntStateStore(t, t.TempDir()+"/execution-conflict.sqlite")
	ctx := context.Background()
	row := map[string]any{
		"id":             "request-alpha",
		"organizationId": "org-alpha",
		"workspaceId":    "workspace-alpha",
		"projectId":      "project-alpha",
		"taskId":         "task-alpha",
		"actorUserId":    "usr-alpha",
		"environmentRef": "environment-alpha",
		"status":         "awaiting_approval",
		"idempotencyKey": "request-once",
	}
	if err := store.SaveExecutionRequest(ctx, row); err != nil {
		t.Fatal(err)
	}
	row["environmentRef"] = "environment-beta"
	if err := store.SaveExecutionRequest(ctx, row); !errors.Is(err, errIdempotencyConflict) {
		t.Fatalf("overwrite error = %v, want errIdempotencyConflict", err)
	}
}

func TestControlPlaneAdminFactsSurviveServerRestart(t *testing.T) {
	store := NewTestEntStateStore(t, t.TempDir()+"/admin-facts.sqlite")
	first, err := newControlPlaneAppWithStore(store)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.tables.SaveAccount(context.Background(), map[string]any{"id": "acct-alpha", "status": "active"}); err != nil {
		t.Fatal(err)
	}
	if err := first.tables.SaveUser(context.Background(), map[string]any{"id": "usr-alpha", "email": "alpha@example.com", "accountId": "acct-alpha", "role": "owner", "status": "active"}); err != nil {
		t.Fatal(err)
	}
	organization, err := first.createOrganization(map[string]any{"name": "Research Lab", "billingAccountId": "acct-alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.createMembership(map[string]any{"organizationId": organization["id"], "userId": "usr-alpha", "accountId": "acct-alpha", "role": "owner"}); err != nil {
		t.Fatal(err)
	}
	if err := first.rememberRuntimeOperations([]clients.FabricOperation{{ID: "fabric-op-alpha", OperationID: "operation-alpha", WorkspaceID: "ws-alpha", ResourceID: "compute-alpha", ResourceKind: "compute_allocation", Status: "failed", ErrorCode: "compute_machine_unavailable", RedactedProviderPayload: map[string]any{"costTags": map[string]any{"opl_operation_id": "operation-alpha"}}}}); err != nil {
		t.Fatal(err)
	}
	if err := first.rememberReconciliation(clients.ReconciliationResult{ID: "reconcile-alpha", Status: "mismatch", BlockNewWorkspaces: true, Reason: "provider_cost_gap"}); err != nil {
		t.Fatal(err)
	}

	restarted, err := newControlPlaneAppWithStore(store)
	if err != nil {
		t.Fatal(err)
	}
	state := restarted.managementState(true, nil)
	if len(state["organizations"].([]any)) != 1 || len(state["memberships"].([]any)) != 1 || len(state["runtimeOperations"].([]any)) != 1 {
		t.Fatalf("admin facts did not survive restart: %#v", state)
	}
	operation := state["runtimeOperations"].([]any)[0].(map[string]any)
	payload := operation["redactedProviderPayload"].(map[string]any)
	if payload["costTags"].(map[string]any)["opl_operation_id"] != "operation-alpha" {
		t.Fatalf("runtime evidence did not survive restart: %#v", operation)
	}
	if operation["errorCode"] != "compute_machine_unavailable" {
		t.Fatalf("runtime error code did not survive restart: %#v", operation)
	}
	reconciliation := state["billingReconciliation"].(map[string]any)
	guard := reconciliation["guard"].(map[string]any)
	if guard["blockNewWorkspaces"] != true {
		t.Fatalf("reconciliation did not survive restart: %#v", reconciliation)
	}
}
