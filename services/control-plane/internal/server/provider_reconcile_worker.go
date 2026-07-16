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
	if err := app.runWorkspaceLaunchesOnce(ctx, service); err != nil {
		errs = append(errs, err)
	}
	operations, err := service.FabricOperations(ctx)
	if err != nil {
		errs = append(errs, err)
	} else if err := app.rememberRuntimeOperations(operations); err != nil {
		errs = append(errs, err)
	}
	for _, row := range app.listComputes("") {
		if err := app.reconcileMonthlyCompute(ctx, service, row, now); err != nil {
			errs = append(errs, err)
		}
	}
	for _, row := range app.listStorages("") {
		if err := app.reconcileMonthlyStorage(ctx, service, row, now); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (app *controlPlaneServer) reconcileMonthlyCompute(ctx context.Context, service *controlplane.Service, row map[string]any, now time.Time) error {
	id := stringValue(row["id"])
	if id == "" {
		return nil
	}
	unlock := app.lockResource("compute", id)
	defer unlock()
	var ok bool
	if row, ok = app.getCompute(id); !ok || !providerSyncDue(row, now) {
		return nil
	}
	if stringValue(row["billingStatus"]) == "manual_review" {
		return nil
	}
	if stringValue(row["billingStatus"]) == "preparing" {
		_, err := app.resumeMonthlyPurchase(ctx, service, row)
		return err
	}
	if stringValue(row["desiredStatus"]) == "destroyed" {
		result, err := app.cleanupComputeResource(ctx, service, id, "provider-reconcile:destroy-compute:"+id)
		if err != nil {
			return err
		}
		body := computeResponse(mergeMaps(row, structToMap(result)))
		body["status"], body["billingStatus"] = "destroyed", "stopped"
		return app.saveComputeFact(body)
	}
	result, err := service.SyncMonthlyCompute(ctx, id)
	if err != nil {
		return app.saveComputeFact(providerSyncFacts(row, err))
	}
	return app.saveComputeFact(providerSyncFacts(computeResponse(mergeMaps(row, structToMap(result))), nil))
}

func (app *controlPlaneServer) reconcileMonthlyStorage(ctx context.Context, service *controlplane.Service, row map[string]any, now time.Time) error {
	id := stringValue(row["id"])
	if id == "" {
		return nil
	}
	unlock := app.lockResource("storage", id)
	defer unlock()
	var ok bool
	if row, ok = app.getStorage(id); !ok || !providerSyncDue(row, now) {
		return nil
	}
	if stringValue(row["billingStatus"]) == "manual_review" {
		return nil
	}
	if stringValue(row["billingStatus"]) == "preparing" {
		_, err := app.resumeMonthlyPurchase(ctx, service, row)
		return err
	}
	if stringValue(row["desiredStatus"]) == "destroyed" {
		result, err := service.CleanupMonthlyStorage(ctx, id, "provider-reconcile:destroy-storage:"+id)
		if err != nil {
			return err
		}
		body := storageResponse(mergeMaps(row, structToMap(result)))
		body["billingStatus"] = "stopped"
		return app.saveStorageFact(body)
	}
	result, err := service.SyncMonthlyStorage(ctx, id)
	if err != nil {
		return app.saveStorageFact(providerSyncFacts(row, err))
	}
	return app.saveStorageFact(providerSyncFacts(storageResponse(mergeMaps(row, structToMap(result))), nil))
}

func providerSyncDue(row map[string]any, now time.Time) bool {
	if isTerminalResourceStatus(stringValue(row["status"])) || stringValue(row["billingStatus"]) == "stopped" {
		return false
	}
	if stringValue(row["billingStatus"]) == "preparing" || stringValue(row["desiredStatus"]) == "destroyed" {
		return true
	}
	status := stringValue(row["status"])
	if status != "provisioning" && status != "pending" && status != "creating" && status != "running" && status != "ready" && status != "active" && status != "available" && status != "bound" {
		return false
	}
	lastSync, ok := parseTimeString(stringValue(row["lastProviderSyncAt"]))
	return !ok || now.Sub(lastSync) >= providerFreshnessWindow()
}

func parseTimeString(value string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), true
		}
	}
	return time.Time{}, false
}
