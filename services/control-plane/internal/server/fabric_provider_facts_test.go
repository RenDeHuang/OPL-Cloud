package server

import (
	"context"
	"errors"
	"testing"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

type providerFactsBoundaryFabric struct {
	fakeFabricClient
	items []clients.ProviderFact
}

func (f *providerFactsBoundaryFabric) ProviderFactsBatch(_ context.Context, _ clients.ProviderFactsBatchInput) (clients.ProviderFactsBatch, error) {
	return clients.ProviderFactsBatch{Items: append([]clients.ProviderFact(nil), f.items...)}, nil
}

func TestReadProviderFactsRequiresExactBatchIdentity(t *testing.T) {
	input := clients.ProviderFactInput{
		AccountID: "acct-alpha", WorkspaceID: "workspace-alpha", ResourceType: "compute", ResourceID: "compute-alpha",
	}
	matching := clients.ProviderFact{
		AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, ResourceType: input.ResourceType, ResourceID: input.ResourceID,
		Available: true, Facts: clients.ProviderResourceFacts{ProviderID: "provider-alpha", Status: "running"},
	}
	unrequested := matching
	unrequested.ResourceID = "compute-other"

	for _, test := range []struct {
		name    string
		items   []clients.ProviderFact
		wantErr bool
	}{
		{name: "exact", items: []clients.ProviderFact{matching}},
		{name: "missing", wantErr: true},
		{name: "unrequested", items: []clients.ProviderFact{unrequested}, wantErr: true},
		{name: "duplicate", items: []clients.ProviderFact{matching, matching}, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fabric := &providerFactsBoundaryFabric{items: test.items}
			service := controlplane.NewService(fakeLedgerClient{}, fabric)
			facts, err := readProviderFacts(context.Background(), service, []clients.ProviderFactInput{input})
			if test.wantErr {
				if !errors.Is(err, errProviderFactsInvalid) || facts != nil {
					t.Fatalf("facts = %#v, err = %v, want invalid response", facts, err)
				}
				return
			}
			if err != nil || len(facts) != 1 || facts[providerFactKey(input)].Facts.ProviderID != "provider-alpha" {
				t.Fatalf("facts = %#v, err = %v", facts, err)
			}
		})
	}
}
