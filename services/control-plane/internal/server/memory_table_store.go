package server

import (
	"context"
	"sync"
)

type memoryTableStore struct {
	mu          sync.Mutex
	users       controlPlaneRecordSet
	sessions    controlPlaneRecordSet
	computes    controlPlaneRecordSet
	storages    controlPlaneRecordSet
	attachments controlPlaneRecordSet
	workspaces  controlPlaneRecordSet
	wallets     controlPlaneRecordSet
	ledger      []map[string]any
	walletTx    []map[string]any
	topups      []map[string]any
	auditEvents []map[string]any
	support     controlPlaneRecordSet
}

func newMemoryTableStore() *memoryTableStore {
	return &memoryTableStore{
		users:       controlPlaneRecordSet{"usr-admin": {"id": "usr-admin", "email": "admin@medopl.cn", "accountId": "acct-admin", "role": "admin", "status": "active"}},
		sessions:    controlPlaneRecordSet{},
		computes:    controlPlaneRecordSet{},
		storages:    controlPlaneRecordSet{},
		attachments: controlPlaneRecordSet{},
		workspaces:  controlPlaneRecordSet{},
		wallets:     controlPlaneRecordSet{},
		support:     controlPlaneRecordSet{},
	}
}

func (s *memoryTableStore) ListUsers(_ context.Context, includeDeleted bool) ([]map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]map[string]any, 0, len(s.users))
	for _, row := range s.users {
		if !includeDeleted && stringValue(row["status"]) == "deleted" {
			continue
		}
		out = append(out, cloneMap(row))
	}
	return out, nil
}

func (s *memoryTableStore) SaveUser(_ context.Context, row map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.users[stringValue(row["id"])] = cloneMap(row)
	return nil
}

func (s *memoryTableStore) DeleteUser(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.users, id)
	return nil
}

func (s *memoryTableStore) ListSessions(_ context.Context) (controlPlaneRecordSet, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneStateTable(s.sessions), nil
}

func (s *memoryTableStore) SaveSession(_ context.Context, row map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[stringValue(row["id"])] = cloneMap(row)
	return nil
}

func (s *memoryTableStore) DeleteSession(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
	return nil
}

func (s *memoryTableStore) ListComputes(_ context.Context, accountID string) ([]map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return filteredRecords(s.computes, accountID)
}

func (s *memoryTableStore) SaveCompute(_ context.Context, row map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.computes[stringValue(row["id"])] = cloneMap(row)
	return nil
}

func (s *memoryTableStore) DeleteCompute(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.computes, id)
	return nil
}

func (s *memoryTableStore) ListStorages(_ context.Context, accountID string) ([]map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return filteredRecords(s.storages, accountID)
}

func (s *memoryTableStore) SaveStorage(_ context.Context, row map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.storages[stringValue(row["id"])] = cloneMap(row)
	return nil
}

func (s *memoryTableStore) DeleteStorage(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.storages, id)
	return nil
}

func (s *memoryTableStore) ListAttachments(_ context.Context, accountID string) ([]map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return filteredRecords(s.attachments, accountID)
}

func (s *memoryTableStore) SaveAttachment(_ context.Context, row map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attachments[stringValue(row["id"])] = cloneMap(row)
	return nil
}

func (s *memoryTableStore) DeleteAttachment(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.attachments, id)
	return nil
}

func (s *memoryTableStore) ListWorkspaces(_ context.Context, accountID string) ([]map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return filteredRecords(s.workspaces, accountID)
}

func (s *memoryTableStore) SaveWorkspace(_ context.Context, row map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workspaces[stringValue(row["id"])] = cloneMap(row)
	return nil
}

func (s *memoryTableStore) DeleteWorkspace(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.workspaces, id)
	return nil
}

func (s *memoryTableStore) ListWallets(_ context.Context, accountID string) ([]map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return filteredRecords(s.wallets, accountID)
}

func (s *memoryTableStore) SaveWallet(_ context.Context, row map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wallets[firstNonEmpty(stringValue(row["id"]), stringValue(row["accountId"]))] = cloneMap(row)
	return nil
}

func (s *memoryTableStore) ListLedger(_ context.Context, accountID string) ([]map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return filteredEvents(s.ledger, accountID), nil
}

func (s *memoryTableStore) SaveLedgerEntry(_ context.Context, row map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ledger = upsertProjectionByID(s.ledger, cloneMap(row))
	return nil
}

func (s *memoryTableStore) ListWalletTransactions(_ context.Context, accountID string) ([]map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return filteredEvents(s.walletTx, accountID), nil
}

func (s *memoryTableStore) SaveWalletTransaction(_ context.Context, row map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.walletTx = upsertProjectionByID(s.walletTx, cloneMap(row))
	return nil
}

func (s *memoryTableStore) ListManualTopups(_ context.Context, accountID string) ([]map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return filteredEvents(s.topups, accountID), nil
}

func (s *memoryTableStore) SaveManualTopup(_ context.Context, row map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.topups = upsertProjectionByID(s.topups, cloneMap(row))
	return nil
}

func (s *memoryTableStore) ListAuditEvents(_ context.Context, accountID string) ([]map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return filteredEvents(s.auditEvents, accountID), nil
}

func (s *memoryTableStore) SaveAuditEvent(_ context.Context, row map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.auditEvents = upsertProjectionByID(s.auditEvents, cloneMap(row))
	return nil
}

func (s *memoryTableStore) ListSupportMappings(_ context.Context, accountID string) ([]map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return filteredRecords(s.support, accountID)
}

func (s *memoryTableStore) SaveSupportMapping(_ context.Context, row map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.support[stringValue(row["id"])] = cloneMap(row)
	return nil
}
