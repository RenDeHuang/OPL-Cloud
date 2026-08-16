package server

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

type workspaceLaunchDebitReadbackStub struct {
	*testSub2APIClient
	mu              sync.Mutex
	history         map[string]clients.Sub2APIBalanceHistoryEntry
	historyErr      error
	historySequence []map[string]clients.Sub2APIBalanceHistoryEntry
	balance         clients.Sub2APIBalance
	balanceErr      error
	balanceErrors   []error
	chargeCalls     int
}

func (s *workspaceLaunchDebitReadbackStub) FinancialBalanceHistoryByCodes(_ context.Context, _ int64, codes []string) (map[string]clients.Sub2APIBalanceHistoryEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.historyErr != nil {
		return nil, s.historyErr
	}
	history := s.history
	if len(s.historySequence) > 0 {
		history = s.historySequence[0]
		s.historySequence = s.historySequence[1:]
	}
	matches := make(map[string]clients.Sub2APIBalanceHistoryEntry, len(codes))
	for _, code := range codes {
		if entry, ok := history[code]; ok {
			matches[code] = entry
		}
	}
	return matches, nil
}

func (s *workspaceLaunchDebitReadbackStub) Balance(context.Context, int64) (clients.Sub2APIBalance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	return s.balance, nil
}

func (s *workspaceLaunchDebitReadbackStub) Charge(_ context.Context, input clients.Sub2APIChargeInput) (clients.Sub2APICharge, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.chargeCalls++
	return clients.Sub2APICharge{Code: input.Code, UserID: input.UserID, ChargeUSDMicros: input.ChargeUSDMicros, Status: "used"}, nil
}

func TestWorkspaceLaunchDebitAuthoritativeReadbackClassification(t *testing.T) {
	operation, err := newWorkspaceLaunchReconcileOperation(workspaceLaunchUnitCommand())
	if err != nil {
		t.Fatal(err)
	}
	operation.Stage = "debit"
	debitAttempt := operation.Attempts["debit"]
	debitAttempt.Attempted, debitAttempt.Status = 1, "reserved"
	debitAttempt.IdempotencyKey = workspaceLaunchStageIdempotencyKey(operation, 1)
	operation.Attempts["debit"] = debitAttempt

	userID := operation.int64Fact("sub2apiUserId")
	code := operation.stringFact("sub2apiRedeemCode")
	charge := operation.int64Fact("totalChargeUsdMicros")
	usedAt := time.Date(2026, 8, 16, 1, 2, 3, 0, time.UTC)
	exactEntry := clients.Sub2APIBalanceHistoryEntry{
		Code: code, Type: "balance", ValueUSDMicros: -charge, Status: "used", UsedBy: &userID, UsedAt: &usedAt, CreatedAt: usedAt,
	}
	exactBalance := clients.Sub2APIBalance{UserID: userID, USDMicros: 47_420_000, Status: "active"}

	tests := []struct {
		name      string
		configure func(*workspaceLaunchDebitReadbackStub)
		wantState string
		wantErr   bool
		wantFacts bool
	}{
		{
			name: "history immediately visible",
			configure: func(stub *workspaceLaunchDebitReadbackStub) {
				stub.history[code] = exactEntry
			},
			wantState: workspaceLaunchStageReady, wantFacts: true,
		},
		{
			name:      "history visibility delayed",
			configure: func(*workspaceLaunchDebitReadbackStub) {},
			wantState: workspaceLaunchStagePending,
		},
		{
			name: "history authority read failed",
			configure: func(stub *workspaceLaunchDebitReadbackStub) {
				stub.historyErr = errors.New("history authority unavailable")
			},
			wantState: workspaceLaunchStageUnknown, wantErr: true,
		},
		{
			name: "used timestamp delayed",
			configure: func(stub *workspaceLaunchDebitReadbackStub) {
				entry := exactEntry
				entry.UsedAt = nil
				stub.history[code] = entry
			},
			wantState: workspaceLaunchStagePending,
		},
		{
			name: "balance projection read failed",
			configure: func(stub *workspaceLaunchDebitReadbackStub) {
				stub.history[code] = exactEntry
				stub.balanceErr = errors.New("balance projection unavailable")
			},
			wantState: workspaceLaunchStagePending,
		},
		{
			name: "user identity conflict",
			configure: func(stub *workspaceLaunchDebitReadbackStub) {
				entry := exactEntry
				otherUserID := userID + 1
				entry.UsedBy = &otherUserID
				stub.history[code] = entry
			},
			wantState: workspaceLaunchStageUnknown, wantErr: true,
		},
		{
			name: "amount conflict",
			configure: func(stub *workspaceLaunchDebitReadbackStub) {
				entry := exactEntry
				entry.ValueUSDMicros--
				stub.history[code] = entry
			},
			wantState: workspaceLaunchStageUnknown, wantErr: true,
		},
		{
			name: "status conflict",
			configure: func(stub *workspaceLaunchDebitReadbackStub) {
				entry := exactEntry
				entry.Status = "unused"
				stub.history[code] = entry
			},
			wantState: workspaceLaunchStageUnknown, wantErr: true,
		},
		{
			name: "balance identity conflict",
			configure: func(stub *workspaceLaunchDebitReadbackStub) {
				stub.history[code] = exactEntry
				stub.balance.UserID++
			},
			wantState: workspaceLaunchStageUnknown,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stub := &workspaceLaunchDebitReadbackStub{
				testSub2APIClient: &testSub2APIClient{charges: map[string]int64{}},
				history:           map[string]clients.Sub2APIBalanceHistoryEntry{}, balance: exactBalance,
			}
			tc.configure(stub)
			service := controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}, stub)
			adapter := &controlPlaneWorkspaceLaunchStageAdapter{app: &controlPlaneServer{}, service: service}

			observation, readErr := adapter.readWorkspaceLaunchDebit(context.Background(), operation)
			if observation.State != tc.wantState || (readErr != nil) != tc.wantErr || (len(observation.Facts) > 0) != tc.wantFacts {
				t.Fatalf("readback state=%q facts=%#v err=%v, want state=%q facts=%t err=%t", observation.State, observation.Facts, readErr, tc.wantState, tc.wantFacts, tc.wantErr)
			}
		})
	}
}

func TestWorkspaceLaunchDebitMissingBeforeReservationRemainsAbsent(t *testing.T) {
	operation, err := newWorkspaceLaunchReconcileOperation(workspaceLaunchUnitCommand())
	if err != nil {
		t.Fatal(err)
	}
	operation.Stage = "debit"
	stub := &workspaceLaunchDebitReadbackStub{
		testSub2APIClient: &testSub2APIClient{charges: map[string]int64{}},
		history:           map[string]clients.Sub2APIBalanceHistoryEntry{},
	}
	service := controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}, stub)
	adapter := &controlPlaneWorkspaceLaunchStageAdapter{app: &controlPlaneServer{}, service: service}

	observation, readErr := adapter.readWorkspaceLaunchDebit(context.Background(), operation)
	if readErr != nil || observation.State != workspaceLaunchStageAbsent {
		t.Fatalf("pre-reservation readback state=%q err=%v, want absent", observation.State, readErr)
	}
}

func TestWorkspaceLaunchDebitConvergesWithBoundedReadOnlyContinuation(t *testing.T) {
	t.Setenv("OPL_TENCENT_ZONE", "na-siliconvalley-1")
	command := workspaceLaunchUnitCommand()
	userID, charge := command.Sub2APIUserID, command.TotalChargeUSDMicros
	usedAt := time.Date(2026, 8, 16, 2, 3, 4, 0, time.UTC)
	code := monthlyRedeemCode(monthlyEnvironment(), command.OperationID)
	exact := clients.Sub2APIBalanceHistoryEntry{
		Code: code, Type: "balance", ValueUSDMicros: -charge, Status: "used", UsedBy: &userID, UsedAt: &usedAt, CreatedAt: usedAt,
	}
	withoutUsedAt := exact
	withoutUsedAt.UsedAt = nil
	empty := map[string]clients.Sub2APIBalanceHistoryEntry{}
	exactHistory := map[string]clients.Sub2APIBalanceHistoryEntry{code: exact}
	delayedUsedAtHistory := map[string]clients.Sub2APIBalanceHistoryEntry{code: withoutUsedAt}

	tests := []struct {
		name          string
		postMutation  map[string]clients.Sub2APIBalanceHistoryEntry
		balanceErrors []error
	}{
		{name: "history delayed", postMutation: empty},
		{name: "used timestamp delayed", postMutation: delayedUsedAtHistory},
		{name: "balance read delayed", postMutation: exactHistory, balanceErrors: []error{errors.New("balance temporarily unavailable"), nil}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			operation, err := newWorkspaceLaunchReconcileOperation(command)
			if err != nil {
				t.Fatal(err)
			}
			operation.Stage, operation.Status = "debit", "pending"
			row, err := workspaceLaunchReconcileOperationRow(operation)
			if err != nil {
				t.Fatal(err)
			}
			store := &workspaceLaunchUnitStore{row: row}
			stub := &workspaceLaunchDebitReadbackStub{
				testSub2APIClient: &testSub2APIClient{charges: map[string]int64{}},
				history:           exactHistory,
				historySequence:   []map[string]clients.Sub2APIBalanceHistoryEntry{empty, empty, tc.postMutation, exactHistory},
				balance:           clients.Sub2APIBalance{UserID: userID, USDMicros: command.PreChargeBalanceMicros - charge, Status: "active"},
				balanceErrors:     tc.balanceErrors,
			}
			service := controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}, stub)
			adapter := &controlPlaneWorkspaceLaunchStageAdapter{app: &controlPlaneServer{}, service: service}
			reconciler := NewWorkspaceLaunchReconciler(store, adapter)

			pending, err := reconciler.Reconcile(context.Background(), operation.ID)
			if err != nil || pending.Status != "pending" || pending.Stage != "debit" || pending.Observations["debit"].State != workspaceLaunchStagePending ||
				pending.Attempts["debit"].Attempted != 1 || pending.Attempts["debit"].PendingReadbacks != 1 || stub.chargeCalls != 1 {
				t.Fatalf("post-debit pending=%s chargeCalls=%d err=%v", workspaceLaunchReconcileResultSummary(pending), stub.chargeCalls, err)
			}

			converged, err := reconciler.Reconcile(context.Background(), operation.ID)
			if err != nil || converged.Status != "pending" || converged.Stage != "ensure_compute_allocation" ||
				converged.Attempts["debit"].Attempted != 1 || converged.Attempts["debit"].Confirmed != 1 || converged.Attempts["debit"].Unknown != 0 ||
				converged.Attempts["debit"].Status != "confirmed" || stub.chargeCalls != 1 {
				t.Fatalf("converged debit=%s chargeCalls=%d err=%v", workspaceLaunchReconcileResultSummary(converged), stub.chargeCalls, err)
			}
		})
	}
}
