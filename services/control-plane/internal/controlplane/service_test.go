package controlplane

import (
	"context"
	"testing"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/domain"
)

type providerAcceptanceReceiptLedger struct {
	record func(clients.ReceiptInput) clients.Receipt
}

func (l providerAcceptanceReceiptLedger) RecordReceipt(_ context.Context, input clients.ReceiptInput, _ string) (clients.Receipt, error) {
	return l.record(input), nil
}

func (providerAcceptanceReceiptLedger) Receipt(context.Context, string) (clients.Receipt, error) {
	return clients.Receipt{}, nil
}

func (providerAcceptanceReceiptLedger) RecordReconciliation(context.Context, clients.ReconciliationInput, string) (clients.ReconciliationResult, error) {
	return clients.ReconciliationResult{}, nil
}

func TestRecordProviderAcceptanceReceiptRejectsMalformedLedgerResponse(t *testing.T) {
	workspace := domain.WorkspaceProjection{
		ID:        "workspace-alpha",
		AccountID: "account-alpha",
		RuntimeID: "runtime-alpha",
		URL:       "https://workspace.example.test/w/workspace-alpha/",
	}
	for _, test := range []struct {
		name   string
		mutate func(*clients.Receipt)
	}{
		{name: "missing receipt ID", mutate: func(receipt *clients.Receipt) { receipt.ReceiptID = "" }},
		{name: "type mismatch", mutate: func(receipt *clients.Receipt) { receipt.Type = "workspace.created" }},
		{name: "status mismatch", mutate: func(receipt *clients.Receipt) { receipt.Status = "running" }},
		{name: "surface mismatch", mutate: func(receipt *clients.Receipt) { receipt.Surface = "control_plane" }},
		{name: "account mismatch", mutate: func(receipt *clients.Receipt) { receipt.AccountID = "account-other" }},
		{name: "workspace mismatch", mutate: func(receipt *clients.Receipt) { receipt.WorkspaceID = "workspace-other" }},
		{name: "job mismatch", mutate: func(receipt *clients.Receipt) { receipt.JobID = "runtime-other" }},
		{name: "execution mismatch", mutate: func(receipt *clients.Receipt) {
			receipt.Execution = map[string]any{"providerRequestId": "runtime-other"}
		}},
		{name: "output refs mismatch", mutate: func(receipt *clients.Receipt) {
			receipt.OutputRefs = map[string]any{"redactedUrl": "https://workspace.example.test/w/workspace-other/"}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ledger := providerAcceptanceReceiptLedger{record: func(input clients.ReceiptInput) clients.Receipt {
				receipt := clients.Receipt{ReceiptInput: input, ReceiptID: "receipt-alpha"}
				test.mutate(&receipt)
				return receipt
			}}
			actual, err := NewService(ledger, nil).RecordProviderAcceptanceReceipt(context.Background(), workspace, "provider-acceptance-alpha")
			if err == nil {
				t.Fatalf("malformed Ledger receipt accepted: %#v", actual)
			}
			if actual != workspace {
				t.Fatalf("Workspace mutated after malformed Ledger receipt: %#v", actual)
			}
		})
	}
}
