package ledger

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestReceiptRejectsMissingIdentityAndSecretContent(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	if _, err := store.RecordReceipt(ctx, ReceiptInput{WorkspaceID: "ws-alpha", IdempotencyKey: "invalid-receipt"}); !errors.Is(err, ErrInvalidReceiptInput) {
		t.Fatalf("missing receipt fields error = %v, want ErrInvalidReceiptInput", err)
	}
	_, err := store.RecordReceipt(ctx, ReceiptInput{Type: "workspace.created", Status: "completed", Surface: "workspace", WorkspaceID: "ws-alpha", Actor: map[string]any{"secret": "must-not-persist"}, IdempotencyKey: "secret-receipt"})
	if !errors.Is(err, ErrInvalidReceiptInput) {
		t.Fatalf("secret receipt error = %v, want ErrInvalidReceiptInput", err)
	}
}

func validWorkspaceGatewayKeyRotationReceiptInput() ReceiptInput {
	return ReceiptInput{
		Type: "workspace.gateway_key_rotated.v1", Status: "completed", Surface: "control_plane",
		AccountID: "acct-alpha", WorkspaceID: "workspace-alpha",
		Execution:      map[string]any{"operationId": "workspace-key-rotate-alpha", "oldKeyId": int64(9), "newKeyId": int64(19)},
		OutputRefs:     map[string]any{"secretFingerprint": "sha256:replacement"},
		Owner:          map[string]any{"userId": "usr-alpha"},
		IdempotencyKey: "workspace-key-rotate-alpha:receipt",
	}
}

func TestWorkspaceGatewayKeyRotationReceiptSchemaMemory(t *testing.T) {
	store := NewMemoryStore()
	if _, err := store.RecordReceipt(context.Background(), validWorkspaceGatewayKeyRotationReceiptInput()); err != nil {
		t.Fatalf("valid Workspace Key rotation receipt: %v", err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*ReceiptInput)
	}{
		{name: "missing operation", mutate: func(input *ReceiptInput) { delete(input.Execution, "operationId") }},
		{name: "missing old Key", mutate: func(input *ReceiptInput) { delete(input.Execution, "oldKeyId") }},
		{name: "same Key", mutate: func(input *ReceiptInput) { input.Execution["newKeyId"] = int64(9) }},
		{name: "missing fingerprint", mutate: func(input *ReceiptInput) { delete(input.OutputRefs, "secretFingerprint") }},
		{name: "missing owner", mutate: func(input *ReceiptInput) { input.Owner = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := validWorkspaceGatewayKeyRotationReceiptInput()
			input.IdempotencyKey += "-" + strings.ReplaceAll(tc.name, " ", "-")
			tc.mutate(&input)
			if _, err := store.RecordReceipt(context.Background(), input); !errors.Is(err, ErrInvalidReceiptInput) {
				t.Fatalf("error=%v, want ErrInvalidReceiptInput", err)
			}
		})
	}
}

func validWalletAdjustmentReceiptInput() ReceiptInput {
	return ReceiptInput{
		Type: "gateway.wallet_adjustment.v1", Status: "completed", Surface: "control_plane", AccountID: "acct-alpha",
		RequestID:      "wallet-adjustment-alpha",
		Actor:          map[string]any{"userId": "usr-admin"},
		Execution:      map[string]any{"operationId": "wallet-adjustment-alpha", "kind": "business_refund", "amountUsdMicros": int64(2_500_000)},
		InputRefs:      map[string]any{"balanceHistoryRef": "sub2api:balance-history:41:history-alpha", "relatedOperationId": "workspace-launch-alpha"},
		Owner:          map[string]any{"accountId": "acct-alpha"},
		IdempotencyKey: "wallet-adjustment-alpha:receipt",
	}
}

func TestWalletAdjustmentReceipt(t *testing.T) {
	store := NewMemoryStore()
	if _, err := store.RecordReceipt(context.Background(), validWalletAdjustmentReceiptInput()); err != nil {
		t.Fatalf("valid wallet adjustment receipt: %v", err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*ReceiptInput)
	}{
		{name: "missing operation", mutate: func(input *ReceiptInput) { delete(input.Execution, "operationId") }},
		{name: "invalid kind", mutate: func(input *ReceiptInput) { input.Execution["kind"] = "payment_refund" }},
		{name: "non positive amount", mutate: func(input *ReceiptInput) { input.Execution["amountUsdMicros"] = int64(0) }},
		{name: "missing history", mutate: func(input *ReceiptInput) { delete(input.InputRefs, "balanceHistoryRef") }},
		{name: "refund missing related operation", mutate: func(input *ReceiptInput) { delete(input.InputRefs, "relatedOperationId") }},
		{name: "missing actor", mutate: func(input *ReceiptInput) { input.Actor = nil }},
		{name: "missing owner", mutate: func(input *ReceiptInput) { input.Owner = nil }},
		{name: "forbidden redeem code", mutate: func(input *ReceiptInput) { input.OutputRefs = map[string]any{"redeemCode": "must-not-persist"} }},
		{name: "forbidden redeem code in plan", mutate: func(input *ReceiptInput) { input.Plan = map[string]any{"redeemCode": "must-not-persist"} }},
		{name: "unexpected identity", mutate: func(input *ReceiptInput) { input.ProjectID = "project-alpha" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := validWalletAdjustmentReceiptInput()
			input.IdempotencyKey += "-" + strings.ReplaceAll(tc.name, " ", "-")
			tc.mutate(&input)
			if _, err := store.RecordReceipt(context.Background(), input); !errors.Is(err, ErrInvalidReceiptInput) {
				t.Fatalf("error=%v, want ErrInvalidReceiptInput", err)
			}
		})
	}
}

func validBillingReceiptInput() ReceiptInput {
	return ReceiptInput{
		Type: "billing.reconciliation.v1", Status: "completed", Surface: "control_plane", AccountID: "acct-alpha", WorkspaceID: "workspace-alpha",
		Cost: map[string]any{
			"pricingVersion": "pricing-v1", "monthlyPriceCnyCents": int64(35_000), "chargeUsdMicros": int64(50_000_000),
			"sub2apiUserId": int64(41), "sub2apiRedeemCode": "opl:test:billing-alpha:charge:v1", "periodStart": "2026-07-01T00:00:00Z",
			"paidThrough": "2026-08-01T00:00:00Z", "resourceType": "compute", "resourceId": "compute-alpha",
		},
		IdempotencyKey: "billing-schema",
	}
}

func TestBillingReceiptSchemaMemory(t *testing.T) {
	testBillingReceiptSchema(t, NewMemoryStore())
}

func TestLegacyResourceBillingReceiptsAreReadOnly(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	legacyTypes := []string{
		"billing.resource_purchased.v1",
		"billing.resource_renewed.v1",
		"billing.resource_expired.v1",
		"billing.resource_refunded.v1",
		"billing.charge_review_required.v1",
	}
	for _, receiptType := range legacyTypes {
		t.Run(receiptType, func(t *testing.T) {
			input := validBillingReceiptInput()
			input.Type = receiptType
			input.IdempotencyKey = "retired-" + receiptType
			if _, err := store.RecordReceipt(ctx, input); !errors.Is(err, ErrInvalidReceiptInput) {
				t.Fatalf("new legacy receipt write error = %v, want ErrInvalidReceiptInput", err)
			}
		})
	}

	legacy := validBillingReceiptInput()
	legacy.Type = "billing.resource_purchased.v1"
	legacy.IdempotencyKey = ""
	receipt := Receipt{ReceiptInput: legacy, ReceiptID: "receipt-legacy-resource", CreatedAt: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)}
	store.receipts[receipt.ReceiptID] = receipt
	read, err := store.Receipt(ctx, receipt.ReceiptID)
	if err != nil || read.Type != legacy.Type {
		t.Fatalf("legacy receipt read = %#v, err=%v", read, err)
	}
	page, err := store.ListReceipts(ctx, ReceiptQuery{Type: legacy.Type})
	if err != nil || len(page.Receipts) != 1 || page.Receipts[0].ReceiptID != receipt.ReceiptID {
		t.Fatalf("legacy receipt list = %#v, err=%v", page, err)
	}
}

func validWorkspaceBillingReceiptInput(receiptType string) ReceiptInput {
	input := ReceiptInput{
		Type: receiptType, Status: "completed", Surface: "control_plane", AccountID: "acct-alpha", WorkspaceID: "workspace-alpha",
		Cost: map[string]any{
			"priceVersion": "pilot-usd-2026-07-v1", "currency": "USD", "billingUnit": "calendar_month",
			"totalUsdMicros": int64(52_580_000), "periodStart": "2026-08-31T09:30:00Z", "paidThrough": "2026-09-30T09:30:00Z",
			"resourceType": "workspace", "resourceId": "workspace-alpha",
			"components": map[string]any{
				"compute": map[string]any{"resourceType": "compute", "resourceId": "compute-alpha", "chargeUsdMicros": int64(50_000_000)},
				"storage": map[string]any{"resourceType": "storage", "resourceId": "storage-alpha", "sizeGb": int64(10), "chargeUsdMicros": int64(2_580_000)},
			},
		},
		IdempotencyKey: "workspace-billing-schema-" + receiptType,
	}
	if receiptType == "billing.workspace_purchased.v1" || receiptType == "billing.workspace_renewed.v1" {
		input.Cost["sub2apiUserId"], input.Cost["sub2apiRedeemCode"], input.Cost["postChargeBalanceUsdMicros"] = int64(41), "opl:workspace-renewal:charge:v1", int64(47_420_000)
	} else if receiptType == "billing.workspace_refunded.v1" {
		input.Cost["sub2apiUserId"], input.Cost["sub2apiRedeemCode"] = int64(41), "opl:workspace-renewal:charge:v1"
		input.Cost["sub2apiRefundCode"], input.Cost["refundUsdMicros"] = "opl:workspace-renewal:refund:v1", int64(52_580_000)
	}
	return input
}

func validWorkspaceLaunchReceiptInput(receiptType string) ReceiptInput {
	input := validWorkspaceBillingReceiptInput("billing.workspace_purchased.v1")
	input.Type = receiptType
	input.RequestID = "workspace-launch-alpha"
	input.Execution = map[string]any{
		"operationId": "workspace-launch-alpha", "resourceType": "workspace", "resourceId": "workspace-alpha",
		"computeAllocationId": "compute-alpha", "storageId": "storage-alpha", "attachmentId": "attachment-alpha", "runtimeId": "runtime-alpha",
		"workspaceApiKeyId": int64(9), "workspaceKeyFingerprint": "sha256:alpha", "runtimeServiceName": "runtime-alpha", "gatewaySecretRef": "secret-alpha",
	}
	input.Owner = map[string]any{"accountId": "acct-alpha", "workspaceId": "workspace-alpha", "ownerUserId": "usr-alpha"}
	input.IdempotencyKey = "workspace-launch-alpha:purchase-receipt"
	if receiptType == "workspace.created" {
		input.Cost = nil
	}
	return input
}

func validWorkspaceDeletionReceiptInput() ReceiptInput {
	return ReceiptInput{
		Type: "workspace.deleted.v1", Status: "completed", Surface: "control_plane", AccountID: "acct-alpha", WorkspaceID: "workspace-alpha",
		RequestID: "workspace-delete-alpha", InputRefs: map[string]any{"launchReceiptId": "receipt-launch-alpha"},
		Execution: map[string]any{
			"operationId": "workspace-delete-alpha", "resourceType": "workspace", "resourceId": "workspace-alpha",
			"computeAllocationId": "compute-alpha", "storageId": "storage-alpha", "attachmentId": "attachment-alpha", "runtimeId": "runtime-alpha",
			"workspaceApiKeyId": int64(9), "workspaceKeyFingerprint": "sha256:alpha", "runtimeServiceName": "runtime-alpha", "gatewaySecretRef": "secret-alpha",
		},
		OutputRefs: map[string]any{
			"runtimeStatus": "absent", "gatewaySecretStatus": "absent", "attachmentStatus": "absent", "storageStatus": "absent",
			"computeStatus": "absent", "workspaceKeyStatus": "absent", "workspaceStatus": "absent",
		},
		Owner:          map[string]any{"accountId": "acct-alpha", "workspaceId": "workspace-alpha", "ownerUserId": "usr-alpha"},
		IdempotencyKey: "workspace-delete-alpha:deletion-receipt",
	}
}

func TestWorkspaceLaunchReceiptSchemaMemory(t *testing.T) {
	for _, receiptType := range []string{"billing.workspace_purchased.v1", "workspace.created"} {
		t.Run(receiptType+" valid", func(t *testing.T) {
			if _, err := NewMemoryStore().RecordReceipt(context.Background(), validWorkspaceLaunchReceiptInput(receiptType)); err != nil {
				t.Fatalf("valid launch receipt: %v", err)
			}
		})
		for _, test := range []struct {
			name   string
			mutate func(*ReceiptInput)
		}{
			{name: "wrong surface", mutate: func(input *ReceiptInput) { input.Surface = "workspace" }},
			{name: "request mismatch", mutate: func(input *ReceiptInput) { input.RequestID = "workspace-launch-other" }},
			{name: "owner extra", mutate: func(input *ReceiptInput) { input.Owner["userId"] = "usr-alpha" }},
			{name: "owner mismatch", mutate: func(input *ReceiptInput) { input.Owner["accountId"] = "acct-other" }},
			{name: "execution missing", mutate: func(input *ReceiptInput) { delete(input.Execution, "gatewaySecretRef") }},
			{name: "execution extra", mutate: func(input *ReceiptInput) { input.Execution["provider"] = "local-docker" }},
			{name: "resource mismatch", mutate: func(input *ReceiptInput) { input.Execution["resourceId"] = "workspace-other" }},
			{name: "non-positive Key ID", mutate: func(input *ReceiptInput) { input.Execution["workspaceApiKeyId"] = int64(0) }},
			{name: "top-level extra", mutate: func(input *ReceiptInput) { input.ProjectID = "project-alpha" }},
		} {
			t.Run(receiptType+" "+test.name, func(t *testing.T) {
				input := validWorkspaceLaunchReceiptInput(receiptType)
				test.mutate(&input)
				input.IdempotencyKey += "-" + strings.ReplaceAll(test.name, " ", "-")
				if _, err := NewMemoryStore().RecordReceipt(context.Background(), input); !errors.Is(err, ErrInvalidReceiptInput) {
					t.Fatalf("error=%v, want ErrInvalidReceiptInput", err)
				}
			})
		}
	}
	for _, test := range []struct {
		name   string
		mutate func(*ReceiptInput)
	}{
		{name: "cost", mutate: func(input *ReceiptInput) { input.Cost = map[string]any{"totalUsdMicros": int64(1)} }},
		{name: "supersedes", mutate: func(input *ReceiptInput) { input.SupersedesReceiptID = "receipt-other" }},
	} {
		t.Run("workspace.created "+test.name, func(t *testing.T) {
			input := validWorkspaceLaunchReceiptInput("workspace.created")
			test.mutate(&input)
			input.IdempotencyKey += "-" + test.name
			if _, err := NewMemoryStore().RecordReceipt(context.Background(), input); !errors.Is(err, ErrInvalidReceiptInput) {
				t.Fatalf("error=%v, want ErrInvalidReceiptInput", err)
			}
		})
	}
}

func TestWorkspaceDeletionReceiptSchemaMemory(t *testing.T) {
	if _, err := NewMemoryStore().RecordReceipt(context.Background(), validWorkspaceDeletionReceiptInput()); err != nil {
		t.Fatalf("valid deletion receipt: %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*ReceiptInput)
	}{
		{name: "wrong status", mutate: func(input *ReceiptInput) { input.Status = "running" }},
		{name: "wrong surface", mutate: func(input *ReceiptInput) { input.Surface = "workspace" }},
		{name: "empty account", mutate: func(input *ReceiptInput) { input.AccountID = "" }},
		{name: "operation mismatch", mutate: func(input *ReceiptInput) { input.Execution["operationId"] = "workspace-delete-other" }},
		{name: "resource mismatch", mutate: func(input *ReceiptInput) { input.Execution["resourceId"] = "workspace-other" }},
		{name: "invalid opaque identity", mutate: func(input *ReceiptInput) { input.Execution["runtimeId"] = "runtime/alpha" }},
		{name: "fractional Key ID", mutate: func(input *ReceiptInput) { input.Execution["workspaceApiKeyId"] = 9.5 }},
		{name: "input ref extra", mutate: func(input *ReceiptInput) { input.InputRefs["purchaseReceiptId"] = "receipt-other" }},
		{name: "owner extra", mutate: func(input *ReceiptInput) { input.Owner["userId"] = "usr-alpha" }},
		{name: "output not absent", mutate: func(input *ReceiptInput) { input.OutputRefs["storageStatus"] = "retained" }},
		{name: "output missing", mutate: func(input *ReceiptInput) { delete(input.OutputRefs, "workspaceStatus") }},
		{name: "cost", mutate: func(input *ReceiptInput) { input.Cost = map[string]any{"refundUsdMicros": int64(1)} }},
		{name: "supersedes", mutate: func(input *ReceiptInput) { input.SupersedesReceiptID = "receipt-launch-alpha" }},
		{name: "recursive refund field", mutate: func(input *ReceiptInput) {
			input.Actor = map[string]any{"evidence": map[string]any{"refundTrace": "refund-alpha"}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := validWorkspaceDeletionReceiptInput()
			test.mutate(&input)
			input.IdempotencyKey += "-" + strings.ReplaceAll(test.name, " ", "-")
			if _, err := NewMemoryStore().RecordReceipt(context.Background(), input); !errors.Is(err, ErrInvalidReceiptInput) {
				t.Fatalf("error=%v, want ErrInvalidReceiptInput", err)
			}
		})
	}
}

func TestWorkspaceBillingReceiptSchemaMemory(t *testing.T) {
	testWorkspaceBillingReceiptSchema(t, NewMemoryStore())
}

func testWorkspaceBillingReceiptSchema(t *testing.T, store Store) {
	t.Helper()
	ctx := context.Background()
	for _, status := range []string{"planned", "running", "failed", "cancelled", "review_required"} {
		t.Run("reject status "+status, func(t *testing.T) {
			input := validWorkspaceBillingReceiptInput("billing.workspace_renewed.v1")
			input.Status = status
			input.IdempotencyKey += "-status-" + status
			if _, err := store.RecordReceipt(ctx, input); !errors.Is(err, ErrInvalidReceiptInput) {
				t.Fatalf("status %q error=%v, want ErrInvalidReceiptInput", status, err)
			}
		})
	}
	for _, receiptType := range []string{"billing.workspace_renewed.v1", "billing.workspace_expired.v1", "billing.workspace_refunded.v1"} {
		t.Run(receiptType, func(t *testing.T) {
			if _, err := store.RecordReceipt(ctx, validWorkspaceBillingReceiptInput(receiptType)); err != nil {
				t.Fatalf("valid Workspace billing receipt: %v", err)
			}
			for _, field := range []string{"priceVersion", "currency", "billingUnit", "totalUsdMicros", "periodStart", "paidThrough", "resourceType", "resourceId", "components"} {
				t.Run("missing "+field, func(t *testing.T) {
					input := validWorkspaceBillingReceiptInput(receiptType)
					delete(input.Cost, field)
					input.IdempotencyKey += "-missing-" + field
					if _, err := store.RecordReceipt(ctx, input); !errors.Is(err, ErrInvalidReceiptInput) {
						t.Fatalf("error=%v, want ErrInvalidReceiptInput", err)
					}
				})
			}
		})
	}
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "total mismatch", mutate: func(cost map[string]any) { cost["totalUsdMicros"] = int64(52_579_999) }},
		{name: "fractional total", mutate: func(cost map[string]any) { cost["totalUsdMicros"] = 52_580_000.5 }},
		{name: "missing compute", mutate: func(cost map[string]any) { delete(cost["components"].(map[string]any), "compute") }},
		{name: "extra component", mutate: func(cost map[string]any) { cost["components"].(map[string]any)["network"] = map[string]any{} }},
		{name: "empty compute id", mutate: func(cost map[string]any) {
			cost["components"].(map[string]any)["compute"].(map[string]any)["resourceId"] = ""
		}},
		{name: "fractional compute", mutate: func(cost map[string]any) {
			cost["components"].(map[string]any)["compute"].(map[string]any)["chargeUsdMicros"] = 1.5
		}},
		{name: "negative storage", mutate: func(cost map[string]any) {
			cost["components"].(map[string]any)["storage"].(map[string]any)["chargeUsdMicros"] = int64(-1)
		}},
		{name: "zero storage size", mutate: func(cost map[string]any) {
			cost["components"].(map[string]any)["storage"].(map[string]any)["sizeGb"] = int64(0)
		}},
		{name: "wrong currency", mutate: func(cost map[string]any) { cost["currency"] = "CNY" }},
		{name: "reversed period", mutate: func(cost map[string]any) { cost["paidThrough"] = "2026-08-01T00:00:00Z" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := validWorkspaceBillingReceiptInput("billing.workspace_renewed.v1")
			test.mutate(input.Cost)
			input.IdempotencyKey += "-" + strings.ReplaceAll(test.name, " ", "-")
			if _, err := store.RecordReceipt(ctx, input); !errors.Is(err, ErrInvalidReceiptInput) {
				t.Fatalf("error=%v, want ErrInvalidReceiptInput", err)
			}
		})
	}
	for _, field := range []string{"sub2apiUserId", "sub2apiRedeemCode", "postChargeBalanceUsdMicros"} {
		t.Run("renewed missing "+field, func(t *testing.T) {
			input := validWorkspaceBillingReceiptInput("billing.workspace_renewed.v1")
			delete(input.Cost, field)
			input.IdempotencyKey += "-missing-" + field
			if _, err := store.RecordReceipt(ctx, input); !errors.Is(err, ErrInvalidReceiptInput) {
				t.Fatalf("error=%v, want ErrInvalidReceiptInput", err)
			}
		})
	}
	for _, field := range []string{"sub2apiUserId", "sub2apiRedeemCode", "sub2apiRefundCode", "refundUsdMicros"} {
		t.Run("refunded missing "+field, func(t *testing.T) {
			input := validWorkspaceBillingReceiptInput("billing.workspace_refunded.v1")
			delete(input.Cost, field)
			input.IdempotencyKey += "-missing-" + field
			if _, err := store.RecordReceipt(ctx, input); !errors.Is(err, ErrInvalidReceiptInput) {
				t.Fatalf("error=%v, want ErrInvalidReceiptInput", err)
			}
		})
	}
	t.Run("refunded amount mismatch", func(t *testing.T) {
		input := validWorkspaceBillingReceiptInput("billing.workspace_refunded.v1")
		input.Cost["refundUsdMicros"] = int64(1)
		input.IdempotencyKey += "-amount-mismatch"
		if _, err := store.RecordReceipt(ctx, input); !errors.Is(err, ErrInvalidReceiptInput) {
			t.Fatalf("error=%v, want ErrInvalidReceiptInput", err)
		}
	})
	for _, receiptType := range []string{"billing.workspace_renewed.v1", "billing.workspace_expired.v1", "billing.workspace_refunded.v1"} {
		t.Run(receiptType+" cross Workspace", func(t *testing.T) {
			input := validWorkspaceBillingReceiptInput(receiptType)
			input.WorkspaceID = "workspace-other"
			input.IdempotencyKey += "-cross-workspace"
			if _, err := store.RecordReceipt(ctx, input); !errors.Is(err, ErrInvalidReceiptInput) {
				t.Fatalf("error=%v, want ErrInvalidReceiptInput", err)
			}
		})
	}
}

func testBillingReceiptSchema(t *testing.T, store Store) {
	t.Helper()
	ctx := context.Background()
	required := []string{"pricingVersion", "monthlyPriceCnyCents", "chargeUsdMicros", "sub2apiUserId", "sub2apiRedeemCode", "periodStart", "paidThrough", "resourceType", "resourceId"}
	receiptTypes := []string{"billing.reconciliation.v1"}
	for _, receiptType := range receiptTypes {
		t.Run(receiptType, func(t *testing.T) {
			for _, field := range required {
				t.Run("missing "+field, func(t *testing.T) {
					input := validBillingReceiptInput()
					input.Type = receiptType
					delete(input.Cost, field)
					if _, err := store.RecordReceipt(ctx, input); !errors.Is(err, ErrInvalidReceiptInput) {
						t.Fatalf("error = %v, want ErrInvalidReceiptInput", err)
					}
				})
			}
			input := validBillingReceiptInput()
			input.Type = receiptType
			if _, err := store.RecordReceipt(ctx, input); err != nil {
				t.Fatalf("valid billing receipt: %v", err)
			}
		})
	}
	for _, field := range required {
		t.Run("wrong type "+field, func(t *testing.T) {
			input := validBillingReceiptInput()
			input.Cost[field] = true
			if _, err := store.RecordReceipt(ctx, input); !errors.Is(err, ErrInvalidReceiptInput) {
				t.Fatalf("error = %v, want ErrInvalidReceiptInput", err)
			}
		})
	}
	for _, field := range []string{"pricingVersion", "sub2apiRedeemCode", "periodStart", "paidThrough", "resourceType", "resourceId"} {
		t.Run("empty "+field, func(t *testing.T) {
			input := validBillingReceiptInput()
			input.Cost[field] = ""
			if _, err := store.RecordReceipt(ctx, input); !errors.Is(err, ErrInvalidReceiptInput) {
				t.Fatalf("error = %v, want ErrInvalidReceiptInput", err)
			}
		})
	}
	for _, test := range []struct {
		name  string
		field string
		value any
	}{
		{name: "fractional CNY", field: "monthlyPriceCnyCents", value: 1.5},
		{name: "negative CNY", field: "monthlyPriceCnyCents", value: int64(-1)},
		{name: "fractional USD", field: "chargeUsdMicros", value: 1.5},
		{name: "negative USD", field: "chargeUsdMicros", value: int64(-1)},
		{name: "fractional user id", field: "sub2apiUserId", value: 41.5},
		{name: "non-positive user id", field: "sub2apiUserId", value: int64(0)},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := validBillingReceiptInput()
			input.Cost[test.field] = test.value
			if _, err := store.RecordReceipt(ctx, input); !errors.Is(err, ErrInvalidReceiptInput) {
				t.Fatalf("error = %v, want ErrInvalidReceiptInput", err)
			}
		})
	}
	invalidFuture := validBillingReceiptInput()
	invalidFuture.Type = "billing.future.v1"
	delete(invalidFuture.Cost, "resourceId")
	if _, err := store.RecordReceipt(ctx, invalidFuture); !errors.Is(err, ErrInvalidReceiptInput) {
		t.Fatalf("future billing receipt error = %v, want ErrInvalidReceiptInput", err)
	}
	valid := validBillingReceiptInput()
	valid.IdempotencyKey = "billing-schema-json-numbers"
	valid.Cost["monthlyPriceCnyCents"], valid.Cost["chargeUsdMicros"], valid.Cost["sub2apiUserId"] = float64(35_000), float64(50_000_000), float64(41)
	if _, err := store.RecordReceipt(ctx, valid); err != nil {
		t.Fatalf("integral JSON numbers must be accepted: %v", err)
	}
}

func TestReceiptRejectsSensitiveContentMemory(t *testing.T) {
	testReceiptRejectsSensitiveContent(t, NewMemoryStore())
}

func TestEvidenceIntegerValidationRejectsUnsafeFloatPrecision(t *testing.T) {
	for name, unsafeFloat := range map[string]any{
		"float32": float32(16_777_217),
		"float64": float64(9_007_199_254_740_993),
	} {
		t.Run(name, func(t *testing.T) {
			store := NewMemoryStore()
			ctx := context.Background()

			receipt := validBillingReceiptInput()
			receipt.IdempotencyKey = "unsafe-" + name + "-receipt"
			receipt.Cost["monthlyPriceCnyCents"] = unsafeFloat
			if _, err := store.RecordReceipt(ctx, receipt); !errors.Is(err, ErrInvalidReceiptInput) {
				t.Fatalf("unsafe receipt %s error = %v, want ErrInvalidReceiptInput", name, err)
			}

			report := validReconciliationReport("ok")
			report["id"] = "unsafe-" + name + "-reconciliation"
			report["counts"].(map[string]any)["billingOperations"] = unsafeFloat
			report["counts"].(map[string]any)["matched"] = unsafeFloat
			if _, err := store.RecordReconciliation(ctx, ReconciliationInput{Report: report, IdempotencyKey: "unsafe-" + name + "-reconciliation"}); !errors.Is(err, ErrInvalidReconciliationInput) {
				t.Fatalf("unsafe reconciliation %s error = %v, want ErrInvalidReconciliationInput", name, err)
			}
		})
	}
}

func testReceiptRejectsSensitiveContent(t *testing.T, store Store) {
	t.Helper()
	ctx := context.Background()
	for _, key := range []string{"apiKey", "adminToken", "rawSub2apiResponse", "rawProviderResponse", "password", "token"} {
		t.Run(key, func(t *testing.T) {
			input := ReceiptInput{
				Type: "execution.receipt.v1", Status: "completed", Surface: "workspace", WorkspaceID: "workspace-alpha",
				OutputRefs: map[string]any{"nested": []map[string]string{{strings.ToUpper(key): "must-not-persist"}}}, IdempotencyKey: "typed-sensitive",
			}
			if _, err := store.RecordReceipt(ctx, input); !errors.Is(err, ErrInvalidReceiptInput) {
				t.Fatalf("error = %v, want ErrInvalidReceiptInput", err)
			}
		})
	}
	input := ReceiptInput{
		Type: "execution.receipt.v1", Status: "completed", Surface: "workspace", WorkspaceID: "workspace-alpha",
		OutputRefs: map[string]any{"inputTokens": int64(42), "outputTokens": int64(12), "tokenCount": int64(54)}, IdempotencyKey: "typed-sensitive",
	}
	if _, err := store.RecordReceipt(ctx, input); err != nil {
		t.Fatalf("token count fields must remain allowed after rejected inputs: %v", err)
	}
}

func validReconciliationReport(status string) map[string]any {
	exceptions := []any{}
	checked, matched := 2, 2
	if status == "mismatch" {
		matched = 1
		exceptions = append(exceptions, map[string]any{"resourceType": "compute", "resourceId": "compute-alpha", "code": "ledger_receipt_missing"})
	}
	return map[string]any{
		"id": "recon-alpha", "status": status,
		"counts":     map[string]any{"billingOperations": checked, "matched": matched, "exceptions": len(exceptions)},
		"exceptions": exceptions,
	}
}

func TestReconciliationSchemaMemory(t *testing.T) {
	testReconciliationSchema(t, NewMemoryStore())
}

func testReconciliationSchema(t *testing.T, store Store) {
	t.Helper()
	ctx := context.Background()
	assertInvalid := func(t *testing.T, report map[string]any, key string) {
		t.Helper()
		_, err := store.RecordReconciliation(ctx, ReconciliationInput{Report: report, IdempotencyKey: key})
		if !errors.Is(err, ErrInvalidReconciliationInput) {
			t.Fatalf("error = %v, want ErrInvalidReconciliationInput", err)
		}
	}

	for _, test := range []struct {
		name   string
		status string
		mutate func(map[string]any)
	}{
		{name: "missing id", status: "ok", mutate: func(report map[string]any) { delete(report, "id") }},
		{name: "empty id", status: "ok", mutate: func(report map[string]any) { report["id"] = "" }},
		{name: "missing status", status: "ok", mutate: func(report map[string]any) { delete(report, "status") }},
		{name: "unknown status", status: "ok", mutate: func(report map[string]any) { report["status"] = "unknown" }},
		{name: "missing counts", status: "ok", mutate: func(report map[string]any) { delete(report, "counts") }},
		{name: "exceptions not array", status: "ok", mutate: func(report map[string]any) { report["exceptions"] = map[string]any{} }},
		{name: "exception item not object", status: "mismatch", mutate: func(report map[string]any) { report["exceptions"] = []any{"unsafe"} }},
		{name: "unknown exception code", status: "mismatch", mutate: func(report map[string]any) { report["exceptions"].([]any)[0].(map[string]any)["code"] = "unknown" }},
		{name: "non opaque resource id", status: "mismatch", mutate: func(report map[string]any) {
			report["exceptions"].([]any)[0].(map[string]any)["resourceId"] = "https://provider.test/resource?token=secret"
		}},
		{name: "sensitive typed content", status: "ok", mutate: func(report map[string]any) {
			report["details"] = []map[string]string{{"AdminToken": "must-not-persist"}}
		}},
		{name: "ok with exception", status: "mismatch", mutate: func(report map[string]any) { report["status"] = "ok" }},
		{name: "mismatch without exception", status: "ok", mutate: func(report map[string]any) { report["status"] = "mismatch" }},
		{name: "exception count mismatch", status: "mismatch", mutate: func(report map[string]any) { report["counts"].(map[string]any)["exceptions"] = 0 }},
		{name: "matched resource mismatch", status: "mismatch", mutate: func(report map[string]any) { report["counts"].(map[string]any)["matched"] = 0 }},
		{name: "extra negative count", status: "ok", mutate: func(report map[string]any) { report["counts"].(map[string]any)["other"] = -1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			report := validReconciliationReport(test.status)
			test.mutate(report)
			assertInvalid(t, report, "reconciliation-schema")
		})
	}
	for _, field := range []string{"billingOperations", "matched", "exceptions"} {
		t.Run("missing count "+field, func(t *testing.T) {
			report := validReconciliationReport("ok")
			delete(report["counts"].(map[string]any), field)
			assertInvalid(t, report, "reconciliation-schema")
		})
		for name, value := range map[string]any{"negative": int64(-1), "fractional": 1.5, "wrong type": "1"} {
			t.Run(name+" count "+field, func(t *testing.T) {
				report := validReconciliationReport("ok")
				report["counts"].(map[string]any)[field] = value
				assertInvalid(t, report, "reconciliation-schema")
			})
		}
	}
	for _, field := range []string{"resourceType", "resourceId", "code"} {
		t.Run("missing exception "+field, func(t *testing.T) {
			report := validReconciliationReport("mismatch")
			delete(report["exceptions"].([]any)[0].(map[string]any), field)
			assertInvalid(t, report, "reconciliation-schema")
		})
	}
	assertInvalid(t, validReconciliationReport("ok"), "")

	valid := validReconciliationReport("ok")
	if result, err := store.RecordReconciliation(ctx, ReconciliationInput{Report: valid, IdempotencyKey: "reconciliation-schema"}); err != nil || result.Status != "ok" || result.BlockNewWorkspaces {
		t.Fatalf("valid report after rejected inputs = %#v, %v", result, err)
	}
	for _, code := range []string{"billing_operation_invalid", "sub2api_balance_history_unavailable", "sub2api_charge_missing", "sub2api_charge_mismatch", "fabric_operations_unavailable", "fabric_operation_missing", "fabric_operation_mismatch", "ledger_receipts_unavailable", "ledger_receipt_missing", "ledger_receipt_mismatch"} {
		report := validReconciliationReport("mismatch")
		report["id"] = "recon-" + code
		report["exceptions"].([]any)[0].(map[string]any)["code"] = code
		if _, err := store.RecordReconciliation(ctx, ReconciliationInput{Report: report, IdempotencyKey: "reconciliation-" + code}); err != nil {
			t.Fatalf("allowlisted code %q: %v", code, err)
		}
	}
	workspace := validReconciliationReport("mismatch")
	workspace["id"] = "recon-workspace-renewal"
	workspace["exceptions"].([]any)[0].(map[string]any)["resourceType"] = "workspace"
	workspace["exceptions"].([]any)[0].(map[string]any)["resourceId"] = "workspace-alpha"
	if _, err := store.RecordReconciliation(ctx, ReconciliationInput{Report: workspace, IdempotencyKey: "reconciliation-workspace-renewal"}); err != nil {
		t.Fatalf("Workspace reconciliation exception: %v", err)
	}
	multiple := validReconciliationReport("mismatch")
	multiple["id"] = "recon-multiple"
	multiple["exceptions"] = append(multiple["exceptions"].([]any), map[string]any{"resourceType": "compute", "resourceId": "compute-alpha", "code": "fabric_operation_missing"})
	multiple["counts"].(map[string]any)["exceptions"] = 2
	if _, err := store.RecordReconciliation(ctx, ReconciliationInput{Report: multiple, IdempotencyKey: "reconciliation-multiple"}); err != nil {
		t.Fatalf("multiple exceptions for one resource: %v", err)
	}
}

func TestReconciliationSchemaRejectsInvalidMemoryReplay(t *testing.T) {
	store := NewMemoryStore()
	input := ReconciliationInput{Report: validReconciliationReport("mismatch"), IdempotencyKey: "reconciliation-invalid-replay"}
	if _, err := store.RecordReconciliation(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	record := store.reconciliationIdempotency[input.IdempotencyKey]
	result := record.result.(ReconciliationResult)
	result.BlockNewWorkspaces = false
	record.result = result
	store.reconciliationIdempotency[input.IdempotencyKey] = record
	if _, err := store.RecordReconciliation(context.Background(), input); !errors.Is(err, ErrInvalidReconciliationInput) {
		t.Fatalf("invalid replay error = %v, want ErrInvalidReconciliationInput", err)
	}
}

func TestBillingReceiptIdempotencyCorrectionAndAccountWorkspaceQuery(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	input := validBillingReceiptInput()
	input.RequestID, input.IdempotencyKey = "billing-operation-alpha", "billing-receipt-alpha"
	first, err := store.RecordReceipt(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := store.RecordReceipt(ctx, input)
	if err != nil || !replayed.Replayed || replayed.ReceiptID != first.ReceiptID {
		t.Fatalf("replay=%#v err=%v", replayed, err)
	}
	correctionInput := input
	correctionInput.IdempotencyKey = "billing-receipt-alpha-correction"
	correctionInput.SupersedesReceiptID = first.ReceiptID
	correctionInput.Cost = validBillingReceiptInput().Cost
	correctionInput.Cost["chargeUsdMicros"] = int64(49_999_999)
	correction, err := store.RecordReceipt(ctx, correctionInput)
	if err != nil || correction.SupersedesReceiptID != first.ReceiptID {
		t.Fatalf("correction=%#v err=%v", correction, err)
	}
	page, err := store.ListReceipts(ctx, ReceiptQuery{AccountID: "acct-alpha", WorkspaceID: "workspace-alpha"})
	if err != nil || len(page.Receipts) != 2 {
		t.Fatalf("account/workspace receipts=%#v err=%v", page, err)
	}
	other, err := store.ListReceipts(ctx, ReceiptQuery{AccountID: "acct-other"})
	if err != nil || len(other.Receipts) != 0 {
		t.Fatalf("other account receipts=%#v err=%v", other, err)
	}
}

func TestReconciliationMismatchBlocksNewWorkspaces(t *testing.T) {
	result, err := NewMemoryStore().RecordReconciliation(context.Background(), ReconciliationInput{Report: validReconciliationReport("mismatch"), IdempotencyKey: "reconciliation-alpha"})
	if err != nil || !result.BlockNewWorkspaces || result.Status != "mismatch" {
		t.Fatalf("reconciliation=%#v err=%v", result, err)
	}
}

func TestMemoryReceiptAndReconciliationIdempotencyNamespaces(t *testing.T) {
	for _, reconciliationFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "receipt first", true: "reconciliation first"}[reconciliationFirst], func(t *testing.T) {
			store := NewMemoryStore()
			ctx := context.Background()
			key := "shared-receipt-reconciliation-key"
			receipt := validBillingReceiptInput()
			receipt.IdempotencyKey = key
			report := validReconciliationReport("ok")
			report["id"] = "shared-namespace-reconciliation"
			reconciliation := ReconciliationInput{Report: report, IdempotencyKey: key}

			if reconciliationFirst {
				if _, err := store.RecordReconciliation(ctx, reconciliation); err != nil {
					t.Fatal(err)
				}
				if _, err := store.RecordReceipt(ctx, receipt); err != nil {
					t.Fatalf("receipt collided with reconciliation namespace: %v", err)
				}
			} else {
				if _, err := store.RecordReceipt(ctx, receipt); err != nil {
					t.Fatal(err)
				}
				if _, err := store.RecordReconciliation(ctx, reconciliation); err != nil {
					t.Fatalf("reconciliation collided with receipt namespace: %v", err)
				}
			}

			changedReceipt := receipt
			changedReceipt.Cost = validBillingReceiptInput().Cost
			changedReceipt.Cost["chargeUsdMicros"] = int64(49_999_999)
			if _, err := store.RecordReceipt(ctx, changedReceipt); !errors.Is(err, ErrIdempotencyConflict) {
				t.Fatalf("changed receipt replay error = %v, want ErrIdempotencyConflict", err)
			}
			changedReport := validReconciliationReport("ok")
			changedReport["id"] = "shared-namespace-reconciliation"
			changedReport["counts"].(map[string]any)["observed"] = 1
			if _, err := store.RecordReconciliation(ctx, ReconciliationInput{Report: changedReport, IdempotencyKey: key}); !errors.Is(err, ErrIdempotencyConflict) {
				t.Fatalf("changed reconciliation replay error = %v, want ErrIdempotencyConflict", err)
			}
		})
	}
}

func TestMemoryConcurrentReconciliationIdempotency(t *testing.T) {
	store := NewMemoryStore()
	type outcome struct {
		result ReconciliationResult
		err    error
	}
	run := func(inputs []ReconciliationInput) []outcome {
		t.Helper()
		start := make(chan struct{})
		outcomes := make(chan outcome, len(inputs))
		var wg sync.WaitGroup
		for _, input := range inputs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				result, err := store.RecordReconciliation(context.Background(), input)
				outcomes <- outcome{result: result, err: err}
			}()
		}
		close(start)
		wg.Wait()
		close(outcomes)
		results := make([]outcome, 0, len(inputs))
		for result := range outcomes {
			results = append(results, result)
		}
		return results
	}

	report := validReconciliationReport("ok")
	report["id"] = "memory-concurrent-same"
	input := ReconciliationInput{Report: report, IdempotencyKey: "memory-concurrent-same"}
	results := run([]ReconciliationInput{input, input, input, input})
	created, replayed := 0, 0
	for _, result := range results {
		if result.err != nil || result.result.ID != "memory-concurrent-same" {
			t.Fatalf("same reconciliation outcome = %#v", result)
		}
		if result.result.Replayed {
			replayed++
		} else {
			created++
		}
	}
	if created != 1 || replayed != 3 {
		t.Fatalf("same reconciliation outcomes = %#v", results)
	}

	firstReport := validReconciliationReport("ok")
	firstReport["id"] = "memory-concurrent-payload"
	first := ReconciliationInput{Report: firstReport, IdempotencyKey: "memory-concurrent-payload"}
	secondReport := validReconciliationReport("ok")
	secondReport["id"] = "memory-concurrent-payload"
	secondReport["counts"].(map[string]any)["observed"] = 1
	second := ReconciliationInput{Report: secondReport, IdempotencyKey: first.IdempotencyKey}
	results = run([]ReconciliationInput{first, second})
	created, conflicts := 0, 0
	for _, result := range results {
		switch {
		case errors.Is(result.err, ErrIdempotencyConflict):
			conflicts++
		case result.err != nil:
			t.Fatalf("different reconciliation payload raw error: %v", result.err)
		default:
			created++
		}
	}
	if created != 1 || conflicts != 1 {
		t.Fatalf("different reconciliation payload outcomes = %#v", results)
	}
}

func TestAnyContinuationRequiresFullIdentity(t *testing.T) {
	for _, receiptType := range []string{"execution.receipt.v1", "workspace.created"} {
		store := NewMemoryStore()
		receipt, err := store.RecordReceipt(context.Background(), ReceiptInput{
			Type:           receiptType,
			Status:         "completed",
			Surface:        "workspace",
			WorkspaceID:    "workspace-alpha",
			ProjectID:      "project-alpha",
			TaskID:         "task-alpha",
			JobID:          "job-alpha",
			IdempotencyKey: "receipt-continuation",
			Continuation: map[string]any{
				"continuationId":          "continuation-alpha",
				"taskVersion":             float64(3),
				"requiredArtifactDigests": []any{"sha256:alpha"},
				"environmentRef":          "environment-alpha",
			},
		})
		if !errors.Is(err, ErrInvalidReceiptInput) || receipt.ReceiptID != "" {
			t.Fatalf("%s incomplete continuation = %#v, %v", receiptType, receipt, err)
		}
	}
}

func TestLegacyReceiptWithoutContinuationRemainsReadable(t *testing.T) {
	store := NewMemoryStore()
	receipt := Receipt{ReceiptInput: ReceiptInput{Type: "workspace.created", Status: "completed", Surface: "workspace", WorkspaceID: "workspace-alpha"}, ReceiptID: "receipt-legacy-created", CreatedAt: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)}
	store.receipts[receipt.ReceiptID] = receipt
	loaded, err := store.Receipt(context.Background(), receipt.ReceiptID)
	if err != nil || loaded.ReceiptID != receipt.ReceiptID || loaded.Continuation != nil || loaded.ContinuationID != "" {
		t.Fatalf("legacy receipt = %#v, %v", loaded, err)
	}
}

func TestReceiptPreservesOpaqueProvenance(t *testing.T) {
	store := NewMemoryStore()
	receipt, err := store.RecordReceipt(context.Background(), ReceiptInput{
		Type:           "execution.receipt.v1",
		Status:         "completed",
		Surface:        "workspace",
		OrganizationID: "org-alpha",
		WorkspaceID:    "workspace-alpha",
		ProjectID:      "project-alpha",
		TaskID:         "task-alpha",
		JobID:          "job-alpha",
		ArtifactID:     "artifact-alpha",
		ReviewID:       "review-alpha",
		OutputRefs:     map[string]any{"digest": "sha256:output"},
		ReviewerChecks: map[string]any{"decision": "accepted"},
		ContinuationID: "continuation-alpha",
		Continuation:   map[string]any{"taskVersion": float64(1), "freeForm": "opaque"},
		IdempotencyKey: "opaque-provenance",
	})
	if err != nil {
		t.Fatalf("record receipt: %v", err)
	}
	assertProvenance := func(value Receipt) {
		t.Helper()
		if value.ArtifactID != "artifact-alpha" || value.ReviewID != "review-alpha" || value.OutputRefs["digest"] != "sha256:output" || value.ReviewerChecks["decision"] != "accepted" || value.ContinuationID != "continuation-alpha" || value.Continuation["freeForm"] != "opaque" || value.Continuation["continuationId"] != nil {
			t.Fatalf("opaque provenance changed: %#v", value)
		}
	}
	assertProvenance(receipt)
	loaded, err := store.Receipt(context.Background(), receipt.ReceiptID)
	if err != nil {
		t.Fatal(err)
	}
	assertProvenance(loaded)
	page, err := store.ListReceipts(context.Background(), ReceiptQuery{WorkspaceID: "workspace-alpha"})
	if err != nil || len(page.Receipts) != 1 {
		t.Fatalf("receipt list = %#v, %v", page, err)
	}
	assertProvenance(page.Receipts[0])
}

func TestHistoricalEvidenceReceiptTypesAreWriteRejected(t *testing.T) {
	store := NewMemoryStore()
	for _, receiptType := range []string{"artifact.manifest.v1", "review.result.v1"} {
		_, err := store.RecordReceipt(context.Background(), ReceiptInput{Type: receiptType, Status: "completed", Surface: "ledger", WorkspaceID: "workspace-alpha", IdempotencyKey: "historical-" + receiptType})
		if !errors.Is(err, ErrInvalidReceiptInput) {
			t.Fatalf("receipt type %q error=%v, want ErrInvalidReceiptInput", receiptType, err)
		}
	}
}

func TestReceiptAcceptsTimedOutExecutionStatus(t *testing.T) {
	store := NewMemoryStore()
	receipt, err := store.RecordReceipt(context.Background(), ReceiptInput{Type: "execution.receipt.v1", Status: "timed_out", Surface: "workspace", WorkspaceID: "workspace-alpha", IdempotencyKey: "timed-out-receipt"})
	if err != nil || receipt.Status != "timed_out" {
		t.Fatalf("timed out receipt: %#v, %v", receipt, err)
	}
}

func TestListReceiptsFiltersAndPaginatesNewestFirst(t *testing.T) {
	store := NewMemoryStore()
	createdAt := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)
	store.receipts = map[string]Receipt{
		"receipt-a":     {ReceiptInput: ReceiptInput{Type: "execution.receipt.v1", Status: "completed", OrganizationID: "org-alpha", WorkspaceID: "ws-alpha", ProjectID: "project-alpha", TaskID: "task-alpha", JobID: "job-alpha"}, ReceiptID: "receipt-a", CreatedAt: createdAt.Add(-time.Minute)},
		"receipt-b":     {ReceiptInput: ReceiptInput{Type: "execution.receipt.v1", Status: "completed", OrganizationID: "org-alpha", WorkspaceID: "ws-alpha", ProjectID: "project-alpha", TaskID: "task-alpha", JobID: "job-alpha"}, ReceiptID: "receipt-b", CreatedAt: createdAt},
		"receipt-c":     {ReceiptInput: ReceiptInput{Type: "review.result.v1", Status: "review_blocked", OrganizationID: "org-alpha", WorkspaceID: "ws-alpha", ProjectID: "project-alpha", TaskID: "task-alpha", JobID: "job-alpha"}, ReceiptID: "receipt-c", CreatedAt: createdAt},
		"receipt-other": {ReceiptInput: ReceiptInput{Type: "execution.receipt.v1", Status: "completed", OrganizationID: "org-other", WorkspaceID: "ws-alpha", ProjectID: "project-alpha", TaskID: "task-alpha", JobID: "job-alpha"}, ReceiptID: "receipt-other", CreatedAt: createdAt.Add(time.Minute)},
	}

	first, err := store.ListReceipts(context.Background(), ReceiptQuery{OrganizationID: "org-alpha", WorkspaceID: "ws-alpha", ProjectID: "project-alpha", TaskID: "task-alpha", JobID: "job-alpha", Type: "execution.receipt.v1", Status: "completed", Limit: 1})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(first.Receipts) != 1 || first.Receipts[0].ReceiptID != "receipt-b" || !first.HasMore || first.NextCursor == "" {
		t.Fatalf("first page = %#v", first)
	}
	second, err := store.ListReceipts(context.Background(), ReceiptQuery{OrganizationID: "org-alpha", WorkspaceID: "ws-alpha", ProjectID: "project-alpha", TaskID: "task-alpha", JobID: "job-alpha", Type: "execution.receipt.v1", Status: "completed", Limit: 1, Cursor: first.NextCursor})
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(second.Receipts) != 1 || second.Receipts[0].ReceiptID != "receipt-a" || second.HasMore || second.NextCursor != "" {
		t.Fatalf("second page = %#v", second)
	}
}

func TestReceiptQueryTypePrefixFiltersBeforePagination(t *testing.T) {
	store := NewMemoryStore()
	createdAt := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)
	store.receipts = map[string]Receipt{
		"receipt-billing-new": {ReceiptInput: ReceiptInput{AccountID: "acct-billing-prefix", Type: "billing.workspace_renewed.v1", Status: "completed"}, ReceiptID: "receipt-billing-new", CreatedAt: createdAt},
		"receipt-execution":   {ReceiptInput: ReceiptInput{AccountID: "acct-billing-prefix", Type: "execution.receipt.v1", Status: "completed"}, ReceiptID: "receipt-execution", CreatedAt: createdAt.Add(-time.Minute)},
		"receipt-billing-old": {ReceiptInput: ReceiptInput{AccountID: "acct-billing-prefix", Type: "billing.workspace_purchased.v1", Status: "completed"}, ReceiptID: "receipt-billing-old", CreatedAt: createdAt.Add(-2 * time.Minute)},
	}

	first, err := store.ListReceipts(context.Background(), ReceiptQuery{AccountID: "acct-billing-prefix", TypePrefix: "billing.", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Receipts) != 1 || first.Receipts[0].ReceiptID != "receipt-billing-new" || !first.HasMore || first.NextCursor == "" {
		t.Fatalf("first billing page = %#v", first)
	}
	second, err := store.ListReceipts(context.Background(), ReceiptQuery{AccountID: "acct-billing-prefix", TypePrefix: "billing.", Limit: 1, Cursor: first.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Receipts) != 1 || second.Receipts[0].ReceiptID != "receipt-billing-old" || second.HasMore || second.NextCursor != "" {
		t.Fatalf("second billing page = %#v", second)
	}
}

func TestListReceiptsRejectsInvalidBoundsAndCursor(t *testing.T) {
	store := NewMemoryStore()
	for _, query := range []ReceiptQuery{{Limit: -1}, {Limit: 101}, {Cursor: "not-a-cursor"}} {
		if _, err := store.ListReceipts(context.Background(), query); !errors.Is(err, ErrInvalidReceiptQuery) {
			t.Fatalf("query %#v error = %v, want ErrInvalidReceiptQuery", query, err)
		}
	}
}
