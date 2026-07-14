package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

type monthlySub2API struct {
	events       *[]string
	balances     []int64
	chargeErrors []error
	charges      []clients.Sub2APIChargeInput
}

func (s *monthlySub2API) Version(context.Context) (string, error) { return "0.1.151", nil }

func (s *monthlySub2API) Balance(_ context.Context, userID int64) (clients.Sub2APIBalance, error) {
	*s.events = append(*s.events, "sub2api.balance")
	if len(s.balances) == 0 {
		return clients.Sub2APIBalance{}, errors.New("unexpected balance read")
	}
	balance := s.balances[0]
	s.balances = s.balances[1:]
	return clients.Sub2APIBalance{UserID: userID, USDMicros: balance}, nil
}

func (s *monthlySub2API) Charge(_ context.Context, input clients.Sub2APIChargeInput) (clients.Sub2APICharge, error) {
	*s.events = append(*s.events, "sub2api.charge")
	s.charges = append(s.charges, input)
	if len(s.chargeErrors) > 0 {
		err := s.chargeErrors[0]
		s.chargeErrors = s.chargeErrors[1:]
		if err != nil {
			return clients.Sub2APICharge{}, err
		}
	}
	return clients.Sub2APICharge{Code: input.Code, UserID: input.UserID, ChargeUSDMicros: input.ChargeUSDMicros, Status: "used"}, nil
}

type monthlyFabric struct {
	fakeFabricClient
	events     *[]string
	createErr  error
	cleanupErr error
	computeIDs []string
	storageIDs []string
}

type provisioningMonthlyFabric struct {
	monthlyFabric
	syncCalls int
}

func (f *provisioningMonthlyFabric) CreateComputeAllocation(_ context.Context, input clients.ComputeAllocationInput, _ string) (clients.ComputeAllocation, error) {
	*f.events = append(*f.events, "fabric.compute.prepare")
	f.computeIDs = append(f.computeIDs, input.ID)
	return clients.ComputeAllocation{ID: input.ID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, PackageID: input.PackageID, Status: "provisioning", Provider: "tencent-tke", ProviderRequestID: "req-" + input.ID}, nil
}

func (f *provisioningMonthlyFabric) SyncComputeAllocation(_ context.Context, id string) (clients.ComputeAllocation, error) {
	*f.events = append(*f.events, "fabric.compute.sync")
	f.syncCalls++
	return clients.ComputeAllocation{ID: id, Status: "running", Provider: "tencent-tke", ProviderResourceID: "ins-" + id, ProviderRequestID: "req-" + id}, nil
}

func (f *provisioningMonthlyFabric) CreateStorageVolume(_ context.Context, input clients.StorageVolumeInput, _ string) (clients.StorageVolume, error) {
	*f.events = append(*f.events, "fabric.storage.prepare")
	f.storageIDs = append(f.storageIDs, input.ID)
	return clients.StorageVolume{ID: input.ID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, SizeGB: input.SizeGB, Status: "provisioning", Provider: "tencent-tke", ProviderRequestID: "req-" + input.ID}, nil
}

func (f *provisioningMonthlyFabric) SyncStorageVolume(_ context.Context, id string) (clients.StorageVolume, error) {
	*f.events = append(*f.events, "fabric.storage.sync")
	f.syncCalls++
	return clients.StorageVolume{ID: id, Status: "available", Provider: "tencent-tke", ProviderResourceID: "disk-" + id, ProviderRequestID: "req-" + id}, nil
}

func (f *monthlyFabric) CreateComputeAllocation(_ context.Context, input clients.ComputeAllocationInput, _ string) (clients.ComputeAllocation, error) {
	*f.events = append(*f.events, "fabric.compute.prepare")
	f.computeIDs = append(f.computeIDs, input.ID)
	if f.createErr != nil {
		return clients.ComputeAllocation{ID: input.ID}, f.createErr
	}
	return clients.ComputeAllocation{ID: input.ID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, PackageID: input.PackageID, Status: "running", Provider: "tencent-tke", ProviderResourceID: "ins-" + input.ID, ProviderRequestID: "req-" + input.ID}, nil
}

func (f *monthlyFabric) CreateStorageVolume(_ context.Context, input clients.StorageVolumeInput, _ string) (clients.StorageVolume, error) {
	*f.events = append(*f.events, "fabric.storage.prepare")
	f.storageIDs = append(f.storageIDs, input.ID)
	if f.createErr != nil {
		return clients.StorageVolume{ID: input.ID}, f.createErr
	}
	return clients.StorageVolume{ID: input.ID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, SizeGB: input.SizeGB, Status: "available", Provider: "tencent-tke", ProviderResourceID: "disk-" + input.ID, ProviderRequestID: "req-" + input.ID}, nil
}

func (f *monthlyFabric) DestroyComputeAllocation(_ context.Context, id, _ string) (clients.ComputeAllocation, error) {
	*f.events = append(*f.events, "fabric.compute.cleanup")
	if f.cleanupErr != nil {
		return clients.ComputeAllocation{ID: id}, f.cleanupErr
	}
	return clients.ComputeAllocation{ID: id, Status: "destroyed"}, nil
}

func (f *monthlyFabric) DestroyStorageVolume(_ context.Context, id, _ string) (clients.StorageVolume, error) {
	*f.events = append(*f.events, "fabric.storage.cleanup")
	if f.cleanupErr != nil {
		return clients.StorageVolume{ID: id}, f.cleanupErr
	}
	return clients.StorageVolume{ID: id, Status: "destroyed"}, nil
}

type monthlyLedger struct {
	fakeLedgerClient
	events        *[]string
	receiptErrors []error
	receipts      []clients.ReceiptInput
}

type scopedReceiptLedger struct {
	fakeLedgerClient
	receipt clients.Receipt
}

func (l scopedReceiptLedger) Receipt(_ context.Context, receiptID string) (clients.Receipt, error) {
	result := l.receipt
	result.ReceiptID = receiptID
	return result, nil
}

func (l *monthlyLedger) RecordReceipt(_ context.Context, input clients.ReceiptInput, _ string) (clients.Receipt, error) {
	*l.events = append(*l.events, "ledger.receipt")
	l.receipts = append(l.receipts, input)
	if len(l.receiptErrors) > 0 {
		err := l.receiptErrors[0]
		l.receiptErrors = l.receiptErrors[1:]
		if err != nil {
			return clients.Receipt{}, err
		}
	}
	return clients.Receipt{ReceiptID: "receipt-monthly"}, nil
}

func newMonthlyBillingTest(t *testing.T, balances []int64) (*controlPlaneServer, *controlplane.Service, *monthlySub2API, *monthlyFabric, *monthlyLedger, *[]string) {
	t.Helper()
	events := &[]string{}
	sub2API := &monthlySub2API{events: events, balances: balances}
	fabric := &monthlyFabric{events: events}
	ledger := &monthlyLedger{events: events}
	app := newControlPlaneAppEmpty()
	if err := app.tables.SaveAccount(context.Background(), map[string]any{"id": "acct-monthly", "status": "active", "sub2apiUserId": int64(41)}); err != nil {
		t.Fatalf("save monthly account: %v", err)
	}
	return app, controlplane.NewService(ledger, fabric, sub2API), sub2API, fabric, ledger, events
}

func TestMonthlyPurchaseChargesExactProductsAndActivates(t *testing.T) {
	now := time.Date(2026, 7, 14, 8, 30, 0, 0, time.UTC)
	for _, tc := range []struct {
		name         string
		resourceType string
		packageID    string
		sizeGB       int
		charge       int64
		cnyCents     int64
	}{
		{name: "basic", resourceType: "compute", packageID: "basic", charge: 50_000_000, cnyCents: 35000},
		{name: "pro", resourceType: "compute", packageID: "pro", charge: 214_285_715, cnyCents: 150000},
		{name: "30GB storage", resourceType: "storage", packageID: "basic", sizeGB: 30, charge: 7_714_286, cnyCents: 5400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			initial := int64(1_000_000_000)
			app, service, sub2API, _, ledger, events := newMonthlyBillingTest(t, []int64{initial, initial, initial - tc.charge})
			result, err := app.purchaseMonthlyResource(context.Background(), service, monthlyPurchaseInput{
				ResourceType: tc.resourceType, ResourceID: tc.resourceType + "-monthly", BillingOperationID: "billing-" + tc.name,
				AccountID: "acct-monthly", WorkspaceID: "workspace-monthly", PackageID: tc.packageID, SizeGB: tc.sizeGB, Environment: "test", Now: now,
			})
			if err != nil {
				t.Fatalf("purchase %s: %v", tc.name, err)
			}
			if result["billingStatus"] != "active" || int64(numberField(result, "chargeUsdMicros", 0)) != tc.charge || int64(numberField(result, "monthlyPriceCnyCents", 0)) != tc.cnyCents || result["paidThrough"] != "2026-08-14T08:30:00Z" {
				t.Fatalf("monthly result = %#v", result)
			}
			if len(sub2API.charges) != 1 || sub2API.charges[0].Code != "opl:test:billing-"+tc.name+":charge:v1" || sub2API.charges[0].ChargeUSDMicros != tc.charge {
				t.Fatalf("charges = %#v", sub2API.charges)
			}
			if len(ledger.receipts) != 1 || int64(numberField(ledger.receipts[0].Cost, "chargeUsdMicros", 0)) != tc.charge {
				t.Fatalf("receipts = %#v", ledger.receipts)
			}
			wantPrepare := "fabric.compute.prepare"
			if tc.resourceType == "storage" {
				wantPrepare = "fabric.storage.prepare"
			}
			want := []string{"sub2api.balance", wantPrepare, "sub2api.balance", "sub2api.charge", "sub2api.balance", "ledger.receipt"}
			if strings.Join(*events, ",") != strings.Join(want, ",") {
				t.Fatalf("events = %#v, want %#v", *events, want)
			}
		})
	}
}

func TestMonthlyPurchaseRejectsInvalidInputBeforeExternalCalls(t *testing.T) {
	for name, input := range map[string]monthlyPurchaseInput{
		"unknown package": {ResourceType: "compute", ResourceID: "compute-invalid", BillingOperationID: "billing-invalid", AccountID: "acct-monthly", PackageID: "enterprise"},
		"partial storage": {ResourceType: "storage", ResourceID: "storage-invalid", BillingOperationID: "billing-invalid", AccountID: "acct-monthly", PackageID: "basic", SizeGB: 15},
	} {
		t.Run(name, func(t *testing.T) {
			app, service, _, _, _, events := newMonthlyBillingTest(t, nil)
			input.Now = time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC)
			if _, err := app.purchaseMonthlyResource(context.Background(), service, input); err == nil {
				t.Fatal("invalid purchase should fail")
			}
			if len(*events) != 0 {
				t.Fatalf("invalid purchase made external calls: %#v", *events)
			}
		})
	}
}

func TestMonthlyPurchaseFabricFailureDoesNotCharge(t *testing.T) {
	app, service, sub2API, fabric, _, events := newMonthlyBillingTest(t, []int64{100_000_000})
	fabric.createErr = errors.New("fabric unavailable")
	fabric.cleanupErr = errors.New("cleanup unavailable")
	result, err := app.purchaseMonthlyResource(context.Background(), service, monthlyPurchaseInput{ResourceType: "compute", ResourceID: "compute-fail", BillingOperationID: "billing-fail", AccountID: "acct-monthly", PackageID: "basic", Environment: "test", Now: time.Now().UTC()})
	if err == nil || result["billingStatus"] != "failed" || result["desiredStatus"] != "destroyed" || result["lastBillingError"] != "fabric_prepare_cleanup_failed" || len(sub2API.charges) != 0 {
		t.Fatalf("fabric failure result=%#v charges=%#v err=%v", result, sub2API.charges, err)
	}
	if strings.Join(*events, ",") != "sub2api.balance,fabric.compute.prepare,fabric.compute.cleanup" {
		t.Fatalf("events = %#v", *events)
	}
	fabric.cleanupErr = nil
	if err := app.reconcileMonthlyCompute(context.Background(), service, result, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	recovered, _ := app.getCompute("compute-fail")
	if recovered["status"] != "destroyed" || recovered["billingStatus"] != "stopped" || strings.Count(strings.Join(*events, ","), "fabric.compute.cleanup") != 2 {
		t.Fatalf("cleanup recovery row=%#v events=%#v", recovered, *events)
	}
}

func TestMonthlyPurchaseResumesProvisioningThroughFabricSync(t *testing.T) {
	events := &[]string{}
	sub2API := &monthlySub2API{events: events, balances: []int64{100_000_000, 100_000_000, 100_000_000, 50_000_000}}
	fabric := &provisioningMonthlyFabric{monthlyFabric: monthlyFabric{events: events}}
	ledger := &monthlyLedger{events: events}
	app := newControlPlaneAppEmpty()
	if err := app.tables.SaveAccount(context.Background(), map[string]any{"id": "acct-monthly", "status": "active", "sub2apiUserId": int64(41)}); err != nil {
		t.Fatal(err)
	}
	service := controlplane.NewService(ledger, fabric, sub2API)
	input := monthlyPurchaseInput{ResourceType: "compute", ResourceID: "compute-provisioning", BillingOperationID: "billing-provisioning", AccountID: "acct-monthly", PackageID: "basic", Environment: "test", Now: time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC)}

	first, err := app.purchaseMonthlyResource(context.Background(), service, input)
	if err != nil || first["billingStatus"] != "preparing" || len(sub2API.charges) != 0 {
		t.Fatalf("first purchase=%#v charges=%#v err=%v", first, sub2API.charges, err)
	}
	second, err := app.purchaseMonthlyResource(context.Background(), service, input)
	if err != nil || second["billingStatus"] != "active" || len(fabric.computeIDs) != 1 || fabric.syncCalls != 1 || len(sub2API.charges) != 1 {
		t.Fatalf("recovered purchase=%#v creates=%#v syncs=%d charges=%#v err=%v", second, fabric.computeIDs, fabric.syncCalls, sub2API.charges, err)
	}
}

func TestMonthlyPurchaseSecondBalanceFailureCleansPreparedResource(t *testing.T) {
	app, service, sub2API, _, _, events := newMonthlyBillingTest(t, []int64{100_000_000, 40_000_000})
	result, err := app.purchaseMonthlyResource(context.Background(), service, monthlyPurchaseInput{ResourceType: "compute", ResourceID: "compute-low", BillingOperationID: "billing-low", AccountID: "acct-monthly", PackageID: "basic", Environment: "test", Now: time.Now().UTC()})
	if !errors.Is(err, errMonthlyInsufficientBalance) || result["billingStatus"] != "failed" || result["status"] != "destroyed" || result["desiredStatus"] != "destroyed" || len(sub2API.charges) != 0 {
		t.Fatalf("insufficient result=%#v charges=%#v err=%v", result, sub2API.charges, err)
	}
	if strings.Join(*events, ",") != "sub2api.balance,fabric.compute.prepare,sub2api.balance,fabric.compute.cleanup" {
		t.Fatalf("events = %#v", *events)
	}
}

func TestMonthlyPurchaseRecoversLostChargeResponseWithSameCode(t *testing.T) {
	app, service, sub2API, fabric, _, _ := newMonthlyBillingTest(t, []int64{100_000_000, 100_000_000, 50_000_000})
	sub2API.chargeErrors = []error{clients.ErrSub2APIChargeUnknown, nil}
	input := monthlyPurchaseInput{ResourceType: "compute", ResourceID: "compute-recover", BillingOperationID: "billing-recover", AccountID: "acct-monthly", PackageID: "basic", Environment: "test", Now: time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC)}
	first, err := app.purchaseMonthlyResource(context.Background(), service, input)
	if !errors.Is(err, clients.ErrSub2APIChargeUnknown) || first["billingStatus"] != "manual_review" {
		t.Fatalf("first result=%#v err=%v", first, err)
	}
	second, err := app.purchaseMonthlyResource(context.Background(), service, input)
	if err != nil || second["billingStatus"] != "active" {
		t.Fatalf("recovered result=%#v err=%v", second, err)
	}
	if len(sub2API.charges) != 2 || sub2API.charges[0].Code != sub2API.charges[1].Code || len(fabric.computeIDs) != 1 {
		t.Fatalf("charges=%#v computes=%#v", sub2API.charges, fabric.computeIDs)
	}
}

func TestMonthlyPurchaseZeroPostChargeBalanceRequiresReview(t *testing.T) {
	app, service, _, _, ledger, _ := newMonthlyBillingTest(t, []int64{100_000_000, 100_000_000, 0})
	result, err := app.purchaseMonthlyResource(context.Background(), service, monthlyPurchaseInput{ResourceType: "compute", ResourceID: "compute-zero", BillingOperationID: "billing-zero", AccountID: "acct-monthly", PackageID: "basic", Environment: "test", Now: time.Now().UTC()})
	if !errors.Is(err, errMonthlyChargeNeedsReview) || result["billingStatus"] != "manual_review" || len(ledger.receipts) != 0 {
		t.Fatalf("zero balance result=%#v receipts=%#v err=%v", result, ledger.receipts, err)
	}
}

func TestMonthlyPurchaseRetriesReceiptWithoutChargingAgain(t *testing.T) {
	app, service, sub2API, _, ledger, _ := newMonthlyBillingTest(t, []int64{100_000_000, 100_000_000, 50_000_000})
	ledger.receiptErrors = []error{errors.New("ledger unavailable"), nil}
	input := monthlyPurchaseInput{ResourceType: "compute", ResourceID: "compute-receipt", BillingOperationID: "billing-receipt", AccountID: "acct-monthly", PackageID: "basic", Environment: "test", Now: time.Now().UTC()}
	first, err := app.purchaseMonthlyResource(context.Background(), service, input)
	if err != nil || first["billingStatus"] != "active" || first["lastReceiptId"] != nil {
		t.Fatalf("receipt outage result=%#v err=%v", first, err)
	}
	second, err := app.purchaseMonthlyResource(context.Background(), service, input)
	if err != nil || second["lastReceiptId"] != "receipt-monthly" || len(sub2API.charges) != 1 || len(ledger.receipts) != 2 {
		t.Fatalf("receipt retry result=%#v charges=%d receipts=%d err=%v", second, len(sub2API.charges), len(ledger.receipts), err)
	}
}

func TestMonthlyEntitlementRejectsInactiveOrExpiredResources(t *testing.T) {
	now := time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC)
	if !monthlyEntitlementActive(map[string]any{"billingStatus": "active", "paidThrough": now.Add(time.Hour).Format(time.RFC3339)}, now) {
		t.Fatal("active unexpired entitlement should be usable")
	}
	for _, row := range []map[string]any{
		{"billingStatus": "preparing", "paidThrough": now.Add(time.Hour).Format(time.RFC3339)},
		{"billingStatus": "active", "paidThrough": now.Format(time.RFC3339)},
		{"billingStatus": "manual_review", "paidThrough": now.Add(time.Hour).Format(time.RFC3339)},
	} {
		if monthlyEntitlementActive(row, now) {
			t.Fatalf("entitlement should be rejected: %#v", row)
		}
	}
}

func TestMonthlyPurchaseRouteUsesSub2APIAndPersistsReceipt(t *testing.T) {
	events := &[]string{}
	sub2API := &monthlySub2API{events: events, balances: []int64{100_000_000, 100_000_000, 50_000_000}}
	fabric := &monthlyFabric{events: events}
	ledger := &monthlyLedger{events: events}
	store := newMemoryTableStore()
	if err := store.SaveAccount(context.Background(), map[string]any{"id": "acct-alpha", "status": "active", "sub2apiUserId": int64(41)}); err != nil {
		t.Fatal(err)
	}
	server, err := NewPersistentServer(controlplane.NewService(ledger, fabric, sub2API), store)
	if err != nil {
		t.Fatalf("new monthly server: %v", err)
	}
	session := tenantAdminSessionForTest(t, server)
	rec := requestWithSession(t, server, session, http.MethodPost, "/api/compute-allocations", `{"packageId":"basic","name":"Monthly Compute"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("purchase status = %d: %s", rec.Code, rec.Body.String())
	}
	var result map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode purchase: %v", err)
	}
	if result["billingStatus"] != "active" || len(sub2API.charges) != 1 || len(ledger.receipts) != 1 {
		t.Fatalf("purchase result=%#v charges=%#v receipts=%#v", result, sub2API.charges, ledger.receipts)
	}
}

func TestPaidResourceRoutesRejectCallerSelectedNewResourceIDsBeforeExternalCalls(t *testing.T) {
	events := &[]string{}
	sub2API := &monthlySub2API{events: events, balances: []int64{100_000_000, 100_000_000, 50_000_000, 100_000_000, 100_000_000, 97_428_571}}
	store := newMemoryTableStore()
	seedTenantMember(t, store, "acct-alpha", "org-alpha", "usr-alpha", "alpha@example.com")
	server, err := NewPersistentServer(controlplane.NewService(&monthlyLedger{events: events}, &monthlyFabric{events: events}, sub2API), store)
	if err != nil {
		t.Fatal(err)
	}
	session := loginForTest(t, server, "alpha@example.com", "CorrectHorseBatteryStaple!")

	for _, tc := range []struct {
		path string
		body string
	}{
		{path: "/api/compute-allocations", body: `{"id":"caller-compute","packageId":"basic"}`},
		{path: "/api/storage-volumes", body: `{"id":"caller-storage","sizeGb":10}`},
		{path: "/api/storage-volumes", body: `{"sizeGb":10.5}`},
		{path: "/api/compute-allocations", body: `{`},
		{path: "/api/storage-volumes", body: `{`},
	} {
		rec := requestWithSession(t, server, session, http.MethodPost, tc.path, tc.body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("caller-selected id on %s status=%d body=%s", tc.path, rec.Code, rec.Body.String())
		}
	}
	if len(*events) != 0 {
		t.Fatalf("caller-selected ids reached external services: %#v", *events)
	}
}

func TestStorageRouteAllowsOnlyOwnedRetainedVolumeReactivation(t *testing.T) {
	events := &[]string{}
	sub2API := &monthlySub2API{events: events, balances: []int64{100_000_000, 100_000_000, 92_285_714}}
	fabric := &provisioningMonthlyFabric{monthlyFabric: monthlyFabric{events: events}}
	store := newMemoryTableStore()
	seedTenantMember(t, store, "acct-alpha", "org-alpha", "usr-alpha", "alpha@example.com")
	retained := monthlyActiveResource("storage", "storage-retained", time.Now().UTC().Add(-time.Hour))
	retained["accountId"], retained["ownerUserId"], retained["billingStatus"] = "acct-alpha", "usr-alpha", "retained"
	retained["sizeGb"], retained["monthlyPriceCnyCents"], retained["chargeUsdMicros"] = 30, int64(5400), int64(7_714_286)
	mustStore(t, store.SaveStorage(context.Background(), retained))
	server, err := NewPersistentServer(controlplane.NewService(&monthlyLedger{events: events}, fabric, sub2API), store)
	if err != nil {
		t.Fatal(err)
	}
	session := loginForTest(t, server, "alpha@example.com", "CorrectHorseBatteryStaple!")
	rec := requestWithSession(t, server, session, http.MethodPost, "/api/storage-volumes", `{"id":"storage-retained","sizeGb":10}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("retained reactivation status=%d body=%s", rec.Code, rec.Body.String())
	}
	var result map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result["id"] != "storage-retained" || result["billingStatus"] != "active" || int64(numberField(result, "sizeGb", 0)) != 30 || int64(numberField(result, "chargeUsdMicros", 0)) != 7_714_286 || len(fabric.storageIDs) != 0 || fabric.syncCalls != 1 {
		t.Fatalf("retained reactivation result=%#v creates=%#v syncs=%d", result, fabric.storageIDs, fabric.syncCalls)
	}
}

func TestCreateUserRequiresAndPersistsSub2APIUserMapping(t *testing.T) {
	app := newControlPlaneAppEmpty()
	input := map[string]any{
		"email": "mapped@example.com", "accountId": "acct-mapped", "role": "owner",
		"password": "CorrectHorseBatteryStaple!",
	}
	if _, err := app.createUser(input); !errors.Is(err, errMonthlyAccountUnmapped) {
		t.Fatalf("missing Sub2API mapping error = %v, want %v", err, errMonthlyAccountUnmapped)
	}

	input["sub2apiUserId"] = float64(41)
	if _, err := app.createUser(input); err != nil {
		t.Fatalf("create mapped user: %v", err)
	}
	accounts, err := app.tables.ListAccounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	account := findRecord(accounts, "acct-mapped")
	if account == nil || int64(numberField(account, "sub2apiUserId", 0)) != 41 {
		t.Fatalf("persisted account mapping = %#v", account)
	}
}

func TestSub2APIUserMappingRejectsNumbersJSONCannotRepresentExactly(t *testing.T) {
	for name, input := range map[string]any{
		"zero":             float64(0),
		"negative":         float64(-1),
		"fractional":       float64(1.5),
		"above safe range": float64(9_007_199_254_740_992),
	} {
		t.Run(name, func(t *testing.T) {
			if _, ok := positiveIntegerField(map[string]any{"sub2apiUserId": input}, "sub2apiUserId"); ok {
				t.Fatalf("accepted inexact Sub2API user id %v", input)
			}
		})
	}
	if value, ok := positiveIntegerField(map[string]any{"sub2apiUserId": float64(9_007_199_254_740_991)}, "sub2apiUserId"); !ok || value != 9_007_199_254_740_991 {
		t.Fatalf("largest safe Sub2API user id = %d, ok=%v", value, ok)
	}
}

func TestStateRouteUsesOnlyLiveSub2APIBalance(t *testing.T) {
	events := &[]string{}
	sub2API := &monthlySub2API{events: events, balances: []int64{123_456_789}}
	store := newMemoryTableStore()
	if err := store.SaveAccount(context.Background(), map[string]any{"id": "acct-alpha", "status": "active", "sub2apiUserId": int64(41)}); err != nil {
		t.Fatal(err)
	}
	server, err := NewPersistentServer(controlplane.NewService(&monthlyLedger{events: events}, &monthlyFabric{events: events}, sub2API), store)
	if err != nil {
		t.Fatal(err)
	}
	response := requestWithSession(t, server, tenantAdminSessionForTest(t, server), http.MethodGet, "/api/state", "")
	if response.Code != http.StatusOK {
		t.Fatalf("state status = %d: %s", response.Code, response.Body.String())
	}
	var state map[string]any
	if err := json.NewDecoder(response.Body).Decode(&state); err != nil {
		t.Fatal(err)
	}
	if _, exists := state["wallet"]; exists {
		t.Fatalf("state retains wallet: %#v", state["wallet"])
	}
	balance, _ := state["balance"].(map[string]any)
	if balance["source"] != "sub2api" || balance["currency"] != "USD" || int64(numberField(balance, "userId", 0)) != 41 || int64(numberField(balance, "usdMicros", 0)) != 123_456_789 {
		t.Fatalf("state balance = %#v", balance)
	}
}

func TestPaidResourceIdempotencyKeysAreScopedToTheSessionAccount(t *testing.T) {
	events := &[]string{}
	sub2API := &monthlySub2API{events: events, balances: []int64{100_000_000, 100_000_000, 50_000_000, 100_000_000, 100_000_000, 50_000_000}}
	fabric := &monthlyFabric{events: events}
	store := newMemoryTableStore()
	seedTenantMember(t, store, "acct-alpha", "org-alpha", "usr-alpha", "alpha@example.com")
	seedTenantMember(t, store, "acct-beta", "org-beta", "usr-beta", "beta@example.com")
	mustStore(t, store.SaveAccount(context.Background(), map[string]any{"id": "acct-beta", "status": "active", "sub2apiUserId": int64(42)}))
	server, err := NewPersistentServer(controlplane.NewService(&monthlyLedger{events: events}, fabric, sub2API), store)
	if err != nil {
		t.Fatal(err)
	}

	alpha := requestWithSession(t, server, loginForTest(t, server, "alpha@example.com", "CorrectHorseBatteryStaple!"), http.MethodPost, "/api/compute-allocations", `{"packageId":"basic"}`)
	beta := requestWithSession(t, server, loginForTest(t, server, "beta@example.com", "CorrectHorseBatteryStaple!"), http.MethodPost, "/api/compute-allocations", `{"packageId":"basic"}`)
	if alpha.Code != http.StatusAccepted || beta.Code != http.StatusAccepted {
		t.Fatalf("same-key purchases: alpha=%d %s beta=%d %s", alpha.Code, alpha.Body.String(), beta.Code, beta.Body.String())
	}
	var alphaResource, betaResource map[string]any
	if err := json.NewDecoder(alpha.Body).Decode(&alphaResource); err != nil {
		t.Fatal(err)
	}
	if err := json.NewDecoder(beta.Body).Decode(&betaResource); err != nil {
		t.Fatal(err)
	}
	if alphaResource["id"] == betaResource["id"] || len(sub2API.charges) != 2 || sub2API.charges[0].UserID != 41 || sub2API.charges[1].UserID != 42 {
		t.Fatalf("alpha=%#v beta=%#v charges=%#v", alphaResource, betaResource, sub2API.charges)
	}
}

func TestMonthlyReadinessRoutesResumePersistedPurchase(t *testing.T) {
	for _, tc := range []struct {
		name         string
		createPath   string
		createBody   string
		resourceType string
		charge       int64
	}{
		{name: "compute get", createPath: "/api/compute-allocations", createBody: `{"packageId":"basic"}`, resourceType: "compute", charge: 50_000_000},
		{name: "storage sync", createPath: "/api/storage-volumes", createBody: `{"sizeGb":10}`, resourceType: "storage", charge: 2_571_429},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := &[]string{}
			initial := int64(100_000_000)
			sub2API := &monthlySub2API{events: events, balances: []int64{initial, initial, initial, initial - tc.charge}}
			fabric := &provisioningMonthlyFabric{monthlyFabric: monthlyFabric{events: events}}
			ledger := &monthlyLedger{events: events}
			store := newMemoryTableStore()
			if err := store.SaveAccount(context.Background(), map[string]any{"id": "acct-alpha", "status": "active", "sub2apiUserId": int64(41)}); err != nil {
				t.Fatal(err)
			}
			server, err := NewPersistentServer(controlplane.NewService(ledger, fabric, sub2API), store)
			if err != nil {
				t.Fatal(err)
			}
			session := tenantAdminSessionForTest(t, server)
			created := requestWithSession(t, server, session, http.MethodPost, tc.createPath, tc.createBody)
			if created.Code != http.StatusAccepted {
				t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
			}
			var pending map[string]any
			if err := json.NewDecoder(created.Body).Decode(&pending); err != nil {
				t.Fatal(err)
			}
			id := stringValue(pending["id"])
			method, path := http.MethodGet, "/api/compute-allocations/"+id
			if tc.resourceType == "storage" {
				method, path = http.MethodPost, "/api/storage-volumes/"+id+"/sync"
			}
			ready := requestWithSession(t, server, session, method, path, `{}`)
			if ready.Code != http.StatusOK {
				t.Fatalf("readiness status=%d body=%s", ready.Code, ready.Body.String())
			}
			var active map[string]any
			if err := json.NewDecoder(ready.Body).Decode(&active); err != nil {
				t.Fatal(err)
			}
			creates := fabric.computeIDs
			if tc.resourceType == "storage" {
				creates = fabric.storageIDs
			}
			if active["billingStatus"] != "active" || len(creates) != 1 || fabric.syncCalls != 1 || len(sub2API.charges) != 1 {
				t.Fatalf("active=%#v creates=%#v syncs=%d charges=%#v", active, creates, fabric.syncCalls, sub2API.charges)
			}
		})
	}
}

func TestMonthlyRoutesRejectInactiveEntitlementsBeforeFabric(t *testing.T) {
	events := &[]string{}
	sub2API := &monthlySub2API{events: events}
	fabricCalls := []string{}
	fabric := &monthlyFabric{events: events, fakeFabricClient: fakeFabricClient{calls: &fabricCalls}}
	ledger := &monthlyLedger{events: events}
	store := newMemoryTableStore()
	now := time.Now().UTC()
	if err := store.SaveCompute(context.Background(), map[string]any{"id": "compute-inactive", "accountId": "acct-alpha", "workspaceId": "workspace-monthly", "status": "running", "billingStatus": "preparing", "paidThrough": now.Add(time.Hour).Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveStorage(context.Background(), map[string]any{"id": "storage-active", "accountId": "acct-alpha", "workspaceId": "workspace-monthly", "status": "available", "billingStatus": "active", "paidThrough": now.Add(time.Hour).Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	server, err := NewPersistentServer(controlplane.NewService(ledger, fabric, sub2API), store)
	if err != nil {
		t.Fatalf("new monthly server: %v", err)
	}
	session := tenantAdminSessionForTest(t, server)
	attachment := requestWithSession(t, server, session, http.MethodPost, "/api/storage-attachments", `{"workspaceId":"workspace-monthly","computeAllocationId":"compute-inactive","storageId":"storage-active"}`)
	if attachment.Code != http.StatusConflict || !strings.Contains(attachment.Body.String(), "monthly_entitlement_inactive") {
		t.Fatalf("attachment status = %d: %s", attachment.Code, attachment.Body.String())
	}
	if len(fabricCalls) != 0 {
		t.Fatalf("inactive attachment reached Fabric: %#v", fabricCalls)
	}

	if err := store.SaveCompute(context.Background(), map[string]any{"id": "compute-inactive", "accountId": "acct-alpha", "workspaceId": "workspace-monthly", "status": "running", "billingStatus": "active", "paidThrough": now.Add(-time.Minute).Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAttachment(context.Background(), map[string]any{"id": "attachment-expired", "accountId": "acct-alpha", "workspaceId": "workspace-monthly", "computeAllocationId": "compute-inactive", "storageId": "storage-active", "status": "attached"}); err != nil {
		t.Fatal(err)
	}
	workspaceReq := httptest.NewRequest(http.MethodPost, "/api/workspaces", bytes.NewBufferString(`{"attachmentId":"attachment-expired"}`))
	workspaceReq.Header.Set("Content-Type", "application/json")
	workspaceReq.Header.Set("Idempotency-Key", "workspace-expired")
	addAuth(workspaceReq, session)
	workspaceRec := httptest.NewRecorder()
	server.ServeHTTP(workspaceRec, workspaceReq)
	if workspaceRec.Code != http.StatusConflict || !strings.Contains(workspaceRec.Body.String(), "monthly_entitlement_inactive") {
		t.Fatalf("workspace status = %d: %s", workspaceRec.Code, workspaceRec.Body.String())
	}
	if len(fabricCalls) != 0 {
		t.Fatalf("expired workspace reached Fabric: %#v", fabricCalls)
	}
}

func TestBillingReceiptRouteIsScopedToTheSessionAccount(t *testing.T) {
	for _, tc := range []struct {
		name       string
		accountID  string
		wantStatus int
	}{
		{name: "owned receipt", accountID: "acct-alpha", wantStatus: http.StatusOK},
		{name: "other account receipt", accountID: "acct-beta", wantStatus: http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ledger := scopedReceiptLedger{receipt: clients.Receipt{ReceiptInput: clients.ReceiptInput{Type: "billing.resource_purchased.v1", Status: "completed", AccountID: tc.accountID, WorkspaceID: "workspace-monthly"}}}
			server := NewServer(newTestService(ledger, &fakeFabricClient{}))
			response := requestWithSession(t, server, tenantAdminSessionForTest(t, server), http.MethodGet, "/api/billing/receipts/receipt-monthly", "")
			if response.Code != tc.wantStatus {
				t.Fatalf("receipt status = %d, want %d: %s", response.Code, tc.wantStatus, response.Body.String())
			}
			if tc.wantStatus == http.StatusOK && !strings.Contains(response.Body.String(), `"accountId":"acct-alpha"`) {
				t.Fatalf("owned receipt response = %s", response.Body.String())
			}
		})
	}
}
