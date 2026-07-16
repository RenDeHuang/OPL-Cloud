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
	balanceErr   error
	chargeErrors []error
	charges      []clients.Sub2APIChargeInput
	refundErrors []error
	refunds      []clients.Sub2APIRefundInput
}

func (s *monthlySub2API) Version(context.Context) (string, error) { return "0.1.155", nil }

func (s *monthlySub2API) Balance(_ context.Context, userID int64) (clients.Sub2APIBalance, error) {
	*s.events = append(*s.events, "sub2api.balance")
	if s.balanceErr != nil {
		return clients.Sub2APIBalance{}, s.balanceErr
	}
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

func (s *monthlySub2API) Refund(_ context.Context, input clients.Sub2APIRefundInput) (clients.Sub2APIRefund, error) {
	*s.events = append(*s.events, "sub2api.refund")
	s.refunds = append(s.refunds, input)
	if len(s.refundErrors) > 0 {
		err := s.refundErrors[0]
		s.refundErrors = s.refundErrors[1:]
		if err != nil {
			return clients.Sub2APIRefund{}, err
		}
	}
	return clients.Sub2APIRefund{Code: input.Code, UserID: input.UserID, RefundUSDMicros: input.RefundUSDMicros, Status: "used"}, nil
}

type monthlyFabric struct {
	fakeFabricClient
	events           *[]string
	createErr        error
	cleanupErr       error
	cleanupStatus    string
	syncErr          error
	preflightResult  *clients.MonthlyPreflight
	preflightErr     error
	preflightInputs  []clients.MonthlyPreflightInput
	mutateCompute    func(*clients.ComputeAllocation)
	mutateStorage    func(*clients.StorageVolume)
	computeIDs       []string
	storageIDs       []string
	storageInputs    []clients.StorageVolumeInput
	computeSync      clients.ComputeAllocation
	storageSync      clients.StorageVolume
	computeRenew     clients.ComputeAllocation
	storageRenew     clients.StorageVolume
	computeRenewErr  error
	storageRenewErr  error
	computeRenewKeys []string
	storageRenewKeys []string
}

type provisioningMonthlyFabric struct {
	monthlyFabric
	syncCalls    int
	computeInput clients.ComputeAllocationInput
	storageInput clients.StorageVolumeInput
}

func (f *monthlyFabric) MonthlyPreflight(_ context.Context, input clients.MonthlyPreflightInput) (clients.MonthlyPreflight, error) {
	*f.events = append(*f.events, "fabric.monthly.preflight")
	f.preflightInputs = append(f.preflightInputs, input)
	if f.preflightResult != nil {
		return *f.preflightResult, f.preflightErr
	}
	requestIDs := map[string]string{"quota": "quota-request", "price": "price-request"}
	if input.ResourceType == "compute" {
		requestIDs = map[string]string{"nodePool": "node-pool-request", "subnets": "subnets-request", "availability": "availability-request"}
	}
	return clients.MonthlyPreflight{
		ResourceType: input.ResourceType, PackageID: input.PackageID, SizeGB: input.SizeGB, Zone: input.Zone,
		Available: true, ChargeType: "PREPAID", PeriodMonths: 1, RenewFlag: "NOTIFY_AND_MANUAL_RENEW",
		ProviderPriceCNY: 12.34, ProviderRequestIDs: requestIDs,
	}, f.preflightErr
}

func (f *provisioningMonthlyFabric) CreateComputeAllocation(_ context.Context, input clients.ComputeAllocationInput, _ string) (clients.ComputeAllocation, error) {
	*f.events = append(*f.events, "fabric.compute.prepare")
	f.computeIDs = append(f.computeIDs, input.ID)
	f.computeInput = input
	return clients.ComputeAllocation{ID: input.ID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, PackageID: input.PackageID, Status: "provisioning", Provider: "tencent-tke", ProviderRequestID: "req-" + input.ID}, nil
}

func (f *provisioningMonthlyFabric) SyncComputeAllocation(_ context.Context, id string) (clients.ComputeAllocation, error) {
	*f.events = append(*f.events, "fabric.compute.sync")
	f.syncCalls++
	return clients.ComputeAllocation{ID: id, AccountID: f.computeInput.AccountID, WorkspaceID: f.computeInput.WorkspaceID, PackageID: f.computeInput.PackageID, Status: "running", Provider: "tencent-tke", ProviderResourceID: "ins-" + id, ProviderRequestID: "req-" + id, InstanceID: "ins-" + id, InstanceType: "S5.MEDIUM4", Zone: "ap-shanghai-2", ChargeType: "PREPAID", RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: "2099-01-01T00:00:00Z", ProviderData: map[string]string{"zone": "ap-shanghai-2", "instanceType": "S5.MEDIUM4"}}, nil
}

func (f *provisioningMonthlyFabric) CreateStorageVolume(_ context.Context, input clients.StorageVolumeInput, _ string) (clients.StorageVolume, error) {
	*f.events = append(*f.events, "fabric.storage.prepare")
	f.storageIDs = append(f.storageIDs, input.ID)
	f.storageInput = input
	return clients.StorageVolume{ID: input.ID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, SizeGB: input.SizeGB, Status: "provisioning", Provider: "tencent-tke", ProviderRequestID: "req-" + input.ID}, nil
}

func (f *provisioningMonthlyFabric) SyncStorageVolume(_ context.Context, id string) (clients.StorageVolume, error) {
	*f.events = append(*f.events, "fabric.storage.sync")
	f.syncCalls++
	return clients.StorageVolume{ID: id, AccountID: f.storageInput.AccountID, WorkspaceID: f.storageInput.WorkspaceID, Status: "available", Provider: "tencent-tke", ProviderResourceID: "disk-" + id, ProviderRequestID: "req-" + id, SizeGB: f.storageInput.SizeGB, CBSStatus: "UNATTACHED", DiskType: "CLOUD_PREMIUM", RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: "2099-01-01T00:00:00Z", Zone: f.storageInput.Zone, ProviderData: map[string]string{"chargeType": "PREPAID"}}, nil
}

func (f *monthlyFabric) CreateComputeAllocation(_ context.Context, input clients.ComputeAllocationInput, _ string) (clients.ComputeAllocation, error) {
	*f.events = append(*f.events, "fabric.compute.prepare")
	f.computeIDs = append(f.computeIDs, input.ID)
	if f.createErr != nil {
		return clients.ComputeAllocation{ID: input.ID}, f.createErr
	}
	instanceType := "S5.MEDIUM4"
	if input.PackageID == "pro" {
		instanceType = "SA5.2XLARGE16"
	}
	result := clients.ComputeAllocation{ID: input.ID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, PackageID: input.PackageID, Status: "running", Provider: "tencent-tke", ProviderResourceID: "ins-" + input.ID, ProviderRequestID: "req-" + input.ID, InstanceID: "ins-" + input.ID, InstanceType: instanceType, Zone: "ap-shanghai-2", ChargeType: "PREPAID", RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: "2099-01-01T00:00:00Z", ProviderData: map[string]string{"zone": "ap-shanghai-2", "instanceType": instanceType}}
	if f.mutateCompute != nil {
		f.mutateCompute(&result)
	}
	return result, nil
}

func (f *monthlyFabric) CreateStorageVolume(_ context.Context, input clients.StorageVolumeInput, _ string) (clients.StorageVolume, error) {
	*f.events = append(*f.events, "fabric.storage.prepare")
	f.storageIDs = append(f.storageIDs, input.ID)
	f.storageInputs = append(f.storageInputs, input)
	if f.createErr != nil {
		return clients.StorageVolume{ID: input.ID}, f.createErr
	}
	result := clients.StorageVolume{ID: input.ID, AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, SizeGB: input.SizeGB, Status: "available", Provider: "tencent-tke", ProviderResourceID: "disk-" + input.ID, ProviderRequestID: "req-" + input.ID, CBSStatus: "UNATTACHED", DiskType: "CLOUD_PREMIUM", RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: "2099-01-01T00:00:00Z", Zone: input.Zone, ProviderData: map[string]string{"chargeType": "PREPAID"}}
	if f.mutateStorage != nil {
		f.mutateStorage(&result)
	}
	return result, nil
}

func (f *monthlyFabric) SyncComputeAllocation(_ context.Context, id string) (clients.ComputeAllocation, error) {
	*f.events = append(*f.events, "fabric.compute.sync")
	result := f.computeSync
	if result.ID == "" {
		result.ID = id
	}
	return result, f.syncErr
}

func (f *monthlyFabric) SyncStorageVolume(_ context.Context, id string) (clients.StorageVolume, error) {
	*f.events = append(*f.events, "fabric.storage.sync")
	result := f.storageSync
	if result.ID == "" {
		result.ID = id
	}
	return result, f.syncErr
}

func (f *monthlyFabric) RenewComputeAllocation(_ context.Context, id, key string) (clients.ComputeAllocation, error) {
	*f.events = append(*f.events, "fabric.compute.renew")
	f.computeRenewKeys = append(f.computeRenewKeys, key)
	result := f.computeRenew
	if result.ID == "" {
		result = clients.ComputeAllocation{ID: id, AccountID: "acct-monthly", WorkspaceID: "workspace-monthly", PackageID: "basic", Status: "running", ProviderResourceID: "ins-" + id, ProviderRequestID: "renew-" + id, InstanceID: "ins-" + id, CVMInstanceID: "ins-" + id, InstanceType: "S5.MEDIUM4", Zone: "ap-shanghai-2", ChargeType: "PREPAID", RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: "2026-09-30T09:30:00Z", ProviderData: map[string]string{"chargeType": "PREPAID", "renewalResult": "renewed", "zone": "ap-shanghai-2", "instanceType": "S5.MEDIUM4"}}
	}
	return result, f.computeRenewErr
}

func (f *monthlyFabric) RenewStorageVolume(_ context.Context, id, key string) (clients.StorageVolume, error) {
	*f.events = append(*f.events, "fabric.storage.renew")
	f.storageRenewKeys = append(f.storageRenewKeys, key)
	result := f.storageRenew
	if result.ID == "" {
		result = clients.StorageVolume{ID: id, AccountID: "acct-monthly", WorkspaceID: "workspace-monthly", Status: "available", ProviderResourceID: "disk-" + id, ProviderRequestID: "renew-" + id, CBSStatus: "UNATTACHED", SizeGB: 10, Zone: "ap-shanghai-2", RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: "2026-09-30T09:30:00Z", ProviderData: map[string]string{"chargeType": "PREPAID", "renewalResult": "renewed", "zone": "ap-shanghai-2"}}
	}
	return result, f.storageRenewErr
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
	return clients.StorageVolume{ID: id, Status: firstNonEmpty(f.cleanupStatus, "destroyed")}, nil
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

type failingMonthlySaveStore struct {
	*memoryTableStore
	err error
}

func (s *failingMonthlySaveStore) SaveCompute(context.Context, map[string]any) error {
	return s.err
}

func (s *failingMonthlySaveStore) SaveStorage(context.Context, map[string]any) error {
	return s.err
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
	t.Setenv("OPL_TENCENT_ZONE", "ap-shanghai-2")
	t.Setenv("OPL_BASIC_COMPUTE_INSTANCE_TYPE", "S5.MEDIUM4")
	t.Setenv("OPL_PRO_COMPUTE_INSTANCE_TYPE", "SA5.2XLARGE16")
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

func TestMonthlyRedeemCodeFitsSub2API(t *testing.T) {
	code := monthlyRedeemCode("production", "billing-7913c4a1dc0c690180")
	if code != "opl:7fbd9eece4373eb56bee0f788969" {
		t.Fatalf("redeem code = %q", code)
	}
	if len(code) > 32 {
		t.Fatalf("redeem code length = %d, want <= 32", len(code))
	}
	if code != monthlyRedeemCode("production", "billing-7913c4a1dc0c690180") {
		t.Fatal("redeem code is not stable")
	}
	if code == monthlyRedeemCode("staging", "billing-7913c4a1dc0c690180") || code == monthlyRedeemCode("production", "billing-different") {
		t.Fatal("redeem code must include environment and operation identity")
	}
}

func TestMonthlyPurchaseChargesExactProductsAndActivates(t *testing.T) {
	now := time.Date(2026, 7, 14, 8, 30, 0, 0, time.UTC)
	for _, tc := range []struct {
		name         string
		resourceType string
		packageID    string
		sizeGB       int
		cbsStatus    string
		charge       int64
		cnyCents     int64
	}{
		{name: "basic", resourceType: "compute", packageID: "basic", charge: 50_000_000, cnyCents: 35000},
		{name: "pro", resourceType: "compute", packageID: "pro", charge: 214_285_715, cnyCents: 150000},
		{name: "10GB attached storage", resourceType: "storage", packageID: "basic", sizeGB: 10, cbsStatus: "ATTACHED", charge: 2_571_429, cnyCents: 1800},
		{name: "100GB storage", resourceType: "storage", packageID: "pro", sizeGB: 100, charge: 25_714_286, cnyCents: 18000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			initial := int64(1_000_000_000)
			app, service, sub2API, fabric, ledger, events := newMonthlyBillingTest(t, []int64{initial, initial - tc.charge})
			if tc.cbsStatus != "" {
				fabric.mutateStorage = func(v *clients.StorageVolume) { v.CBSStatus = tc.cbsStatus }
			}
			result, err := app.purchaseMonthlyResource(context.Background(), service, monthlyPurchaseInput{
				ResourceType: tc.resourceType, ResourceID: tc.resourceType + "-monthly", BillingOperationID: "billing-" + tc.name,
				AccountID: "acct-monthly", WorkspaceID: "workspace-monthly", PackageID: tc.packageID, SizeGB: tc.sizeGB,
				ComputeID: "compute-placement", Zone: "ap-shanghai-2", Environment: "test", Now: now,
			})
			if err != nil {
				t.Fatalf("purchase %s: %v", tc.name, err)
			}
			if result["billingStatus"] != "active" || result["autoRenew"] != false || int64(numberField(result, "chargeUsdMicros", 0)) != tc.charge || int64(numberField(result, "monthlyPriceCnyCents", 0)) != tc.cnyCents || result["paidThrough"] != "2026-08-14T08:30:00Z" {
				t.Fatalf("monthly result = %#v", result)
			}
			if len(sub2API.charges) != 1 || sub2API.charges[0].Code != monthlyRedeemCode("test", "billing-"+tc.name) || sub2API.charges[0].ChargeUSDMicros != tc.charge {
				t.Fatalf("charges = %#v", sub2API.charges)
			}
			if len(ledger.receipts) != 1 || int64(numberField(ledger.receipts[0].Cost, "chargeUsdMicros", 0)) != tc.charge {
				t.Fatalf("receipts = %#v", ledger.receipts)
			}
			if len(fabric.preflightInputs) != 1 || fabric.preflightInputs[0].ResourceType != tc.resourceType || fabric.preflightInputs[0].PackageID != tc.packageID || fabric.preflightInputs[0].SizeGB != tc.sizeGB {
				t.Fatalf("preflight inputs = %#v", fabric.preflightInputs)
			}
			wantPrepare := "fabric.compute.prepare"
			if tc.resourceType == "storage" {
				wantPrepare = "fabric.storage.prepare"
				if len(fabric.storageInputs) != 1 || fabric.storageInputs[0].ComputeID != "compute-placement" || fabric.storageInputs[0].Zone != "ap-shanghai-2" {
					t.Fatalf("storage placement = %#v", fabric.storageInputs)
				}
			}
			want := []string{"fabric.monthly.preflight", "sub2api.balance", "sub2api.charge", "sub2api.balance", wantPrepare, "ledger.receipt"}
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

func TestMonthlyPurchasePreflightFailureHasNoFinancialOrProviderSideEffects(t *testing.T) {
	tests := []struct {
		name   string
		result *clients.MonthlyPreflight
		err    error
	}{
		{name: "upstream failure", err: errors.New("fabric preflight unavailable")},
		{name: "partial response", result: &clients.MonthlyPreflight{ResourceType: "compute", PackageID: "basic", Zone: "ap-shanghai-2", Available: true, ChargeType: "PREPAID", PeriodMonths: 1, RenewFlag: "NOTIFY_AND_MANUAL_RENEW", ProviderPriceCNY: 12.34}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			app, service, sub2API, fabric, ledger, events := newMonthlyBillingTest(t, []int64{100_000_000})
			fabric.preflightResult, fabric.preflightErr = tc.result, tc.err
			result, err := app.purchaseMonthlyResource(context.Background(), service, monthlyPurchaseInput{
				ResourceType: "compute", ResourceID: "compute-preflight", BillingOperationID: "billing-preflight-" + strings.ReplaceAll(tc.name, " ", "-"),
				AccountID: "acct-monthly", WorkspaceID: "workspace-monthly", PackageID: "basic", Environment: "test", Now: time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC),
			})
			if err == nil || result["billingStatus"] != "charge_pending" {
				t.Fatalf("result=%#v err=%v", result, err)
			}
			if strings.Join(*events, ",") != "fabric.monthly.preflight" || len(sub2API.charges) != 0 || len(sub2API.refunds) != 0 || len(fabric.computeIDs) != 0 || len(ledger.receipts) != 0 {
				t.Fatalf("events=%#v charges=%#v refunds=%#v creates=%#v receipts=%#v", *events, sub2API.charges, sub2API.refunds, fabric.computeIDs, ledger.receipts)
			}
		})
	}
}

func TestMonthlyPurchaseDebitFailureDoesNotMutateFabric(t *testing.T) {
	app, service, sub2API, fabric, ledger, events := newMonthlyBillingTest(t, []int64{100_000_000})
	sub2API.chargeErrors = []error{errors.New("debit failed")}
	result, err := app.purchaseMonthlyResource(context.Background(), service, monthlyPurchaseInput{ResourceType: "compute", ResourceID: "compute-debit-fail", BillingOperationID: "billing-debit-fail", AccountID: "acct-monthly", PackageID: "basic", Environment: "test", Now: time.Now().UTC()})
	if err == nil || result["billingStatus"] != "charge_pending" || len(fabric.computeIDs) != 0 || len(fabric.storageIDs) != 0 || len(ledger.receipts) != 0 {
		t.Fatalf("debit failure result=%#v fabric=%#v/%#v receipts=%#v err=%v", result, fabric.computeIDs, fabric.storageIDs, ledger.receipts, err)
	}
	if strings.Join(*events, ",") != "fabric.monthly.preflight,sub2api.balance,sub2api.charge" {
		t.Fatalf("debit failure events = %#v", *events)
	}
}

func TestMonthlyPurchaseConfirmedAbsenceRefundsOnce(t *testing.T) {
	app, service, sub2API, fabric, ledger, events := newMonthlyBillingTest(t, []int64{100_000_000, 50_000_000})
	fabric.createErr = errors.New("create response lost")
	fabric.computeSync = clients.ComputeAllocation{ID: "compute-absent", AccountID: "acct-monthly", WorkspaceID: "workspace-monthly", Status: "external_deleted"}
	input := monthlyPurchaseInput{ResourceType: "compute", ResourceID: "compute-absent", BillingOperationID: "billing-absent", AccountID: "acct-monthly", WorkspaceID: "workspace-monthly", PackageID: "basic", Environment: "test", Now: time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC)}

	result, err := app.purchaseMonthlyResource(context.Background(), service, input)
	if !errors.Is(err, errMonthlyPurchaseRefunded) || result["billingStatus"] != "refunded" || len(sub2API.charges) != 1 || len(sub2API.refunds) != 1 || len(ledger.receipts) != 1 || ledger.receipts[0].Type != "billing.resource_refunded.v1" {
		t.Fatalf("absence result=%#v charges=%#v refunds=%#v receipts=%#v err=%v", result, sub2API.charges, sub2API.refunds, ledger.receipts, err)
	}
	if sub2API.refunds[0].RefundUSDMicros != 50_000_000 || sub2API.refunds[0].Code != monthlyRefundCode("test", "billing-absent") || sub2API.refunds[0].RefundUSDMicros <= 0 {
		t.Fatalf("refund = %#v", sub2API.refunds[0])
	}
	before := len(*events)
	if _, err := app.purchaseMonthlyResource(context.Background(), service, input); !errors.Is(err, errMonthlyPurchaseRefunded) {
		t.Fatalf("refund replay err=%v", err)
	}
	if len(*events) != before || len(sub2API.charges) != 1 || len(sub2API.refunds) != 1 || len(fabric.computeIDs) != 1 || len(ledger.receipts) != 1 {
		t.Fatalf("refund replay duplicated work: events=%#v charges=%#v refunds=%#v creates=%#v", *events, sub2API.charges, sub2API.refunds, fabric.computeIDs)
	}
}

func TestMonthlyPurchaseUnknownProviderResultNeedsManualReviewWithoutRefund(t *testing.T) {
	app, service, sub2API, fabric, ledger, events := newMonthlyBillingTest(t, []int64{100_000_000, 50_000_000})
	fabric.createErr = errors.New("create response lost")
	fabric.syncErr = errors.New("provider readback unavailable")
	input := monthlyPurchaseInput{ResourceType: "compute", ResourceID: "compute-unknown", BillingOperationID: "billing-unknown", AccountID: "acct-monthly", PackageID: "basic", Environment: "test", Now: time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC)}

	result, err := app.purchaseMonthlyResource(context.Background(), service, input)
	if !errors.Is(err, errMonthlyChargeNeedsReview) || result["billingStatus"] != "manual_review" || len(sub2API.charges) != 1 || len(sub2API.refunds) != 0 || len(ledger.receipts) != 1 || ledger.receipts[0].Type != "billing.charge_review_required.v1" {
		t.Fatalf("unknown result=%#v charges=%#v refunds=%#v receipts=%#v err=%v", result, sub2API.charges, sub2API.refunds, ledger.receipts, err)
	}
	before := len(*events)
	if _, err := app.purchaseMonthlyResource(context.Background(), service, input); !errors.Is(err, errMonthlyChargeNeedsReview) {
		t.Fatalf("manual review replay err=%v", err)
	}
	if len(*events) != before || len(sub2API.charges) != 1 || len(sub2API.refunds) != 0 || len(fabric.preflightInputs) != 1 || len(fabric.computeIDs) != 1 || fabric.computeIDs[0] != "compute-unknown" || len(ledger.receipts) != 1 {
		t.Fatalf("manual review changed identity or retried: events=%#v charges=%#v refunds=%#v creates=%#v", *events, sub2API.charges, sub2API.refunds, fabric.computeIDs)
	}
}

func TestMonthlyPurchaseCommercialReadbackMismatchNeedsManualReviewWithoutRefund(t *testing.T) {
	tests := []struct {
		name          string
		resourceType  string
		mutateCompute func(*clients.ComputeAllocation)
		mutateStorage func(*clients.StorageVolume)
	}{
		{name: "compute account missing", resourceType: "compute", mutateCompute: func(v *clients.ComputeAllocation) { v.AccountID = "" }},
		{name: "compute workspace missing", resourceType: "compute", mutateCompute: func(v *clients.ComputeAllocation) { v.WorkspaceID = "" }},
		{name: "compute provider id missing", resourceType: "compute", mutateCompute: func(v *clients.ComputeAllocation) { v.ProviderResourceID = "" }},
		{name: "compute charge type wrong", resourceType: "compute", mutateCompute: func(v *clients.ComputeAllocation) { v.ChargeType = "POSTPAID_BY_HOUR" }},
		{name: "compute renew flag missing", resourceType: "compute", mutateCompute: func(v *clients.ComputeAllocation) { v.RenewFlag = "" }},
		{name: "compute deadline too early", resourceType: "compute", mutateCompute: func(v *clients.ComputeAllocation) { v.Deadline = "2026-07-31T00:00:00Z" }},
		{name: "compute zone missing", resourceType: "compute", mutateCompute: func(v *clients.ComputeAllocation) { v.Zone, v.ProviderData["zone"] = "", "" }},
		{name: "compute zone mismatches", resourceType: "compute", mutateCompute: func(v *clients.ComputeAllocation) { v.Zone, v.ProviderData["zone"] = "ap-shanghai-3", "ap-shanghai-3" }},
		{name: "compute instance type missing", resourceType: "compute", mutateCompute: func(v *clients.ComputeAllocation) { v.InstanceType, v.ProviderData["instanceType"] = "", "" }},
		{name: "compute instance type conflicts", resourceType: "compute", mutateCompute: func(v *clients.ComputeAllocation) { v.ProviderData["instanceType"] = "SA5.2XLARGE16" }},
		{name: "storage size mismatches", resourceType: "storage", mutateStorage: func(v *clients.StorageVolume) { v.SizeGB++ }},
		{name: "storage zone mismatches", resourceType: "storage", mutateStorage: func(v *clients.StorageVolume) { v.Zone = "ap-shanghai-3" }},
		{name: "storage charge type wrong", resourceType: "storage", mutateStorage: func(v *clients.StorageVolume) { v.ProviderData["chargeType"] = "POSTPAID_BY_HOUR" }},
		{name: "storage CBS status missing", resourceType: "storage", mutateStorage: func(v *clients.StorageVolume) { v.CBSStatus = "" }},
		{name: "storage CBS status not ready", resourceType: "storage", mutateStorage: func(v *clients.StorageVolume) { v.CBSStatus = "CREATING" }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			app, service, sub2API, fabric, ledger, _ := newMonthlyBillingTest(t, []int64{1_000_000_000, 950_000_000})
			fabric.mutateCompute, fabric.mutateStorage = tc.mutateCompute, tc.mutateStorage
			input := monthlyPurchaseInput{
				ResourceType: tc.resourceType, ResourceID: tc.resourceType + "-readback", BillingOperationID: "billing-" + strings.ReplaceAll(tc.name, " ", "-"),
				AccountID: "acct-monthly", WorkspaceID: "workspace-monthly", PackageID: "basic", Environment: "test",
				Now: time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC),
			}
			if tc.resourceType == "storage" {
				input.SizeGB, input.ComputeID, input.Zone = 10, "compute-placement", "ap-shanghai-2"
			}

			result, err := app.purchaseMonthlyResource(context.Background(), service, input)
			if !errors.Is(err, errMonthlyChargeNeedsReview) || result["billingStatus"] != "manual_review" {
				t.Fatalf("result=%#v err=%v", result, err)
			}
			if len(sub2API.charges) != 1 || len(sub2API.refunds) != 0 || len(ledger.receipts) != 1 || ledger.receipts[0].Type != "billing.charge_review_required.v1" {
				t.Fatalf("charges=%#v refunds=%#v receipts=%#v", sub2API.charges, sub2API.refunds, ledger.receipts)
			}
		})
	}
}

func TestMonthlyPurchaseRejectsConsistentButWrongComputeSKU(t *testing.T) {
	t.Setenv("OPL_BASIC_COMPUTE_INSTANCE_TYPE", "S5.MEDIUM4")
	t.Setenv("OPL_PRO_COMPUTE_INSTANCE_TYPE", "SA5.2XLARGE16")
	for _, tc := range []struct {
		packageID string
		wrongSKU  string
	}{
		{packageID: "basic", wrongSKU: "SA5.2XLARGE16"},
		{packageID: "pro", wrongSKU: "S5.MEDIUM4"},
	} {
		t.Run(tc.packageID, func(t *testing.T) {
			app, service, sub2API, fabric, ledger, _ := newMonthlyBillingTest(t, []int64{1_000_000_000, 0})
			fabric.mutateCompute = func(result *clients.ComputeAllocation) {
				result.InstanceType = tc.wrongSKU
				result.ProviderData["instanceType"] = tc.wrongSKU
			}

			result, err := app.purchaseMonthlyResource(context.Background(), service, monthlyPurchaseInput{
				ResourceType: "compute", ResourceID: "compute-wrong-sku-" + tc.packageID,
				BillingOperationID: "billing-wrong-sku-" + tc.packageID, AccountID: "acct-monthly",
				WorkspaceID: "workspace-monthly", PackageID: tc.packageID, Environment: "test",
				Now: time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC),
			})
			if !errors.Is(err, errMonthlyChargeNeedsReview) || result["billingStatus"] != "manual_review" {
				t.Fatalf("wrong %s SKU activated: result=%#v err=%v", tc.packageID, result, err)
			}
			if len(sub2API.charges) != 1 || len(sub2API.refunds) != 0 || len(ledger.receipts) != 1 || ledger.receipts[0].Type != "billing.charge_review_required.v1" {
				t.Fatalf("charges=%#v refunds=%#v receipts=%#v", sub2API.charges, sub2API.refunds, ledger.receipts)
			}
		})
	}
}

func TestMonthlyPurchaseClampsEntitlementToCanonicalProviderDeadline(t *testing.T) {
	app, service, _, fabric, ledger, _ := newMonthlyBillingTest(t, []int64{100_000_000, 50_000_000})
	fabric.mutateCompute = func(result *clients.ComputeAllocation) {
		result.Deadline = "2026-08-16T08:00:00+08:00"
		result.ProviderData["deadline"] = result.Deadline
	}
	now := time.Date(2026, 7, 16, 8, 30, 0, 0, time.UTC)

	result, err := app.purchaseMonthlyResource(context.Background(), service, monthlyPurchaseInput{
		ResourceType: "compute", ResourceID: "compute-deadline", BillingOperationID: "billing-deadline",
		AccountID: "acct-monthly", WorkspaceID: "workspace-monthly", PackageID: "basic", Environment: "test", Now: now,
	})
	if err != nil || result["billingStatus"] != "active" {
		t.Fatalf("purchase result=%#v err=%v", result, err)
	}
	wantDeadline := "2026-08-16T00:00:00Z"
	if result["deadline"] != wantDeadline || result["paidThrough"] != wantDeadline || providerDataValue(result, "deadline") != wantDeadline {
		t.Fatalf("deadline was not canonical and bounded: %#v", result)
	}
	stored, ok := app.getCompute("compute-deadline")
	if !ok || stored["deadline"] != wantDeadline || stored["paidThrough"] != wantDeadline {
		t.Fatalf("stored compute=%#v", stored)
	}
	if len(ledger.receipts) != 1 || ledger.receipts[0].Cost["paidThrough"] != wantDeadline {
		t.Fatalf("receipt=%#v", ledger.receipts)
	}
}

func TestMonthlyProviderDeadlineRejectsTimestampWithoutTimezone(t *testing.T) {
	if _, err := monthlyProviderDeadline(map[string]any{"deadline": "2026-08-16 12:34:56"}); err == nil {
		t.Fatal("provider deadline without timezone must fail closed")
	}
}

func TestCleanupMonthlyStoragePreservesFabricRetentionStatus(t *testing.T) {
	app, service, _, fabric, _, _ := newMonthlyBillingTest(t, nil)
	fabric.cleanupStatus = "retained"
	row := monthlyActiveResource("storage", "storage-cleanup-retained", time.Now().UTC().Add(time.Hour))

	result, err := app.cleanupMonthlyResource(context.Background(), service, row)
	if err != nil || result["status"] != "retained" || result["desiredStatus"] != "destroyed" {
		t.Fatalf("cleanup result=%#v err=%v", result, err)
	}
}

func TestMonthlyPurchaseResumesProvisioningThroughFabricSync(t *testing.T) {
	events := &[]string{}
	sub2API := &monthlySub2API{events: events, balances: []int64{100_000_000, 50_000_000}}
	fabric := &provisioningMonthlyFabric{monthlyFabric: monthlyFabric{events: events}}
	ledger := &monthlyLedger{events: events}
	app := newControlPlaneAppEmpty()
	if err := app.tables.SaveAccount(context.Background(), map[string]any{"id": "acct-monthly", "status": "active", "sub2apiUserId": int64(41)}); err != nil {
		t.Fatal(err)
	}
	service := controlplane.NewService(ledger, fabric, sub2API)
	input := monthlyPurchaseInput{ResourceType: "compute", ResourceID: "compute-provisioning", BillingOperationID: "billing-provisioning", AccountID: "acct-monthly", PackageID: "basic", Environment: "test", Now: time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC)}

	first, err := app.purchaseMonthlyResource(context.Background(), service, input)
	if err != nil || first["billingStatus"] != "preparing" || len(sub2API.charges) != 1 {
		t.Fatalf("first purchase=%#v charges=%#v err=%v", first, sub2API.charges, err)
	}
	second, err := app.purchaseMonthlyResource(context.Background(), service, input)
	if err != nil || second["billingStatus"] != "active" || len(fabric.preflightInputs) != 1 || len(fabric.computeIDs) != 1 || fabric.syncCalls != 1 || len(sub2API.charges) != 1 {
		t.Fatalf("recovered purchase=%#v creates=%#v syncs=%d charges=%#v err=%v", second, fabric.computeIDs, fabric.syncCalls, sub2API.charges, err)
	}
}

func TestMonthlyPurchaseUnconfirmedDebitDoesNotMutateFabric(t *testing.T) {
	app, service, sub2API, fabric, ledger, events := newMonthlyBillingTest(t, []int64{100_000_000, 60_000_000})
	result, err := app.purchaseMonthlyResource(context.Background(), service, monthlyPurchaseInput{ResourceType: "compute", ResourceID: "compute-unconfirmed", BillingOperationID: "billing-unconfirmed", AccountID: "acct-monthly", PackageID: "basic", Environment: "test", Now: time.Now().UTC()})
	if !errors.Is(err, errMonthlyChargeNeedsReview) || result["billingStatus"] != "manual_review" || len(sub2API.charges) != 1 || len(fabric.computeIDs) != 0 || len(ledger.receipts) != 1 || ledger.receipts[0].Type != "billing.charge_review_required.v1" {
		t.Fatalf("unconfirmed debit result=%#v charges=%#v creates=%#v err=%v", result, sub2API.charges, fabric.computeIDs, err)
	}
	if strings.Join(*events, ",") != "fabric.monthly.preflight,sub2api.balance,sub2api.charge,sub2api.balance,ledger.receipt" {
		t.Fatalf("events = %#v", *events)
	}
}

func TestMonthlyPurchaseRecoversLostChargeResponseWithSameCode(t *testing.T) {
	app, service, sub2API, fabric, _, _ := newMonthlyBillingTest(t, []int64{100_000_000, 100_000_000, 50_000_000})
	sub2API.chargeErrors = []error{clients.ErrSub2APIChargeUnknown, nil}
	input := monthlyPurchaseInput{ResourceType: "compute", ResourceID: "compute-recover", BillingOperationID: "billing-recover", AccountID: "acct-monthly", PackageID: "basic", Environment: "test", Now: time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC)}
	first, err := app.purchaseMonthlyResource(context.Background(), service, input)
	if !errors.Is(err, clients.ErrSub2APIChargeUnknown) || first["billingStatus"] != "charge_pending" {
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

func TestMonthlyPurchaseAllowsExactBalance(t *testing.T) {
	app, service, sub2API, _, ledger, _ := newMonthlyBillingTest(t, []int64{50_000_000, 0})
	result, err := app.purchaseMonthlyResource(context.Background(), service, monthlyPurchaseInput{ResourceType: "compute", ResourceID: "compute-zero", BillingOperationID: "billing-zero", AccountID: "acct-monthly", PackageID: "basic", Environment: "test", Now: time.Now().UTC()})
	if err != nil || result["billingStatus"] != "active" || result["postChargeBalanceUsdMicros"] != int64(0) || len(sub2API.charges) != 1 || len(ledger.receipts) != 1 {
		t.Fatalf("exact balance result=%#v charges=%#v receipts=%#v err=%v", result, sub2API.charges, ledger.receipts, err)
	}
}

func TestMonthlyPurchaseRetriesReceiptWithoutChargingAgain(t *testing.T) {
	app, service, sub2API, fabric, ledger, _ := newMonthlyBillingTest(t, []int64{100_000_000, 50_000_000})
	ledger.receiptErrors = []error{errors.New("ledger unavailable"), nil}
	input := monthlyPurchaseInput{ResourceType: "compute", ResourceID: "compute-receipt", BillingOperationID: "billing-receipt", AccountID: "acct-monthly", PackageID: "basic", Environment: "test", Now: time.Now().UTC()}
	first, err := app.purchaseMonthlyResource(context.Background(), service, input)
	if err != nil || first["billingStatus"] != "active" || first["lastReceiptId"] != nil {
		t.Fatalf("receipt outage result=%#v err=%v", first, err)
	}
	second, err := app.purchaseMonthlyResource(context.Background(), service, input)
	if err != nil || second["lastReceiptId"] != "receipt-monthly" || len(fabric.preflightInputs) != 1 || len(sub2API.charges) != 1 || len(ledger.receipts) != 2 {
		t.Fatalf("receipt retry result=%#v charges=%d receipts=%d err=%v", second, len(sub2API.charges), len(ledger.receipts), err)
	}
}

func TestMonthlyReviewAndReceiptPendingReturnPersistenceErrors(t *testing.T) {
	persistErr := errors.New("monthly state unavailable")
	row := map[string]any{
		"id": "compute-review", "accountId": "acct-monthly", "packageId": "basic", "billingOperationId": "billing-review",
		"pricingVersion": pricingCatalogVersion, "monthlyPriceCnyCents": int64(35000), "chargeUsdMicros": int64(50_000_000),
		"periodStart": "2026-07-16T00:00:00Z", "paidThrough": "2026-08-16T00:00:00Z",
	}

	t.Run("manual review", func(t *testing.T) {
		events := &[]string{}
		ledger := &monthlyLedger{events: events}
		app := newControlPlaneAppEmpty()
		app.tables = &failingMonthlySaveStore{memoryTableStore: newMemoryTableStore(), err: persistErr}
		_, err := app.markMonthlyManualReview(context.Background(), controlplane.NewService(ledger, nil, nil), cloneMap(row), 41, "provider_unknown")
		if !errors.Is(err, persistErr) || len(ledger.receipts) != 0 {
			t.Fatalf("manual review err=%v receipts=%#v", err, ledger.receipts)
		}
	})

	t.Run("ledger receipt pending", func(t *testing.T) {
		events := &[]string{}
		ledger := &monthlyLedger{events: events, receiptErrors: []error{errors.New("ledger unavailable")}}
		app := newControlPlaneAppEmpty()
		app.tables = &failingMonthlySaveStore{memoryTableStore: newMemoryTableStore(), err: persistErr}
		_, err := app.ensureMonthlyReceipt(context.Background(), controlplane.NewService(ledger, nil, nil), cloneMap(row), 41, "billing.resource_purchased.v1")
		if !errors.Is(err, persistErr) {
			t.Fatalf("receipt pending persistence error = %v", err)
		}
	})

	t.Run("charge conflict", func(t *testing.T) {
		events := &[]string{}
		sub2API := &monthlySub2API{events: events, chargeErrors: []error{clients.ErrSub2APIChargeConflict}}
		app := newControlPlaneAppEmpty()
		app.tables = &failingMonthlySaveStore{memoryTableStore: newMemoryTableStore(), err: persistErr}
		_, err := app.chargeMonthlyOperation(context.Background(), controlplane.NewService(nil, nil, sub2API), cloneMap(row), 41, 100_000_000)
		if !errors.Is(err, persistErr) {
			t.Fatalf("charge review persistence error = %v", err)
		}
	})
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
	t.Setenv("OPL_TENCENT_ZONE", "ap-shanghai-2")
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
	rec := requestWithSession(t, server, session, http.MethodPost, "/api/compute-allocations", `{"packageId":"basic","name":"Monthly Compute","zone":"ap-guangzhou-3"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("purchase status = %d: %s", rec.Code, rec.Body.String())
	}
	var result map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode purchase: %v", err)
	}
	if result["billingStatus"] != "active" || result["zone"] != "ap-shanghai-2" || len(sub2API.charges) != 1 || len(ledger.receipts) != 1 {
		t.Fatalf("purchase result=%#v charges=%#v receipts=%#v", result, sub2API.charges, ledger.receipts)
	}
	if len(fabric.preflightInputs) != 1 || fabric.preflightInputs[0].Zone != "ap-shanghai-2" {
		t.Fatalf("compute preflight input = %#v", fabric.preflightInputs)
	}
	computes, err := store.ListComputes(context.Background(), "acct-alpha")
	if err != nil || len(computes) != 1 || computes[0]["zone"] != "ap-shanghai-2" {
		t.Fatalf("stored computes=%#v err=%v", computes, err)
	}
}

func TestStoragePurchaseUsesOwnedComputeZone(t *testing.T) {
	events := &[]string{}
	sub2API := &monthlySub2API{events: events, balances: []int64{100_000_000, 97_428_571}}
	fabric := &monthlyFabric{events: events}
	store := newMemoryTableStore()
	seedTenantMember(t, store, "acct-alpha", "org-alpha", "usr-alpha", "alpha@example.com")
	mustStore(t, store.SaveCompute(context.Background(), map[string]any{
		"id": "compute-alpha", "accountId": "acct-alpha", "workspaceId": "workspace-alpha", "status": "running",
		"packageId":     "basic",
		"billingStatus": "active", "paidThrough": time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
		"providerData": map[string]any{"zone": "ap-shanghai-2"},
	}))
	server, err := NewPersistentServer(controlplane.NewService(&monthlyLedger{events: events}, fabric, sub2API), store)
	if err != nil {
		t.Fatal(err)
	}
	session := loginForTest(t, server, "alpha@example.com", "CorrectHorseBatteryStaple!")

	missing := requestWithSession(t, server, session, http.MethodPost, "/api/storage-volumes", `{"sizeGb":10}`)
	if missing.Code != http.StatusBadRequest || !strings.Contains(missing.Body.String(), "compute_allocation_required") || len(*events) != 0 {
		t.Fatalf("missing compute response=%d %s events=%#v", missing.Code, missing.Body.String(), *events)
	}
	created := requestWithSession(t, server, session, http.MethodPost, "/api/storage-volumes", `{"sizeGb":10,"computeAllocationId":"compute-alpha","workspaceId":"workspace-alpha"}`)
	if created.Code != http.StatusAccepted {
		t.Fatalf("storage purchase status=%d body=%s", created.Code, created.Body.String())
	}
	if len(fabric.storageInputs) != 1 || fabric.storageInputs[0].ComputeID != "compute-alpha" || fabric.storageInputs[0].Zone != "ap-shanghai-2" || fabric.storageInputs[0].WorkspaceID != "workspace-alpha" {
		t.Fatalf("storage placement = %#v", fabric.storageInputs)
	}
}

func TestStoragePurchaseRejectsPackageMismatchBeforeExternalCalls(t *testing.T) {
	for _, tc := range []struct {
		name           string
		computePackage string
		requestPackage string
	}{
		{name: "requested package", computePackage: "basic", requestPackage: `,"packageId":"pro"`},
		{name: "default package", computePackage: "pro"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := &[]string{}
			store := newMemoryTableStore()
			seedTenantMember(t, store, "acct-alpha", "org-alpha", "usr-alpha", "alpha@example.com")
			mustStore(t, store.SaveCompute(context.Background(), map[string]any{
				"id": "compute-alpha", "accountId": "acct-alpha", "workspaceId": "workspace-alpha", "status": "running",
				"packageId": tc.computePackage, "billingStatus": "active", "paidThrough": time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
				"providerData": map[string]any{"zone": "ap-shanghai-2"},
			}))
			server, err := NewPersistentServer(controlplane.NewService(&monthlyLedger{events: events}, &monthlyFabric{events: events}, &monthlySub2API{events: events}), store)
			if err != nil {
				t.Fatal(err)
			}
			session := loginForTest(t, server, "alpha@example.com", "CorrectHorseBatteryStaple!")
			response := requestWithSession(t, server, session, http.MethodPost, "/api/storage-volumes", `{"sizeGb":10,"computeAllocationId":"compute-alpha"`+tc.requestPackage+`}`)
			if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "compute_storage_package_mismatch") {
				t.Fatalf("mismatch response=%d %s", response.Code, response.Body.String())
			}
			if len(*events) != 0 {
				t.Fatalf("package mismatch reached external services: %#v", *events)
			}
		})
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
	sub2API := &monthlySub2API{events: events, balances: []int64{100_000_000, 92_285_714}}
	fabric := &provisioningMonthlyFabric{monthlyFabric: monthlyFabric{events: events}}
	fabric.storageInput = clients.StorageVolumeInput{ID: "storage-retained", AccountID: "acct-alpha", WorkspaceID: "workspace-monthly", ComputeID: "compute-retained", Zone: "ap-shanghai-2", SizeGB: 30}
	store := newMemoryTableStore()
	seedTenantMember(t, store, "acct-alpha", "org-alpha", "usr-alpha", "alpha@example.com")
	mustStore(t, store.SaveCompute(context.Background(), map[string]any{
		"id": "compute-retained", "accountId": "acct-alpha", "workspaceId": "workspace-monthly", "packageId": "basic", "status": "running", "billingStatus": "active",
		"paidThrough": time.Now().UTC().Add(time.Hour).Format(time.RFC3339), "providerData": map[string]any{"zone": "ap-shanghai-2"},
	}))
	retained := monthlyActiveResource("storage", "storage-retained", time.Now().UTC().Add(-time.Hour))
	retained["accountId"], retained["ownerUserId"], retained["billingStatus"] = "acct-alpha", "usr-alpha", "retained"
	retained["sizeGb"], retained["monthlyPriceCnyCents"], retained["chargeUsdMicros"] = 30, int64(5400), int64(7_714_286)
	retained["computeAllocationId"], retained["zone"] = "compute-retained", "ap-shanghai-2"
	mustStore(t, store.SaveStorage(context.Background(), retained))
	server, err := NewPersistentServer(controlplane.NewService(&monthlyLedger{events: events}, fabric, sub2API), store)
	if err != nil {
		t.Fatal(err)
	}
	session := loginForTest(t, server, "alpha@example.com", "CorrectHorseBatteryStaple!")
	rec := requestWithSession(t, server, session, http.MethodPost, "/api/storage-volumes", `{"id":"storage-retained","sizeGb":10,"computeAllocationId":"compute-retained"}`)
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
	events := &[]string{}
	service := controlplane.NewService(nil, nil, &monthlySub2API{events: events, balances: []int64{0}})
	input := map[string]any{
		"email": "mapped@example.com", "accountId": "acct-mapped", "role": "owner",
		"password": "CorrectHorseBatteryStaple!",
	}
	if _, err := app.createUser(context.Background(), service, input); !errors.Is(err, errMonthlyAccountUnmapped) {
		t.Fatalf("missing Sub2API mapping error = %v, want %v", err, errMonthlyAccountUnmapped)
	}

	input["sub2apiUserId"] = float64(41)
	if _, err := app.createUser(context.Background(), service, input); err != nil {
		t.Fatalf("create mapped user: %v", err)
	}
	accounts, err := app.tables.ListAccounts(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	account := findRecord(accounts, "acct-mapped")
	if account == nil || int64(numberField(account, "sub2apiUserId", 0)) != 41 {
		t.Fatalf("persisted account mapping = %#v", account)
	}
}

func TestCreateUserRejectsSub2APIUserMappedToAnotherAccount(t *testing.T) {
	app := newControlPlaneAppEmpty()
	events := &[]string{}
	service := controlplane.NewService(nil, nil, &monthlySub2API{events: events, balances: []int64{0, 0}})
	for _, input := range []map[string]any{
		{"email": "one@example.com", "accountId": "acct-one", "role": "owner", "password": "CorrectHorseBatteryStaple!", "sub2apiUserId": float64(41)},
		{"email": "two@example.com", "accountId": "acct-two", "role": "owner", "password": "CorrectHorseBatteryStaple!", "sub2apiUserId": float64(41)},
	} {
		_, err := app.createUser(context.Background(), service, input)
		if stringValue(input["accountId"]) == "acct-one" && err != nil {
			t.Fatal(err)
		}
		if stringValue(input["accountId"]) == "acct-two" && (err == nil || err.Error() != "sub2api_account_mapping_conflict") {
			t.Fatalf("duplicate account mapping error = %v", err)
		}
	}
}

func TestSub2APIUserIDRejectsDuplicateStoredMapping(t *testing.T) {
	store := newMemoryTableStore()
	store.mu.Lock()
	store.accounts["acct-one"] = map[string]any{"id": "acct-one", "status": "active", "sub2apiUserId": int64(41)}
	store.accounts["acct-two"] = map[string]any{"id": "acct-two", "status": "active", "sub2apiUserId": int64(41)}
	store.mu.Unlock()
	app := newControlPlaneAppEmpty()
	app.tables = store
	if _, err := app.sub2APIUserID(context.Background(), "acct-one"); err == nil || err.Error() != "sub2api_account_mapping_conflict" {
		t.Fatalf("duplicate stored mapping error = %v", err)
	}
}

func TestCreateUserValidatesSub2APIUserBeforePersisting(t *testing.T) {
	events := &[]string{}
	store := newMemoryTableStore()
	sub2API := &monthlySub2API{events: events, balanceErr: errors.New("sub2api unavailable")}
	server, err := NewPersistentServer(controlplane.NewService(&monthlyLedger{events: events}, &monthlyFabric{events: events}, sub2API), store)
	if err != nil {
		t.Fatal(err)
	}
	session := operatorSessionForTest(t, server)
	response := requestWithSession(t, server, session, http.MethodPost, "/api/users", `{"email":"unverified@example.com","accountId":"acct-unverified","role":"owner","password":"CorrectHorseBatteryStaple!","sub2apiUserId":41}`)
	if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), "sub2api_user_mapping_unverified") {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	users, err := store.ListUsers(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := store.ListAccounts(context.Background(), "acct-unverified")
	if err != nil {
		t.Fatal(err)
	}
	persistedUser := false
	for _, user := range users {
		persistedUser = persistedUser || stringValue(user["email"]) == "unverified@example.com"
	}
	if persistedUser || len(accounts) != 0 {
		t.Fatalf("unverified mapping persisted users=%#v accounts=%#v", users, accounts)
	}
	if len(*events) != 1 || (*events)[0] != "sub2api.balance" {
		t.Fatalf("mapping validation events=%#v", *events)
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
	sub2API := &monthlySub2API{events: events, balances: []int64{123_456_789, 123_456_789}}
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
	if balance["source"] != "sub2api" || balance["currency"] != "USD" || balance["status"] != "available" || balance["available"] != true || int64(numberField(balance, "userId", 0)) != 41 || int64(numberField(balance, "usdMicros", 0)) != 123_456_789 {
		t.Fatalf("state balance = %#v", balance)
	}
}

func TestStateRouteDegradesWhenSub2APIBalanceIsUnavailable(t *testing.T) {
	events := &[]string{}
	sub2API := &monthlySub2API{events: events, balances: []int64{0}}
	store := newMemoryTableStore()
	if err := store.SaveAccount(context.Background(), map[string]any{"id": "acct-alpha", "status": "active", "sub2apiUserId": int64(41)}); err != nil {
		t.Fatal(err)
	}
	server, err := NewPersistentServer(controlplane.NewService(&monthlyLedger{events: events}, &monthlyFabric{events: events}, sub2API), store)
	if err != nil {
		t.Fatal(err)
	}
	session := tenantAdminSessionForTest(t, server)
	sub2API.balanceErr = errors.New("sub2api unavailable")
	response := requestWithSession(t, server, session, http.MethodGet, "/api/state", "")
	if response.Code != http.StatusOK {
		t.Fatalf("state status=%d body=%s", response.Code, response.Body.String())
	}
	var state map[string]any
	if err := json.NewDecoder(response.Body).Decode(&state); err != nil {
		t.Fatal(err)
	}
	balance, _ := state["balance"].(map[string]any)
	if balance["source"] != "sub2api" || balance["currency"] != "USD" || balance["status"] != "unavailable" || balance["available"] != false {
		t.Fatalf("degraded balance=%#v", balance)
	}
	if _, exists := balance["usdMicros"]; exists {
		t.Fatalf("degraded balance must not look like zero: %#v", balance)
	}
}

func TestPaidResourceIdempotencyKeysAreScopedToTheSessionAccount(t *testing.T) {
	events := &[]string{}
	sub2API := &monthlySub2API{events: events, balances: []int64{100_000_000, 50_000_000, 100_000_000, 50_000_000}}
	fabric := &monthlyFabric{events: events}
	store := newMemoryTableStore()
	seedTenantMember(t, store, "acct-alpha", "org-alpha", "usr-alpha", "alpha@example.com")
	seedTenantMember(t, store, "acct-beta", "org-beta", "usr-beta", "beta@example.com")
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

func TestVerificationSlotUsesNormalIdempotentCommercialPurchase(t *testing.T) {
	events := &[]string{}
	sub2API := &monthlySub2API{events: events, balances: []int64{100_000_000, 50_000_000, 50_000_000, 47_428_571}}
	fabric := &monthlyFabric{events: events}
	ledger := &monthlyLedger{events: events}
	store := newMemoryTableStore()
	seedTenantMember(t, store, "acct-alpha", "org-alpha", "usr-alpha", "alpha@example.com")
	server, err := NewPersistentServer(controlplane.NewService(ledger, fabric, sub2API), store)
	if err != nil {
		t.Fatal(err)
	}
	session := loginForTest(t, server, "alpha@example.com", "CorrectHorseBatteryStaple!")

	computeBody := `{"packageId":"basic","name":"verification-slot-01"}`
	compute := requestWithSession(t, server, session, http.MethodPost, "/api/compute-allocations", computeBody)
	computeReplay := requestWithSession(t, server, session, http.MethodPost, "/api/compute-allocations", computeBody)
	if compute.Code != http.StatusAccepted || computeReplay.Code != http.StatusAccepted {
		t.Fatalf("compute=%d %s replay=%d %s", compute.Code, compute.Body.String(), computeReplay.Code, computeReplay.Body.String())
	}
	var computeResult, computeReplayResult map[string]any
	if err := json.NewDecoder(compute.Body).Decode(&computeResult); err != nil {
		t.Fatal(err)
	}
	if err := json.NewDecoder(computeReplay.Body).Decode(&computeReplayResult); err != nil {
		t.Fatal(err)
	}
	storageBody := `{"packageId":"basic","sizeGb":10,"name":"verification-slot-01","computeAllocationId":"` + stringValue(computeResult["id"]) + `"}`
	storage := requestWithSession(t, server, session, http.MethodPost, "/api/storage-volumes", storageBody)
	storageReplay := requestWithSession(t, server, session, http.MethodPost, "/api/storage-volumes", storageBody)
	if storage.Code != http.StatusAccepted || storageReplay.Code != http.StatusAccepted {
		t.Fatalf("storage=%d %s replay=%d %s", storage.Code, storage.Body.String(), storageReplay.Code, storageReplay.Body.String())
	}
	var storageResult, storageReplayResult map[string]any
	if err := json.NewDecoder(storage.Body).Decode(&storageResult); err != nil {
		t.Fatal(err)
	}
	if err := json.NewDecoder(storageReplay.Body).Decode(&storageReplayResult); err != nil {
		t.Fatal(err)
	}

	if computeResult["id"] != computeReplayResult["id"] || storageResult["id"] != storageReplayResult["id"] || computeResult["name"] != "verification-slot-01" || storageResult["name"] != "verification-slot-01" {
		t.Fatalf("compute=%#v replay=%#v storage=%#v replay=%#v", computeResult, computeReplayResult, storageResult, storageReplayResult)
	}
	if len(sub2API.charges) != 2 || len(fabric.preflightInputs) != 2 || len(fabric.computeIDs) != 1 || len(fabric.storageIDs) != 1 || len(ledger.receipts) != 2 {
		t.Fatalf("charges=%#v preflights=%#v computes=%#v storages=%#v receipts=%#v", sub2API.charges, fabric.preflightInputs, fabric.computeIDs, fabric.storageIDs, ledger.receipts)
	}
	for _, receipt := range ledger.receipts {
		if receipt.AccountID != "acct-alpha" || receipt.Type != "billing.resource_purchased.v1" {
			t.Fatalf("verification slot escaped normal tenant receipt: %#v", receipt)
		}
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
			sub2API := &monthlySub2API{events: events, balances: []int64{initial, initial, initial - tc.charge}}
			fabric := &provisioningMonthlyFabric{monthlyFabric: monthlyFabric{events: events}}
			ledger := &monthlyLedger{events: events}
			store := newMemoryTableStore()
			if err := store.SaveAccount(context.Background(), map[string]any{"id": "acct-alpha", "status": "active", "sub2apiUserId": int64(41)}); err != nil {
				t.Fatal(err)
			}
			if tc.resourceType == "storage" {
				mustStore(t, store.SaveCompute(context.Background(), map[string]any{
					"id": "compute-placement", "accountId": "acct-alpha", "workspaceId": "workspace-placement", "packageId": "basic", "status": "running", "billingStatus": "active",
					"paidThrough": time.Now().UTC().Add(time.Hour).Format(time.RFC3339), "providerData": map[string]any{"zone": "ap-shanghai-2"},
				}))
				tc.createBody = `{"sizeGb":10,"computeAllocationId":"compute-placement"}`
			}
			server, err := NewPersistentServer(controlplane.NewService(ledger, fabric, sub2API), store)
			if err != nil {
				t.Fatal(err)
			}
			session := tenantAdminSessionForTest(t, server)
			created := requestWithSession(t, server, session, http.MethodPost, tc.createPath, tc.createBody)
			if created.Code != http.StatusAccepted {
				t.Fatalf("create status=%d body=%s events=%#v", created.Code, created.Body.String(), *events)
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
	sub2API := &monthlySub2API{events: events, balances: []int64{0}}
	fabricCalls := []string{}
	fabric := &monthlyFabric{events: events, fakeFabricClient: fakeFabricClient{calls: &fabricCalls}}
	ledger := &monthlyLedger{events: events}
	store := newMemoryTableStore()
	now := time.Now().UTC()
	if err := store.SaveCompute(context.Background(), map[string]any{"id": "compute-inactive", "accountId": "acct-alpha", "workspaceId": "workspace-monthly", "packageId": "basic", "status": "running", "billingStatus": "preparing", "paidThrough": now.Add(time.Hour).Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveStorage(context.Background(), map[string]any{"id": "storage-active", "accountId": "acct-alpha", "workspaceId": "workspace-monthly", "packageId": "basic", "status": "available", "billingStatus": "active", "paidThrough": now.Add(time.Hour).Format(time.RFC3339)}); err != nil {
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
