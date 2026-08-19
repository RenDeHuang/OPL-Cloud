package fabric

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"
)

const (
	storageProvisionTimeout          = 10 * time.Minute
	computeAllocationPollInterval    = 10 * time.Second
	computeAllocationPollWindow      = 10 * time.Minute
	computeAllocationAttemptTimeout  = 30 * time.Second
	computeAllocationFinalizeTimeout = 2 * time.Minute
	providerFactsBatchTimeout        = 5 * time.Second
	providerFactsBatchWorkerCount    = 8
	readinessProviderTimeout         = 5 * time.Second
	readinessSuccessTTL              = 5 * time.Second
	runtimeHealthSummaryTimeout      = 5 * time.Second
)

type readinessRefresh struct {
	done   chan struct{}
	result map[string]any
	err    error
}

type Service struct {
	provider                         Provider
	mu                               sync.Mutex
	jobMu                            sync.Mutex
	readinessMu                      sync.Mutex
	readinessCached                  bool
	readinessResult                  map[string]any
	readinessExpiresAt               time.Time
	readinessRefresh                 *readinessRefresh
	readinessTTL                     time.Duration
	readinessTimeout                 time.Duration
	computes                         map[string]ComputeAllocation
	volumes                          map[string]StorageVolume
	attachments                      map[string]StorageAttachment
	destroying                       map[string]bool
	reconciling                      map[string]bool
	operations                       OperationStore
	now                              func() time.Time
	computeAllocationPollInterval    time.Duration
	computeAllocationPollWindow      time.Duration
	computeAllocationAttemptTimeout  time.Duration
	computeAllocationFinalizeTimeout time.Duration
}

func NewService(provider Provider) *Service {
	return NewServiceWithOperationStore(provider, NewMemoryOperationStore())
}

func NewServiceWithOperationStore(provider Provider, operations OperationStore) *Service {
	if operations == nil {
		operations = NewMemoryOperationStore()
	}
	computes, volumes, attachments, _ := replayResourceState(context.Background(), operations)
	return &Service{
		provider: provider, computes: computes, volumes: volumes, attachments: attachments,
		destroying: map[string]bool{}, reconciling: map[string]bool{}, operations: operations,
		now:                           func() time.Time { return time.Now().UTC() },
		readinessTTL:                  readinessSuccessTTL,
		readinessTimeout:              readinessProviderTimeout,
		computeAllocationPollInterval: computeAllocationPollInterval, computeAllocationPollWindow: computeAllocationPollWindow,
		computeAllocationAttemptTimeout: computeAllocationAttemptTimeout, computeAllocationFinalizeTimeout: computeAllocationFinalizeTimeout,
	}
}

func (s *Service) Catalog(_ context.Context) Catalog {
	return s.provider.Descriptor().Catalog
}

func (s *Service) MonthlyPreflight(ctx context.Context, input MonthlyPreflightInput) (MonthlyPreflight, error) {
	if (input.ResourceType != "compute" && input.ResourceType != "storage") || strings.TrimSpace(input.PackageID) == "" || input.PackageID != strings.TrimSpace(input.PackageID) || input.Zone == "" || input.Zone != strings.TrimSpace(input.Zone) ||
		(input.ResourceType == "compute" && input.SizeGB != 0) || (input.ResourceType == "storage" && input.SizeGB <= 0) {
		return MonthlyPreflight{}, ErrInvalidMonthlyPreflight
	}
	result, err := s.provider.MonthlyPreflight(ctx, input)
	if err != nil {
		return MonthlyPreflight{}, fmt.Errorf("%w: %v", ErrMonthlyPreflightUnavailable, err)
	}
	requiredRequestIDs := []string{"nodePool", "subnets", "availability", "quota"}
	if input.ResourceType == "storage" {
		requiredRequestIDs = []string{"quota", "price"}
	}
	validRequestIDs := len(result.ProviderRequestIDs) > 0
	for _, key := range requiredRequestIDs {
		validRequestIDs = validRequestIDs && strings.TrimSpace(result.ProviderRequestIDs[key]) != ""
	}
	pricingValid := !s.provider.Descriptor().RequiresMonthlyPricing ||
		strings.TrimSpace(result.ChargeType) != "" && result.PeriodMonths > 0 && strings.TrimSpace(result.RenewFlag) != "" && result.ProviderPriceCNY > 0
	if result.ResourceType != input.ResourceType || result.PackageID != input.PackageID || result.SizeGB != input.SizeGB || result.Zone != input.Zone || !result.Available ||
		!pricingValid || math.IsNaN(result.ProviderPriceCNY) || math.IsInf(result.ProviderPriceCNY, 0) || !validRequestIDs ||
		(input.ResourceType == "compute" && strings.TrimSpace(result.NodePoolID) == "") {
		return MonthlyPreflight{}, ErrMonthlyPreflightUnavailable
	}
	return result, nil
}

func (s *Service) Readiness(ctx context.Context) (map[string]any, error) {
	s.readinessMu.Lock()
	if s.readinessCached && s.now().Before(s.readinessExpiresAt) {
		result := s.readinessResult
		s.readinessMu.Unlock()
		return result, nil
	}
	refresh := s.readinessRefresh
	if refresh == nil {
		refresh = &readinessRefresh{done: make(chan struct{})}
		s.readinessRefresh = refresh
		go s.refreshReadiness(refresh)
	}
	s.readinessMu.Unlock()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-refresh.done:
		return refresh.result, refresh.err
	}
}

func (s *Service) refreshReadiness(refresh *readinessRefresh) {
	ctx, cancel := context.WithTimeout(context.Background(), s.readinessTimeout)
	result, err := s.provider.Readiness(ctx)
	cancel()

	s.readinessMu.Lock()
	refresh.result = result
	refresh.err = err
	if err == nil {
		s.readinessCached = true
		s.readinessResult = result
		s.readinessExpiresAt = s.now().Add(s.readinessTTL)
	}
	if s.readinessRefresh == refresh {
		s.readinessRefresh = nil
	}
	close(refresh.done)
	s.readinessMu.Unlock()
}

func (s *Service) ListOperations(ctx context.Context) ([]FabricOperation, error) {
	return s.operations.List(ctx)
}

func isReadyResourceStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "running", "ready", "active":
		return true
	default:
		return false
	}
}

func isRetainedStorageStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "retained", "released":
		return true
	default:
		return false
	}
}
