package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

type monthlySub2API struct {
	testSub2APIClient
	events            *[]string
	balances          []int64
	balanceErr        error
	balanceErrors     []error
	chargeErrors      []error
	chargeResults     []clients.Sub2APICharge
	charges           []clients.Sub2APIChargeInput
	refundErrors      []error
	refunds           []clients.Sub2APIRefundInput
	workspaceKeyErr   error
	workspaceKeyCalls []int64
}

type authoritativeReplaySub2API struct {
	client       *clients.Sub2APIHTTPClient
	codes        []string
	values       []string
	historyCalls int
	adjusted     bool
}

type authoritativeReplayConfig struct {
	chargeValue       string
	initialBalance    json.RawMessage
	adjustedBalance   json.RawMessage
	historyStatus     int
	historyEntries    func(code, value string) []any
	loseFirstResponse bool
}

func authoritativeHistoryEntry(code, value string) map[string]any {
	return map[string]any{
		"code": code, "type": "balance", "value": json.RawMessage(value), "status": "used", "used_by": 41,
		"used_at": "2026-07-16T00:01:00Z", "created_at": "2026-07-16T00:00:00Z",
	}
}

func newAuthoritativeReplaySub2API(t *testing.T, config authoritativeReplayConfig) *authoritativeReplaySub2API {
	t.Helper()
	fixture := &authoritativeReplaySub2API{adjusted: !config.loseFirstResponse}
	success := func(w http.ResponseWriter, data any) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"code": 0, "message": "success", "data": data}); err != nil {
			t.Errorf("encode Sub2API response: %v", err)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			success(w, map[string]any{"access_token": "access", "refresh_token": "refresh"})
		case "/api/v1/admin/users/41":
			balance := config.initialBalance
			if fixture.adjusted {
				balance = config.adjustedBalance
			}
			success(w, map[string]any{"id": 41, "balance": balance, "status": "active"})
		case "/api/v1/admin/usage/search-api-keys":
			name := workspaceReservedKeyName("workspace-monthly")
			if r.URL.Query().Get("user_id") != "41" || r.URL.Query().Get("q") != name {
				t.Fatalf("Workspace Key search = %s", r.URL.String())
			}
			success(w, []any{map[string]any{"id": 9, "user_id": 41, "name": name}})
		case "/api/v1/admin/users/41/api-keys":
			if r.URL.Query().Get("page") != "1" || r.URL.Query().Get("page_size") != "1" {
				t.Fatalf("Workspace Key read = %s", r.URL.String())
			}
			success(w, map[string]any{"items": []any{map[string]any{
				"id": 9, "user_id": 41, "name": workspaceReservedKeyName("workspace-monthly"), "key": "workspace-key-secret", "status": "active",
				"quota": 0, "quota_used": 0, "usage_5h": 0, "usage_1d": 0, "usage_7d": 0,
			}}, "total": 1, "page": 1, "page_size": 1, "pages": 1})
		case "/api/v1/admin/redeem-codes/create-and-redeem":
			var input struct {
				Code   string      `json:"code"`
				Type   string      `json:"type"`
				Value  json.Number `json:"value"`
				UserID int64       `json:"user_id"`
			}
			decoder := json.NewDecoder(r.Body)
			decoder.UseNumber()
			if err := decoder.Decode(&input); err != nil || input.Code == "" || input.Type != "balance" || input.UserID != 41 || input.Value.String() != config.chargeValue {
				t.Fatalf("balance adjustment = %#v err=%v", input, err)
			}
			fixture.codes = append(fixture.codes, input.Code)
			fixture.values = append(fixture.values, input.Value.String())
			if config.loseFirstResponse && len(fixture.codes) == 1 {
				fixture.adjusted = true
				http.Error(w, "response lost after adjustment", http.StatusInternalServerError)
				return
			}
			http.Error(w, "redeem code exists", http.StatusConflict)
		case "/api/v1/admin/users/41/balance-history":
			fixture.historyCalls++
			if len(fixture.codes) == 0 || r.URL.Query().Get("type") != "balance" {
				t.Fatalf("history request without adjustment: %s", r.URL.String())
			}
			if config.historyStatus != 0 {
				http.Error(w, "history unavailable", config.historyStatus)
				return
			}
			code, value := fixture.codes[len(fixture.codes)-1], fixture.values[len(fixture.values)-1]
			items := []any{authoritativeHistoryEntry(code, value)}
			if config.historyEntries != nil {
				items = config.historyEntries(code, value)
			}
			success(w, map[string]any{"items": items, "total": len(items), "page": 1, "page_size": 100, "pages": 1})
		default:
			t.Fatalf("unexpected Sub2API route %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	client, err := clients.NewSub2APIHTTPClient(clients.Sub2APIConfig{
		BaseURL: server.URL, AdminEmail: "admin@example.test", AdminPassword: "admin-secret", Timeout: time.Second,
	}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	fixture.client = client
	return fixture
}

func (s *monthlySub2API) Version(context.Context) (string, error) { return "0.1.155", nil }

func (s *monthlySub2API) Balance(_ context.Context, userID int64) (clients.Sub2APIBalance, error) {
	*s.events = append(*s.events, "sub2api.balance")
	if len(s.balanceErrors) > 0 {
		err := s.balanceErrors[0]
		s.balanceErrors = s.balanceErrors[1:]
		if err != nil {
			return clients.Sub2APIBalance{}, err
		}
	}
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

func (s *monthlySub2API) WorkspaceKey(_ context.Context, userID int64) (clients.Sub2APIWorkspaceKey, error) {
	s.workspaceKeyCalls = append(s.workspaceKeyCalls, userID)
	if s.workspaceKeyErr != nil {
		return clients.Sub2APIWorkspaceKey{}, s.workspaceKeyErr
	}
	return clients.Sub2APIWorkspaceKey{ID: 9, UserID: userID, Name: "opl-workspace", Key: "workspace-key-secret", Status: "active"}, nil
}

func (s *monthlySub2API) WorkspaceKeysForConvergence(_ context.Context, userID int64, name string) ([]clients.Sub2APIWorkspaceKey, error) {
	s.workspaceKeyCalls = append(s.workspaceKeyCalls, userID)
	if s.workspaceKeyErr != nil {
		return nil, s.workspaceKeyErr
	}
	if userID != 41 || name != workspaceReservedKeyName("workspace-monthly") {
		return nil, nil
	}
	return []clients.Sub2APIWorkspaceKey{{ID: 9, UserID: userID, Name: name, Key: "workspace-key-secret", Status: "active"}}, nil
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
	if len(s.chargeResults) > 0 {
		result := s.chargeResults[0]
		s.chargeResults = s.chargeResults[1:]
		return result, nil
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

func (s *monthlySub2API) FinancialBalanceHistoryByCodes(context.Context, int64, []string) (map[string]clients.Sub2APIBalanceHistoryEntry, error) {
	return map[string]clients.Sub2APIBalanceHistoryEntry{}, nil
}

type monthlyFabric struct {
	fakeFabricClient
	events                  *[]string
	createErr               error
	cleanupErr              error
	cleanupStatus           string
	computeCleanupStatus    string
	computeCleanupSync      clients.ComputeAllocation
	computeCleanupSyncErr   error
	computeCleanupStarted   bool
	computeDestroyed        bool
	syncErr                 error
	computeReadResults      []clients.ComputeAllocation
	computeReadErrors       []error
	preflightResult         *clients.MonthlyPreflight
	preflightResults        []clients.MonthlyPreflight
	preflightErr            error
	preflightInputs         []clients.MonthlyPreflightInput
	providerTruth           *clients.MonthlyProviderTruth
	providerTruthErr        error
	computeProviderTruth    *clients.ComputeProviderTruth
	computeProviderTruthErr error
	computeProviderTruthFn  func(clients.ComputeClaimRecoveryInput) (clients.ComputeProviderTruth, error)
	computeProviderInputs   []clients.ComputeClaimRecoveryInput
	activationTruth         *clients.WorkspaceActivationTruth
	activationTruthErr      error
	activationTruthInputs   []clients.WorkspaceActivationTruthInput
	computeClaimProof       clients.ComputeClaimRecoveryProof
	computeClaimProofErr    error
	computeClaimProofFn     func(clients.ComputeClaimRecoveryInput) (clients.ComputeClaimRecoveryProof, error)
	computeClaimResult      *clients.ComputeClaimRecoveryProof
	computeClaimErr         error
	computeClaimInputs      []clients.ComputeClaimRecoveryInput
	computeClaimCalls       []clients.ComputeClaimRecoveryClaimInput
	computeClaimKeys        []string
	beforeComputeClaimProof func()
	beforeComputeClaim      func()
	mutateCompute           func(*clients.ComputeAllocation)
	mutateStorage           func(*clients.StorageVolume)
	computeIDs              []string
	computeInputs           []clients.ComputeAllocationInput
	computeCreateKeys       []string
	storageIDs              []string
	storageCreateKeys       []string
	storageInputs           []clients.StorageVolumeInput
	storageCreateErr        error
	computeSync             clients.ComputeAllocation
	storageSync             clients.StorageVolume
	storageSyncErr          error
	computeRenew            clients.ComputeAllocation
	storageRenew            clients.StorageVolume
	computeRenewErr         error
	storageRenewErr         error
	computeRenewKeys        []string
	storageRenewKeys        []string
	afterRuntime            func()
}

func (f *monthlyFabric) ComputeClaimRecoveryProof(_ context.Context, input clients.ComputeClaimRecoveryInput) (clients.ComputeClaimRecoveryProof, error) {
	if f.beforeComputeClaimProof != nil {
		f.beforeComputeClaimProof()
	}
	*f.events = append(*f.events, "fabric.compute-claim.proof")
	f.computeClaimInputs = append(f.computeClaimInputs, input)
	if f.computeClaimProofFn != nil {
		return f.computeClaimProofFn(input)
	}
	return f.computeClaimProof, f.computeClaimProofErr
}

func (f *monthlyFabric) ComputeProviderTruth(_ context.Context, input clients.ComputeClaimRecoveryInput) (clients.ComputeProviderTruth, error) {
	*f.events = append(*f.events, "fabric.compute-provider-truth")
	f.computeProviderInputs = append(f.computeProviderInputs, input)
	if f.computeProviderTruthFn != nil {
		return f.computeProviderTruthFn(input)
	}
	if f.computeProviderTruth != nil {
		return *f.computeProviderTruth, f.computeProviderTruthErr
	}
	// Preserve the legacy fake's observation fields for existing assertions.
	// The explicit ComputeProviderTruth fixtures above verify the production GET path.
	*f.events = append(*f.events, "fabric.compute-claim.proof")
	f.computeClaimInputs = append(f.computeClaimInputs, input)
	if f.beforeComputeClaimProof != nil {
		f.beforeComputeClaimProof()
	}
	proof, proofErr := f.computeClaimProof, f.computeClaimProofErr
	if f.computeClaimProofFn != nil {
		proof, proofErr = f.computeClaimProofFn(input)
	}
	state, computeState := "unknown", "unknown"
	if proofErr == nil && proof.Eligible && proof.Reason == "none" {
		state, computeState = "ready", "ready"
	}
	return clients.ComputeProviderTruth{
		SchemaVersion: 1, State: state, ComputeState: computeState, StorageState: "unknown",
		NodeOwnershipState: proof.NodeOwnershipState, CVMOwnershipState: proof.CVMOwnershipState, Proof: &proof,
	}, proofErr
}

func (f *monthlyFabric) ComputeClaimRecoveryIdentityEvidence(_ context.Context, input clients.ComputeClaimRecoveryClaimInput) (*clients.ComputeClaimIdentityEvidence, error) {
	return &clients.ComputeClaimIdentityEvidence{
		BindingClassification: "current",
		BindingDigest:         strings.Repeat("b", 64),
		Checks: []clients.ComputeClaimIdentityCheck{
			{Field: "binding.present", Matches: true, Expected: "present", Actual: "present"},
			{Field: "binding.valid", Matches: true, Expected: "valid", Actual: "valid"},
			{Field: "binding.launchOperationId", Matches: true, Expected: input.LaunchOperationID, Actual: input.LaunchOperationID},
			{Field: "binding.idempotencyKey", Matches: true, Expected: input.LaunchOperationID + ":compute", Actual: input.LaunchOperationID + ":compute"},
		},
		MutationLedger: "absent",
	}, nil
}

func (f *monthlyFabric) ClaimComputeRecovery(_ context.Context, input clients.ComputeClaimRecoveryClaimInput, key string) (clients.ComputeClaimRecoveryProof, error) {
	if f.beforeComputeClaim != nil {
		f.beforeComputeClaim()
	}
	*f.events = append(*f.events, "fabric.compute-claim.claim")
	f.computeClaimCalls = append(f.computeClaimCalls, input)
	f.computeClaimKeys = append(f.computeClaimKeys, key)
	if f.computeClaimResult != nil {
		return *f.computeClaimResult, f.computeClaimErr
	}
	return f.computeClaimProof, f.computeClaimProofErr
}

func (f *monthlyFabric) CreateWorkspaceRuntime(ctx context.Context, input clients.WorkspaceRuntimeInput, key string) (clients.WorkspaceRuntime, error) {
	runtime, err := f.fakeFabricClient.CreateWorkspaceRuntime(ctx, input, key)
	if f.afterRuntime != nil {
		after := f.afterRuntime
		f.afterRuntime = nil
		after()
	}
	return runtime, err
}

func (f *monthlyFabric) MonthlyPreflight(_ context.Context, input clients.MonthlyPreflightInput) (clients.MonthlyPreflight, error) {
	*f.events = append(*f.events, "fabric.monthly.preflight")
	f.preflightInputs = append(f.preflightInputs, input)
	if len(f.preflightResults) > 0 {
		result := f.preflightResults[0]
		f.preflightResults = f.preflightResults[1:]
		return result, f.preflightErr
	}
	if f.preflightResult != nil {
		return *f.preflightResult, f.preflightErr
	}
	requestIDs := map[string]string{"quota": "quota-request", "price": "price-request"}
	if input.ResourceType == "compute" {
		requestIDs = map[string]string{"nodePool": "node-pool-request", "subnets": "subnets-request", "availability": "availability-request"}
	}
	result := clients.MonthlyPreflight{
		ResourceType: input.ResourceType, PackageID: input.PackageID, SizeGB: input.SizeGB, Zone: input.Zone,
		Available: true, ChargeType: "PREPAID", PeriodMonths: 1, RenewFlag: "NOTIFY_AND_MANUAL_RENEW",
		ProviderPriceCNY: 12.34, ProviderRequestIDs: requestIDs,
	}
	if input.ResourceType == "compute" {
		setMonthlyPreflightNodePoolID(&result, "np-"+input.PackageID)
	}
	return result, f.preflightErr
}

func setMonthlyPreflightNodePoolID(result *clients.MonthlyPreflight, nodePoolID string) {
	field := reflect.ValueOf(result).Elem().FieldByName("NodePoolID")
	if field.IsValid() && field.CanSet() {
		field.SetString(nodePoolID)
	}
}

func monthlyPreflightResult(input clients.MonthlyPreflightInput, nodePoolID string) clients.MonthlyPreflight {
	requestIDs := map[string]string{"quota": "quota-request", "price": "price-request"}
	if input.ResourceType == "compute" {
		requestIDs = map[string]string{"nodePool": "node-pool-request", "subnets": "subnets-request", "availability": "availability-request"}
	}
	result := clients.MonthlyPreflight{
		ResourceType: input.ResourceType, PackageID: input.PackageID, SizeGB: input.SizeGB, Zone: input.Zone,
		Available: true, ChargeType: "PREPAID", PeriodMonths: 1, RenewFlag: "NOTIFY_AND_MANUAL_RENEW",
		ProviderPriceCNY: 12.34, ProviderRequestIDs: requestIDs,
	}
	setMonthlyPreflightNodePoolID(&result, nodePoolID)
	return result
}

func (f *monthlyFabric) MonthlyProviderTruth(_ context.Context, _, _ string) (clients.MonthlyProviderTruth, error) {
	*f.events = append(*f.events, "fabric.monthly-provider-truth")
	if f.providerTruth == nil {
		return clients.MonthlyProviderTruth{}, f.providerTruthErr
	}
	return *f.providerTruth, f.providerTruthErr
}

func (f *monthlyFabric) WorkspaceActivationTruth(_ context.Context, input clients.WorkspaceActivationTruthInput) (clients.WorkspaceActivationTruth, error) {
	*f.events = append(*f.events, "fabric.workspace-activation-truth")
	f.activationTruthInputs = append(f.activationTruthInputs, input)
	if f.activationTruth != nil {
		return *f.activationTruth, f.activationTruthErr
	}
	return clients.WorkspaceActivationTruth{
		SchemaVersion: 1, Ready: true, Reason: "none", ComputeState: "ready", StorageState: "ready",
		Compute:    clients.ComputeAllocation{ID: input.ComputeAllocationID, OperationID: input.ComputeOperationID},
		Storage:    clients.StorageVolume{ID: input.StorageVolumeID, OperationID: input.StorageOperationID},
		Attachment: clients.StorageAttachment{ID: input.AttachmentID, OperationID: input.AttachmentOperationID},
		Runtime:    clients.WorkspaceActivationRuntimeTruth{ID: input.RuntimeID, OperationID: input.RuntimeOperationID, ServiceName: input.ServiceName},
		Checks:     []any{map[string]any{"name": "workspace_runtime_ready", "ok": true}},
	}, f.activationTruthErr
}

func (f *monthlyFabric) CreateComputeAllocation(_ context.Context, input clients.ComputeAllocationInput, key string) (clients.ComputeAllocation, error) {
	*f.events = append(*f.events, "fabric.compute.prepare")
	f.computeIDs = append(f.computeIDs, input.ID)
	f.computeInputs = append(f.computeInputs, input)
	f.computeCreateKeys = append(f.computeCreateKeys, key)
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

func (f *monthlyFabric) CreateStorageVolume(_ context.Context, input clients.StorageVolumeInput, key string) (clients.StorageVolume, error) {
	if input.ExpectedRecoveryState == "storage_attempt_unknown" {
		return clients.StorageVolume{ID: input.ID}, errors.New("storage_recovery_expectation_invalid")
	}
	*f.events = append(*f.events, "fabric.storage.prepare")
	f.storageIDs = append(f.storageIDs, input.ID)
	f.storageCreateKeys = append(f.storageCreateKeys, key)
	f.storageInputs = append(f.storageInputs, input)
	if f.storageCreateErr != nil {
		return clients.StorageVolume{ID: input.ID}, f.storageCreateErr
	}
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
	if f.computeCleanupStarted {
		result := f.computeCleanupSync
		if result.ID == "" {
			result.ID = id
		}
		if result.Status == "" {
			result.Status = "destroyed"
		}
		if isTerminalResourceStatus(result.Status) || result.Status == "stopped" {
			f.computeCleanupStarted = false
			f.computeDestroyed = true
		}
		return result, f.computeCleanupSyncErr
	}
	if f.computeDestroyed {
		return clients.ComputeAllocation{ID: id, AccountID: "acct-monthly", WorkspaceID: "workspace-monthly", Status: "external_deleted"}, nil
	}
	result := f.computeSync
	if result.ID == "" {
		result.ID = id
	}
	return result, f.syncErr
}

func (f *monthlyFabric) GetComputeAllocation(_ context.Context, id string) (clients.ComputeAllocation, error) {
	*f.events = append(*f.events, "fabric.compute.get")
	result := f.computeSync
	if len(f.computeReadResults) > 0 {
		result = f.computeReadResults[0]
		f.computeReadResults = f.computeReadResults[1:]
	}
	if result.ID == "" {
		result.ID = id
	}
	if len(f.computeReadErrors) > 0 {
		err := f.computeReadErrors[0]
		f.computeReadErrors = f.computeReadErrors[1:]
		return result, err
	}
	return result, f.syncErr
}

func (f *monthlyFabric) SyncStorageVolume(_ context.Context, id string) (clients.StorageVolume, error) {
	*f.events = append(*f.events, "fabric.storage.sync")
	result := f.storageSync
	if result.ID == "" {
		result.ID = id
	}
	if f.storageSyncErr != nil {
		return result, f.storageSyncErr
	}
	return result, f.syncErr
}

func (f *monthlyFabric) GetStorageVolume(_ context.Context, id string) (clients.StorageVolume, error) {
	*f.events = append(*f.events, "fabric.storage.get")
	result := f.storageSync
	if result.ID == "" {
		result.ID = id
	}
	if f.storageSyncErr != nil {
		return result, f.storageSyncErr
	}
	return result, f.syncErr
}

func TestReadMonthlyStorageRequiresDedicatedReadCapability(t *testing.T) {
	calls := []string{}
	service := controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{calls: &calls}, &testSub2APIClient{})
	_, err := service.ReadMonthlyStorage(context.Background(), "storage-alpha")
	if err == nil || err.Error() != "fabric_storage_volume_read_unavailable" {
		t.Fatalf("missing storage reader err=%v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("missing storage reader fell back to Fabric mutation-capable call: %#v", calls)
	}
}

func (f *monthlyFabric) RenewComputeAllocation(_ context.Context, id, key string) (clients.ComputeAllocation, error) {
	*f.events = append(*f.events, "fabric.compute.renew")
	f.computeRenewKeys = append(f.computeRenewKeys, key)
	if f.computeDestroyed {
		return clients.ComputeAllocation{ID: id, AccountID: "acct-monthly", WorkspaceID: "workspace-monthly", Status: "external_deleted"}, errors.New("compute already destroyed")
	}
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
	f.computeCleanupStarted = true
	return clients.ComputeAllocation{ID: id, Status: firstNonEmpty(f.computeCleanupStatus, "destroyed")}, nil
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
	seedTenantMember(t, app.tables, "acct-monthly", "org-monthly", "usr-monthly-owner", "monthly-owner@example.com")
	return app, controlplane.NewService(ledger, fabric, sub2API), sub2API, fabric, ledger, events
}

func monthlyActiveResource(resourceType, id string, paidThrough time.Time) map[string]any {
	status, providerID := "running", "ins-"+id
	if resourceType == "storage" {
		status, providerID = "available", "disk-"+id
	}
	row := map[string]any{
		"id": id, "accountId": "acct-monthly", "workspaceId": "workspace-monthly", "packageId": "basic", "status": status,
		"provider": "tencent-tke", "providerResourceId": providerID, "providerRequestId": "req-" + id,
		"billingStatus": "active", "billingOperationId": "purchase-" + id, "billingOperationStartedAt": paidThrough.AddDate(0, -1, 0).Format(time.RFC3339),
		"sub2apiRedeemCode": "opl:test:purchase-" + id + ":charge:v1", "pricingVersion": pricingCatalogVersion,
		"monthlyPriceCnyCents": int64(35000), "chargeUsdMicros": int64(50_000_000),
		"billingAnchorDay": int64(paidThrough.Day()), "periodStart": paidThrough.AddDate(0, -1, 0).Format(time.RFC3339),
		"paidThrough": paidThrough.Format(time.RFC3339), "autoRenew": true, "lastReceiptId": "receipt-purchase-" + id,
		"postChargeBalanceKnown": true, "postChargeBalanceUsdMicros": int64(100_000_000),
		"chargeType": "PREPAID", "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": paidThrough.Format(time.RFC3339), "zone": "ap-shanghai-2",
		"providerData": map[string]any{"chargeType": "PREPAID", "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": paidThrough.Format(time.RFC3339), "zone": "ap-shanghai-2"},
	}
	if resourceType == "storage" {
		row["sizeGb"], row["diskType"], row["cbsStatus"] = 10, "CLOUD_PREMIUM", "UNATTACHED"
		row["monthlyPriceCnyCents"], row["chargeUsdMicros"] = int64(1800), int64(2_580_000)
	} else {
		row["instanceId"], row["cvmInstanceId"], row["instanceType"] = providerID, providerID, "S5.MEDIUM4"
		row["providerData"].(map[string]any)["instanceType"] = "S5.MEDIUM4"
	}
	return row
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
			receipt := customerBillingReceipt()
			receipt.AccountID = tc.accountID
			ledger := scopedReceiptLedger{receipt: receipt}
			server := NewServer(newTestService(ledger, &fakeFabricClient{}))
			response := requestWithSession(t, server, tenantAdminSessionForTest(t, server), http.MethodGet, "/api/billing/receipts/receipt-monthly", "")
			if response.Code != tc.wantStatus {
				t.Fatalf("receipt status = %d, want %d: %s", response.Code, tc.wantStatus, response.Body.String())
			}
			if tc.wantStatus == http.StatusOK && (!strings.Contains(response.Body.String(), `"receiptId":"receipt-monthly"`) || strings.Contains(response.Body.String(), `"accountId"`)) {
				t.Fatalf("owned receipt response = %s", response.Body.String())
			}
		})
	}
}
