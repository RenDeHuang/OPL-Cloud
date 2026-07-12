package server

import (
	"context"
	"errors"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"opl-cloud/services/control-plane/internal/controlplane"
)

const (
	defaultProviderReconcileInterval = 10 * time.Minute
	defaultProviderFreshnessWindow   = 15 * time.Minute
)

func providerReconcileWorkerEnabled() bool {
	value := strings.TrimSpace(os.Getenv("OPL_PROVIDER_RECONCILE_WORKER_ENABLED"))
	return value == "1" || strings.EqualFold(value, "true") || strings.EqualFold(value, "yes")
}

func providerReconcileInterval() time.Duration {
	return durationFromEnv("OPL_PROVIDER_RECONCILE_INTERVAL_MS", defaultProviderReconcileInterval)
}

func providerFreshnessWindow() time.Duration {
	return durationFromEnv("OPL_PROVIDER_FRESHNESS_WINDOW_MS", defaultProviderFreshnessWindow)
}

func durationFromEnv(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	ms, err := strconv.Atoi(raw)
	if err != nil || ms <= 0 {
		return fallback
	}
	return time.Duration(ms) * time.Millisecond
}

func (app *controlPlaneServer) startProviderReconcileWorker(ctx context.Context, service *controlplane.Service, interval time.Duration) {
	if interval <= 0 {
		interval = defaultProviderReconcileInterval
	}
	go func() {
		if err := app.runProviderReconcileOnce(ctx, service, time.Now().UTC()); err != nil {
			log.Printf("provider reconcile failed: %v", err)
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				if err := app.runProviderReconcileOnce(ctx, service, now.UTC()); err != nil {
					log.Printf("provider reconcile failed: %v", err)
				}
			}
		}
	}()
}

func (app *controlPlaneServer) runProviderReconcileOnce(ctx context.Context, service *controlplane.Service, now time.Time) error {
	var errs []error
	operations, err := service.FabricOperations(ctx)
	if err != nil {
		errs = append(errs, err)
	} else if err := app.rememberRuntimeOperations(operations); err != nil {
		errs = append(errs, err)
	}
	computes, storages, err := app.settlementResourceRows(ctx)
	if err != nil {
		return err
	}
	for id, row := range computes {
		if stringValue(row["status"]) == "failed" && stringValue(row["holdId"]) != "" && stringValue(row["holdReleaseId"]) == "" {
			release, releaseErr := service.ReleaseResourceHold(ctx, destroyResourceInput(id, row), "compute", "compute_create_failed", "provider-reconcile:compute:"+id)
			if releaseErr != nil {
				errs = append(errs, releaseErr)
				continue
			}
			body := cloneMap(row)
			body["holdReleaseId"] = release.ID
			body["ledgerEntryId"] = release.LedgerEntryID
			body["walletTransactionId"] = release.WalletTransactionID
			body["wallet"] = structToMap(release.Wallet)
			body["billingStatus"] = "stopped"
			if saveErr := app.saveComputeFact(body); saveErr != nil {
				errs = append(errs, saveErr)
			}
			continue
		}
		if !providerSyncDue(row, now) {
			continue
		}
		result, err := service.SyncComputeAllocation(ctx, destroyResourceInput(id, row), "provider-reconcile:compute:"+id)
		if err != nil {
			if saveErr := app.saveComputeFact(providerSyncFacts(row, err)); saveErr != nil {
				return saveErr
			}
			continue
		}
		body := providerSyncFacts(computeResponse(mergeMaps(row, structToMap(result))), nil)
		if err := app.saveComputeFact(body); err != nil {
			return err
		}
	}
	for id, row := range storages {
		if !providerSyncDue(row, now) {
			continue
		}
		result, err := service.SyncStorageVolume(ctx, destroyResourceInput(id, row), "provider-reconcile:storage:"+id)
		if err != nil {
			if saveErr := app.saveStorageFact(providerSyncFacts(row, err)); saveErr != nil {
				return saveErr
			}
			continue
		}
		body := providerSyncFacts(storageResponse(mergeMaps(row, structToMap(result))), nil)
		if err := app.saveStorageFact(body); err != nil {
			return err
		}
	}
	return errors.Join(errs...)
}

func providerSyncDue(row map[string]any, now time.Time) bool {
	if billingStatusFor(row) == "stopped" || isTerminalResourceStatus(stringValue(row["status"])) {
		return false
	}
	status := stringValue(row["status"])
	if status != "running" && status != "ready" && status != "active" && status != "available" && status != "bound" {
		return false
	}
	lastSync, ok := parseTimeString(stringValue(row["lastProviderSyncAt"]))
	return !ok || now.Sub(lastSync) >= providerFreshnessWindow()
}

func parseTimeString(value string) (time.Time, bool) {
	if value == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed.UTC(), true
		}
	}
	return time.Time{}, false
}
