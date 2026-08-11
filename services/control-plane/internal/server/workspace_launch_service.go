package server

import (
	"context"
	"errors"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

var errWorkspaceLaunchStageAdapterUnavailable = errors.New("workspace_launch_stage_adapter_unavailable")

type controlPlaneWorkspaceLaunchStageAdapter struct {
	app           *controlPlaneServer
	service       *controlplane.Service
	keyCredential clients.SessionDelegatedCredential
	keyUserID     int64
}

func (a *controlPlaneWorkspaceLaunchStageAdapter) ReadStage(ctx context.Context, operation workspaceLaunchReconcileOperation) (workspaceLaunchStageObservation, error) {
	if a == nil || a.app == nil || a.service == nil {
		return workspaceLaunchStageObservation{State: workspaceLaunchStageUnknown}, errWorkspaceLaunchStageAdapterUnavailable
	}
	switch operation.Stage {
	case "key":
		return a.readWorkspaceLaunchKey(ctx, operation)
	case "debit":
		return a.readWorkspaceLaunchDebit(ctx, operation)
	case "ensure_compute_allocation", "storage", "attachment", "secret", "runtime":
		return a.readWorkspaceLaunchFabricStage(ctx, operation)
	case "activation":
		return a.readWorkspaceLaunchActivation(ctx, operation)
	case "receipt":
		return a.readWorkspaceLaunchReceipt(ctx, operation)
	default:
		return workspaceLaunchStageObservation{State: workspaceLaunchStageUnknown}, errInvalidWorkspaceLaunchOperation
	}
}

func (a *controlPlaneWorkspaceLaunchStageAdapter) MutateStage(ctx context.Context, operation workspaceLaunchReconcileOperation, idempotencyKey string) error {
	if a == nil || a.app == nil || a.service == nil || idempotencyKey == "" {
		return errWorkspaceLaunchStageAdapterUnavailable
	}
	switch operation.Stage {
	case "key":
		return a.mutateWorkspaceLaunchKey(ctx, operation, idempotencyKey)
	case "debit":
		return a.mutateWorkspaceLaunchDebit(ctx, operation)
	case "ensure_compute_allocation", "storage", "attachment", "secret", "runtime":
		return a.mutateWorkspaceLaunchFabricStage(ctx, operation, idempotencyKey)
	case "activation":
		return a.mutateWorkspaceLaunchActivation(ctx, operation, idempotencyKey)
	case "receipt":
		return a.mutateWorkspaceLaunchReceipt(ctx, operation, idempotencyKey)
	default:
		return errInvalidWorkspaceLaunchOperation
	}
}

func (app *controlPlaneServer) workspaceLaunchReconciler(service *controlplane.Service, credential clients.SessionDelegatedCredential, userID int64) *WorkspaceLaunchReconciler {
	return NewWorkspaceLaunchReconciler(app.tables, &controlPlaneWorkspaceLaunchStageAdapter{
		app: app, service: service, keyCredential: credential, keyUserID: userID,
	})
}

func (app *controlPlaneServer) createWorkspaceLaunch(ctx context.Context, service *controlplane.Service, credential clients.SessionDelegatedCredential, userID int64, command workspaceLaunchReconcileCreate) (workspaceLaunchReconcileOperation, error) {
	return app.workspaceLaunchReconciler(service, credential, userID).Create(ctx, command)
}

func (app *controlPlaneServer) resumeWorkspaceLaunch(ctx context.Context, service *controlplane.Service, operationID string, authorization workspaceLaunchResumeAuthorization) (workspaceLaunchReconcileOperation, error) {
	row, found, err := app.tables.GetRuntimeOperation(ctx, operationID)
	if err != nil {
		return workspaceLaunchReconcileOperation{}, err
	} else if !found {
		return workspaceLaunchReconcileOperation{}, errBillingReviewNotFound
	}
	operation, err := decodeWorkspaceLaunchReconcileOperation(row)
	if err != nil {
		return workspaceLaunchReconcileOperation{}, err
	}
	if existing := operation.ResumeAuthorization; existing != nil && existing.AuthorizationID == authorization.AuthorizationID && authorization.AuthorizedAt == "" {
		authorization.AuthorizedAt = existing.AuthorizedAt
	}
	if authorization.AuthorizedAt == "" {
		authorization.AuthorizedAt = time.Now().UTC().Format(time.RFC3339)
	}
	return app.workspaceLaunchReconciler(service, clients.SessionDelegatedCredential{}, 0).Resume(ctx, operationID, authorization)
}

func (app *controlPlaneServer) runWorkspaceLaunchesOnce(ctx context.Context, service *controlplane.Service) error {
	rows, err := queryRuntimeOperations(ctx, app.tables, runtimeOperationQuery{
		Action: workspaceLaunchAction, ExcludedStatuses: []string{"succeeded", "refunded", "failed", "manual_review"},
	})
	if err != nil {
		return err
	}
	var errs []error
	for _, row := range rows {
		operation, decodeErr := decodeWorkspaceLaunchReconcileOperation(row)
		if decodeErr != nil {
			errs = append(errs, decodeErr)
			continue
		}
		if err := app.runWorkspaceLaunch(ctx, service, operation.ID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (app *controlPlaneServer) runWorkspaceLaunch(ctx context.Context, service *controlplane.Service, operationID string) error {
	_, err := app.workspaceLaunchReconciler(service, clients.SessionDelegatedCredential{}, 0).Reconcile(ctx, operationID)
	return err
}
