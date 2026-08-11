package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"opl-cloud/services/control-plane/internal/controlplane"
)

const (
	defaultMonthlyBillingInterval = time.Hour
	monthlyRenewalLead            = 24 * time.Hour
	monthlyBillingWorkspacePage   = 50
)

func monthlyBillingWorkerEnabled() bool {
	value := strings.TrimSpace(os.Getenv("OPL_MONTHLY_BILLING_WORKER_ENABLED"))
	return value == "1" || strings.EqualFold(value, "true") || strings.EqualFold(value, "yes")
}

func monthlyBillingWorkerInterval() time.Duration {
	return durationFromEnv("OPL_MONTHLY_BILLING_INTERVAL_MS", defaultMonthlyBillingInterval)
}

func (app *controlPlaneServer) startMonthlyBillingWorker(ctx context.Context, service *controlplane.Service, interval time.Duration) {
	if interval <= 0 {
		interval = defaultMonthlyBillingInterval
	}
	go func() {
		if err := app.runMonthlyBillingOnce(ctx, service, time.Now().UTC()); err != nil {
			log.Printf("monthly billing failed: %v", err)
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				if err := app.runMonthlyBillingOnce(ctx, service, now.UTC()); err != nil {
					log.Printf("monthly billing failed: %v", err)
				}
			}
		}
	}()
}

func (app *controlPlaneServer) runMonthlyBillingOnce(ctx context.Context, service *controlplane.Service, now time.Time) error {
	recoveryOperations, err := queryRuntimeOperations(ctx, app.tables, runtimeOperationQuery{
		Action: "workspace.renewal", Statuses: []string{"verifying"},
	})
	if err != nil {
		return err
	}
	recoveryWorkspaces := make(map[string]struct{}, len(recoveryOperations))
	for _, operation := range recoveryOperations {
		if workspaceID := stringValue(operation["workspaceId"]); workspaceID != "" {
			recoveryWorkspaces[workspaceID] = struct{}{}
		}
	}

	var errs []error
	for offset := 0; ; {
		page, err := app.tables.PageWorkspaces(ctx, "", tablePageQuery{Offset: offset, Limit: monthlyBillingWorkspacePage})
		if err != nil {
			return errors.Join(append(errs, err)...)
		}
		for _, workspace := range page.Items {
			state, present, stateErr := normalizeWorkspaceBillingStateForWorkspace(workspace, workspace)
			if stateErr != nil {
				errs = append(errs, fmt.Errorf("workspace %s: %w", stringValue(workspace["id"]), stateErr))
				continue
			}
			if !present {
				continue
			}
			workspaceID := stringValue(workspace["id"])
			_, recovering := recoveryWorkspaces[workspaceID]
			if !recovering && !workspaceRenewalDue(state, now) {
				continue
			}
			if err := app.processWorkspaceRenewal(ctx, service, workspaceID, now.UTC()); err != nil && !monthlyBusinessOutcome(err) {
				errs = append(errs, fmt.Errorf("workspace %s: %w", stringValue(workspace["id"]), err))
			}
		}
		offset += len(page.Items)
		if offset >= page.Total || len(page.Items) == 0 {
			break
		}
	}
	return errors.Join(errs...)
}

func workspaceRenewalDue(state workspaceBillingState, now time.Time) bool {
	paidThrough, err := time.Parse(time.RFC3339, state.PaidThrough)
	if err != nil {
		return false
	}
	return !now.UTC().Before(paidThrough.UTC()) || state.AutoRenew && !now.UTC().Before(paidThrough.UTC().Add(-monthlyRenewalLead))
}

func monthlyBusinessOutcome(err error) bool {
	return errors.Is(err, errMonthlyInsufficientBalance) || errors.Is(err, errMonthlyAccountUnmapped)
}
