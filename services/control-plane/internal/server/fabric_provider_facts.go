package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

var errProviderFactsInvalid = errors.New("fabric_provider_facts_invalid")

func providerFactKey(input clients.ProviderFactInput) string {
	return input.AccountID + "\x00" + input.WorkspaceID + "\x00" + input.ResourceType + "\x00" + input.ResourceID
}

func providerFactResultKey(fact clients.ProviderFact) string {
	return fact.AccountID + "\x00" + fact.WorkspaceID + "\x00" + fact.ResourceType + "\x00" + fact.ResourceID
}

func readProviderFacts(ctx context.Context, service *controlplane.Service, inputs []clients.ProviderFactInput) (map[string]clients.ProviderFact, error) {
	result := make(map[string]clients.ProviderFact, len(inputs))
	for start := 0; start < len(inputs); start += 50 {
		end := start + 50
		if end > len(inputs) {
			end = len(inputs)
		}
		requested := make(map[string]struct{}, end-start)
		for _, input := range inputs[start:end] {
			key := providerFactKey(input)
			if input.AccountID == "" || input.WorkspaceID == "" || input.ResourceType == "" || input.ResourceID == "" {
				return nil, errProviderFactsInvalid
			}
			if _, duplicate := requested[key]; duplicate {
				return nil, errProviderFactsInvalid
			}
			requested[key] = struct{}{}
		}
		batch, err := service.ProviderFactsBatch(ctx, clients.ProviderFactsBatchInput{Items: inputs[start:end]})
		if err != nil {
			return nil, err
		}
		for _, fact := range batch.Items {
			key := providerFactResultKey(fact)
			if _, ok := requested[key]; !ok {
				return nil, errProviderFactsInvalid
			}
			if _, duplicate := result[key]; duplicate {
				return nil, errProviderFactsInvalid
			}
			result[key] = fact
		}
		for key := range requested {
			if _, ok := result[key]; !ok {
				return nil, errProviderFactsInvalid
			}
		}
	}
	return result, nil
}

func readProviderFact(ctx context.Context, service *controlplane.Service, input clients.ProviderFactInput) (clients.ProviderFact, error) {
	facts, err := readProviderFacts(ctx, service, []clients.ProviderFactInput{input})
	if err != nil {
		return clients.ProviderFact{}, err
	}
	return facts[providerFactKey(input)], nil
}

func uniqueProviderFactInputs(inputs []clients.ProviderFactInput) []clients.ProviderFactInput {
	result := make([]clients.ProviderFactInput, 0, len(inputs))
	seen := make(map[string]struct{}, len(inputs))
	for _, input := range inputs {
		key := providerFactKey(input)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, input)
	}
	return result
}

func providerFactEvidence(fact clients.ProviderFact) map[string]any {
	return structToMap(fact)
}

func providerFactFromEvidence(value map[string]any) (clients.ProviderFact, bool) {
	if len(value) == 0 {
		return clients.ProviderFact{}, false
	}
	var fact clients.ProviderFact
	payload, err := json.Marshal(value)
	if err != nil || json.Unmarshal(payload, &fact) != nil {
		return clients.ProviderFact{}, false
	}
	return fact, true
}

func providerFactCovers(input clients.ProviderFactInput, fact clients.ProviderFact, expectedProviderID string, minimumExpiry time.Time) bool {
	if providerFactResultKey(fact) != providerFactKey(input) || !fact.Available || strings.TrimSpace(fact.ErrorCode) != "" ||
		strings.TrimSpace(fact.Facts.ProviderID) == "" || strings.TrimSpace(fact.Facts.PackageOrSpec) == "" ||
		strings.TrimSpace(fact.Facts.Zone) == "" || !providerFactStatusUsable(input.ResourceType, fact.Facts.Status) || strings.TrimSpace(fact.Facts.LastReadAt) == "" {
		return false
	}
	if expectedProviderID != "" && fact.Facts.ProviderID != expectedProviderID {
		return false
	}
	expiresAt, err := time.Parse(time.RFC3339, fact.Facts.ExpiresAt)
	if err != nil || expiresAt.UTC().Before(minimumExpiry.UTC()) {
		return false
	}
	_, err = time.Parse(time.RFC3339Nano, fact.Facts.LastReadAt)
	return err == nil
}

func providerFactStatusUsable(_ string, status string) bool {
	value := strings.ToLower(strings.TrimSpace(status))
	if value == "" {
		return false
	}
	switch value {
	case "external_deleted", "deleted", "missing", "not_found":
		return false
	default:
		return true
	}
}

func providerFactConfirmedAbsent(input clients.ProviderFactInput, fact clients.ProviderFact) bool {
	if providerFactResultKey(fact) != providerFactKey(input) || !fact.Available || strings.TrimSpace(fact.ErrorCode) != "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(fact.Facts.Status)) {
	case "external_deleted", "deleted", "missing", "not_found":
		_, err := time.Parse(time.RFC3339Nano, fact.Facts.LastReadAt)
		return err == nil
	default:
		return false
	}
}

func projectProviderFact(row map[string]any, fact clients.ProviderFact) map[string]any {
	result := cloneMap(row)
	if fact.Facts.ProviderID != "" {
		result["providerResourceId"] = fact.Facts.ProviderID
	}
	result["providerStatus"] = fact.Facts.Status
	result["lastProviderSyncAt"] = fact.Facts.LastReadAt
	result["lastProviderSyncError"] = ""
	return result
}
