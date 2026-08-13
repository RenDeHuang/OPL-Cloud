package fabric

import (
	"context"
	"fmt"
	"sync"
	"time"
)

func (s *Service) ProviderFactsBatch(ctx context.Context, input ProviderFactsBatchInput) (ProviderFactsBatch, error) {
	result := ProviderFactsBatch{Items: make([]ProviderFact, len(input.Items))}
	if len(input.Items) == 0 || len(input.Items) > 50 {
		return ProviderFactsBatch{}, fmt.Errorf("provider_facts_batch_invalid")
	}
	batchCtx, cancel := context.WithTimeout(ctx, providerFactsBatchTimeout)
	defer cancel()
	type job struct {
		index int
		item  ProviderFactInput
	}
	jobs := make(chan job, len(input.Items))
	for index, item := range input.Items {
		result.Items[index] = ProviderFact{AccountID: item.AccountID, WorkspaceID: item.WorkspaceID, ResourceType: item.ResourceType, ResourceID: item.ResourceID}
		jobs <- job{index: index, item: item}
	}
	close(jobs)
	workers := providerFactsBatchWorkerCount
	if len(input.Items) < workers {
		workers = len(input.Items)
	}
	var wait sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for {
				select {
				case <-batchCtx.Done():
					return
				case next, ok := <-jobs:
					if !ok {
						return
					}
					result.Items[next.index] = s.providerFact(batchCtx, next.item)
				}
			}
		}()
	}
	wait.Wait()
	if batchCtx.Err() != nil {
		for index := range result.Items {
			if !result.Items[index].Available && result.Items[index].ErrorCode == "" {
				result.Items[index].ErrorCode = "provider_facts_timeout"
			}
		}
	}
	return result, nil
}

func (s *Service) RuntimeHealthSummary(ctx context.Context) (RuntimeHealthSummary, error) {
	provider, ok := s.provider.(runtimeHealthSummaryProvider)
	if !ok {
		return RuntimeHealthSummary{}, ErrRuntimeHealthSummaryUnavailable
	}
	readCtx, cancel := context.WithTimeout(ctx, runtimeHealthSummaryTimeout)
	defer cancel()
	summary, err := provider.RuntimeHealthSummary(readCtx)
	if err != nil {
		return RuntimeHealthSummary{}, fmt.Errorf("%w: %v", ErrRuntimeHealthSummaryUnavailable, err)
	}
	if summary.Total < 0 || summary.Ready < 0 || summary.Unready < 0 || summary.Ready+summary.Unready != summary.Total {
		return RuntimeHealthSummary{}, fmt.Errorf("%w: invalid_counts", ErrRuntimeHealthSummaryUnavailable)
	}
	return summary, nil
}

func (s *Service) providerFact(ctx context.Context, input ProviderFactInput) ProviderFact {
	result := ProviderFact{AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, ResourceType: input.ResourceType, ResourceID: input.ResourceID}
	if input.AccountID == "" || input.WorkspaceID == "" || input.ResourceID == "" {
		result.ErrorCode = "provider_fact_identity_required"
		return result
	}
	s.mu.Lock()
	compute := s.computes[input.ResourceID]
	storage := s.volumes[input.ResourceID]
	attachment := s.attachments[input.ResourceID]
	attachmentCompute := s.computes[attachment.ComputeID]
	attachmentStorage := s.volumes[attachment.VolumeID]
	s.mu.Unlock()
	var facts ProviderResourceFacts
	var err error
	if input.ResourceType != "compute" && input.ResourceType != "storage" && input.ResourceType != "attachment" && input.ResourceType != "runtime" {
		result.ErrorCode = "provider_fact_resource_type_invalid"
		return result
	}
	provider, ok := s.provider.(providerFactsReader)
	if !ok {
		result.ErrorCode = "provider_facts_unavailable"
		return result
	}
	switch input.ResourceType {
	case "compute":
		if compute.ID == "" || compute.AccountID != input.AccountID || compute.WorkspaceID != input.WorkspaceID {
			result.ErrorCode = "provider_fact_identity_mismatch"
			return result
		}
		facts, err = provider.ReadComputeProviderFacts(ctx, compute)
	case "storage":
		if storage.ID == "" || storage.AccountID != input.AccountID || storage.WorkspaceID != input.WorkspaceID {
			result.ErrorCode = "provider_fact_identity_mismatch"
			return result
		}
		facts, err = provider.ReadStorageProviderFacts(ctx, storage)
	case "attachment":
		if attachment.ID == "" || attachment.WorkspaceID != input.WorkspaceID || attachmentCompute.AccountID != input.AccountID || attachmentCompute.WorkspaceID != input.WorkspaceID || attachmentStorage.AccountID != input.AccountID || attachmentStorage.WorkspaceID != input.WorkspaceID {
			result.ErrorCode = "provider_fact_identity_mismatch"
			return result
		}
		facts, err = provider.ReadStorageAttachmentProviderFacts(ctx, attachment, attachmentCompute, attachmentStorage)
	case "runtime":
		var runtime WorkspaceRuntime
		runtime, err = s.WorkspaceRuntimeStatus(ctx, input.WorkspaceID)
		if err == nil && (runtime.ID != input.ResourceID || runtime.WorkspaceID != input.WorkspaceID) {
			result.ErrorCode = "provider_fact_identity_mismatch"
			return result
		}
		facts = provider.WorkspaceRuntimeProviderFacts(runtime)
	}
	if err != nil {
		result.ErrorCode = errorCode(err)
		return result
	}
	facts.LastReadAt = s.now().Format(time.RFC3339Nano)
	result.Available, result.Facts = true, facts
	return result
}
