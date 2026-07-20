package ledger

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"time"
)

var ErrIdempotencyConflict = errors.New("idempotency key already used with different payload")
var ErrReceiptNotFound = errors.New("receipt not found")
var ErrContinuationNotFound = errors.New("continuation not found")
var ErrContinuationIneligible = errors.New("continuation is not eligible")
var ErrInvalidReceiptInput = errors.New("invalid receipt input")
var ErrInvalidReceiptQuery = errors.New("invalid receipt query")
var ErrArtifactNotFound = errors.New("artifact not found")
var ErrInvalidArtifactInput = errors.New("invalid artifact input")
var ErrReviewNotFound = errors.New("review not found")
var ErrInvalidReviewInput = errors.New("invalid review input")
var ErrReviewPolicyNotFound = errors.New("review policy not found")
var ErrInvalidReviewPolicyInput = errors.New("invalid review policy input")
var ErrInvalidReviewGateInput = errors.New("invalid review gate input")
var ErrInvalidReceiptRetentionInput = errors.New("invalid receipt retention input")
var ErrReceiptRetentionShortening = errors.New("receipt retention cannot be shortened")
var ErrReceiptRetentionActive = errors.New("receipt retention is active")
var ErrReceiptLegalHold = errors.New("receipt is under legal hold")
var ErrInvalidReconciliationInput = errors.New("invalid reconciliation input")

const artifactReceiptType = "artifact.manifest.v1"
const reviewReceiptType = "review.result.v1"

type ReceiptInput struct {
	Type                string         `json:"type"`
	Status              string         `json:"status"`
	Surface             string         `json:"surface"`
	AccountID           string         `json:"accountId,omitempty"`
	OrganizationID      string         `json:"organizationId"`
	WorkspaceID         string         `json:"workspaceId"`
	ProjectID           string         `json:"projectId"`
	TaskID              string         `json:"taskId"`
	RequestID           string         `json:"requestId"`
	ApprovalID          string         `json:"approvalId"`
	JobID               string         `json:"jobId"`
	ArtifactID          string         `json:"artifactId"`
	ReviewID            string         `json:"reviewId"`
	ContinuationID      string         `json:"continuationId"`
	Actor               map[string]any `json:"actor"`
	Plan                map[string]any `json:"plan"`
	Execution           map[string]any `json:"execution"`
	Environment         map[string]any `json:"environment"`
	InputRefs           map[string]any `json:"inputRefs"`
	OutputRefs          map[string]any `json:"outputRefs"`
	ReviewerChecks      map[string]any `json:"reviewerChecks"`
	Cost                map[string]any `json:"cost"`
	Owner               map[string]any `json:"owner"`
	Continuation        map[string]any `json:"continuation"`
	SupersedesReceiptID string         `json:"supersedesReceiptId"`
	IdempotencyKey      string         `json:"-"`
}

type ReceiptRetention struct {
	RetainUntil      time.Time                 `json:"retainUntil,omitempty"`
	LegalHold        bool                      `json:"legalHold"`
	PrivacyRedaction *PrivacyRedactionEvidence `json:"privacyRedaction,omitempty"`
}

func (retention ReceiptRetention) MarshalJSON() ([]byte, error) {
	type retentionJSON struct {
		RetainUntil      *time.Time                `json:"retainUntil,omitempty"`
		LegalHold        bool                      `json:"legalHold"`
		PrivacyRedaction *PrivacyRedactionEvidence `json:"privacyRedaction,omitempty"`
	}
	var retainUntil *time.Time
	if !retention.RetainUntil.IsZero() {
		value := retention.RetainUntil
		retainUntil = &value
	}
	return json.Marshal(retentionJSON{RetainUntil: retainUntil, LegalHold: retention.LegalHold, PrivacyRedaction: retention.PrivacyRedaction})
}

type PrivacyRedactionEvidence struct {
	AppliedAt time.Time `json:"appliedAt"`
	Reason    string    `json:"reason"`
	Eligible  bool      `json:"eligible"`
}

type ReceiptRetentionInput struct {
	ReceiptID      string    `json:"-"`
	RetainUntil    time.Time `json:"retainUntil,omitempty"`
	LegalHold      bool      `json:"legalHold"`
	IdempotencyKey string    `json:"-"`
}

type ReceiptPrivacyDeleteInput struct {
	ReceiptID      string `json:"-"`
	Reason         string `json:"reason"`
	IdempotencyKey string `json:"-"`
}

type ReceiptRetentionResult struct {
	ReceiptID string           `json:"receiptId"`
	Retention ReceiptRetention `json:"retention"`
	Replayed  bool             `json:"replayed"`
}

type Receipt struct {
	ReceiptInput
	ReceiptID string           `json:"receiptId"`
	CreatedAt time.Time        `json:"createdAt"`
	Retention ReceiptRetention `json:"retention"`
	Replayed  bool             `json:"replayed"`
}

const (
	DefaultReceiptPageSize = 50
	MaxReceiptPageSize     = 100
)

type ReceiptQuery struct {
	AccountID      string
	OrganizationID string
	WorkspaceID    string
	ProjectID      string
	TaskID         string
	JobID          string
	Type           string
	Status         string
	Cursor         string
	Limit          int
}

type ReceiptPage struct {
	Receipts   []Receipt `json:"receipts"`
	NextCursor string    `json:"nextCursor"`
	HasMore    bool      `json:"hasMore"`
}

type receiptCursor struct {
	CreatedAt time.Time `json:"createdAt"`
	ReceiptID string    `json:"receiptId"`
}

func normalizeReceiptQuery(query ReceiptQuery) (ReceiptQuery, receiptCursor, error) {
	if query.Limit == 0 {
		query.Limit = DefaultReceiptPageSize
	}
	if query.Limit < 1 || query.Limit > MaxReceiptPageSize {
		return ReceiptQuery{}, receiptCursor{}, ErrInvalidReceiptQuery
	}
	if query.Cursor == "" {
		return query, receiptCursor{}, nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(query.Cursor)
	if err != nil {
		return ReceiptQuery{}, receiptCursor{}, ErrInvalidReceiptQuery
	}
	var cursor receiptCursor
	if err := json.Unmarshal(payload, &cursor); err != nil || cursor.CreatedAt.IsZero() || cursor.ReceiptID == "" {
		return ReceiptQuery{}, receiptCursor{}, ErrInvalidReceiptQuery
	}
	return query, cursor, nil
}

func encodeReceiptCursor(receipt Receipt) string {
	payload, _ := json.Marshal(receiptCursor{CreatedAt: receipt.CreatedAt, ReceiptID: receipt.ReceiptID})
	return base64.RawURLEncoding.EncodeToString(payload)
}

type ArtifactInput struct {
	OrganizationID string `json:"organizationId"`
	WorkspaceID    string `json:"workspaceId"`
	ProjectID      string `json:"projectId"`
	TaskID         string `json:"taskId"`
	JobID          string `json:"jobId"`
	Digest         string `json:"digest"`
	MediaType      string `json:"mediaType"`
	SizeBytes      int64  `json:"sizeBytes"`
	StorageRef     string `json:"storageRef"`
	IdempotencyKey string `json:"-"`
}

type Artifact struct {
	ArtifactInput
	ArtifactID string    `json:"artifactId"`
	ReceiptID  string    `json:"receiptId"`
	CreatedAt  time.Time `json:"createdAt"`
	Replayed   bool      `json:"replayed"`
}

type ReviewInput struct {
	OrganizationID       string         `json:"organizationId"`
	WorkspaceID          string         `json:"workspaceId"`
	ProjectID            string         `json:"projectId"`
	TaskID               string         `json:"taskId"`
	JobID                string         `json:"jobId"`
	ReviewerRef          string         `json:"reviewerRef"`
	ReviewerVersion      string         `json:"reviewerVersion"`
	InputArtifactDigests []string       `json:"inputArtifactDigests"`
	Checks               map[string]any `json:"checks"`
	Decision             string         `json:"decision"`
	IdempotencyKey       string         `json:"-"`
}

type Review struct {
	ReviewInput
	ReviewID  string    `json:"reviewId"`
	ReceiptID string    `json:"receiptId"`
	CreatedAt time.Time `json:"createdAt"`
	Replayed  bool      `json:"replayed"`
}

type ExecutionIdentity struct {
	OrganizationID string `json:"organizationId"`
	WorkspaceID    string `json:"workspaceId"`
	ProjectID      string `json:"projectId"`
	TaskID         string `json:"taskId"`
	JobID          string `json:"jobId"`
}

type RequiredReviewer struct {
	ReviewerRef     string `json:"reviewerRef"`
	ReviewerVersion string `json:"reviewerVersion"`
}

type ReviewPolicyInput struct {
	ExecutionIdentity
	Version            string             `json:"version"`
	RequiredReviewers  []RequiredReviewer `json:"requiredReviewers"`
	SupersedesPolicyID string             `json:"supersedesPolicyId,omitempty"`
	IdempotencyKey     string             `json:"-"`
}

type ReviewPolicy struct {
	ReviewPolicyInput
	PolicyID  string    `json:"policyId"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
	Replayed  bool      `json:"replayed"`
}

type ReviewPolicyQuery struct {
	ExecutionIdentity
	Status string
}

type ReviewGateInput struct {
	ExecutionIdentity
	ReviewIDs []string `json:"reviewIds"`
}

type ReviewGateEvidence struct {
	ReviewerRef     string `json:"reviewerRef"`
	RequiredVersion string `json:"requiredVersion"`
	ReviewID        string `json:"reviewId,omitempty"`
	ActualVersion   string `json:"actualVersion,omitempty"`
}

type ReviewGateResult struct {
	PolicyID             string               `json:"policyId"`
	PolicyVersion        string               `json:"policyVersion"`
	Status               string               `json:"status"`
	ContinuationEligible bool                 `json:"continuationEligible"`
	Missing              []ReviewGateEvidence `json:"missing"`
	Pending              []ReviewGateEvidence `json:"pending"`
	Rejected             []ReviewGateEvidence `json:"rejected"`
	VersionMismatches    []ReviewGateEvidence `json:"versionMismatches"`
}

func validateArtifactInput(input ArtifactInput) error {
	if input.OrganizationID == "" || input.WorkspaceID == "" || input.ProjectID == "" || input.TaskID == "" || input.JobID == "" || input.Digest == "" || input.MediaType == "" || input.SizeBytes < 0 || input.IdempotencyKey == "" || !isOpaqueReference(input.StorageRef) {
		return ErrInvalidArtifactInput
	}
	return nil
}

func validateReviewInput(input ReviewInput) error {
	if input.OrganizationID == "" || input.WorkspaceID == "" || input.ProjectID == "" || input.TaskID == "" || input.JobID == "" || !isOpaqueReference(input.ReviewerRef) || !isOpaqueReference(input.ReviewerVersion) || len(input.InputArtifactDigests) == 0 || len(input.Checks) == 0 || input.IdempotencyKey == "" || (input.Decision != "accepted" && input.Decision != "pending" && input.Decision != "rejected") || containsForbiddenReceiptKey(input.Checks) {
		return ErrInvalidReviewInput
	}
	return nil
}

func reviewReceiptStatus(decision string) string {
	if decision == "rejected" {
		return "review_blocked"
	}
	if decision == "pending" {
		return "review_required"
	}
	return "completed"
}

func validateReviewPolicyInput(input ReviewPolicyInput) error {
	if !validExecutionIdentity(input.ExecutionIdentity) || !isOpaqueReference(input.Version) || input.IdempotencyKey == "" || len(input.RequiredReviewers) == 0 || (input.SupersedesPolicyID != "" && !isOpaqueReference(input.SupersedesPolicyID)) {
		return ErrInvalidReviewPolicyInput
	}
	seen := make(map[string]struct{}, len(input.RequiredReviewers))
	for _, required := range input.RequiredReviewers {
		if !isOpaqueReference(required.ReviewerRef) || !isOpaqueReference(required.ReviewerVersion) {
			return ErrInvalidReviewPolicyInput
		}
		if _, exists := seen[required.ReviewerRef]; exists {
			return ErrInvalidReviewPolicyInput
		}
		seen[required.ReviewerRef] = struct{}{}
	}
	return nil
}

func validExecutionIdentity(identity ExecutionIdentity) bool {
	return identity.OrganizationID != "" && identity.WorkspaceID != "" && identity.ProjectID != "" && identity.TaskID != "" && identity.JobID != ""
}

func sameExecutionIdentity(left, right ExecutionIdentity) bool {
	return left == right
}

func evaluateReviewGate(policy ReviewPolicy, reviews []Review) ReviewGateResult {
	result := ReviewGateResult{
		PolicyID: policy.PolicyID, PolicyVersion: policy.Version, Status: "accepted", ContinuationEligible: true,
		Missing: []ReviewGateEvidence{}, Pending: []ReviewGateEvidence{}, Rejected: []ReviewGateEvidence{}, VersionMismatches: []ReviewGateEvidence{},
	}
	for _, required := range policy.RequiredReviewers {
		var mismatch *Review
		var pending *Review
		var rejected *Review
		accepted := false
		for i := range reviews {
			review := &reviews[i]
			if review.ReviewerRef != required.ReviewerRef || !sameExecutionIdentity(policy.ExecutionIdentity, executionIdentityFromReview(*review)) {
				continue
			}
			if review.ReviewerVersion != required.ReviewerVersion {
				if mismatch == nil {
					mismatch = review
				}
				continue
			}
			switch review.Decision {
			case "accepted":
				accepted = true
			case "pending":
				pending = review
			case "rejected":
				rejected = review
			}
		}
		evidence := ReviewGateEvidence{ReviewerRef: required.ReviewerRef, RequiredVersion: required.ReviewerVersion}
		if rejected != nil {
			evidence.ReviewID = rejected.ReviewID
			evidence.ActualVersion = rejected.ReviewerVersion
			result.Rejected = append(result.Rejected, evidence)
			continue
		}
		if accepted {
			continue
		}
		if pending != nil {
			evidence.ReviewID = pending.ReviewID
			evidence.ActualVersion = pending.ReviewerVersion
			result.Pending = append(result.Pending, evidence)
			continue
		}
		if mismatch != nil {
			evidence.ReviewID = mismatch.ReviewID
			evidence.ActualVersion = mismatch.ReviewerVersion
			result.VersionMismatches = append(result.VersionMismatches, evidence)
			continue
		}
		result.Missing = append(result.Missing, evidence)
	}
	if len(result.Rejected) > 0 || len(result.VersionMismatches) > 0 {
		result.Status = "review_blocked"
		result.ContinuationEligible = false
	} else if len(result.Missing) > 0 || len(result.Pending) > 0 {
		result.Status = "review_required"
		result.ContinuationEligible = false
	}
	return result
}

func executionIdentityFromReview(review Review) ExecutionIdentity {
	return ExecutionIdentity{OrganizationID: review.OrganizationID, WorkspaceID: review.WorkspaceID, ProjectID: review.ProjectID, TaskID: review.TaskID, JobID: review.JobID}
}

func executionIdentityFromReceipt(receipt Receipt) ExecutionIdentity {
	return ExecutionIdentity{OrganizationID: receipt.OrganizationID, WorkspaceID: receipt.WorkspaceID, ProjectID: receipt.ProjectID, TaskID: receipt.TaskID, JobID: receipt.JobID}
}

func receiptForRead(receipt Receipt, gate ReviewGateResult, gateErr error) Receipt {
	if receipt.Continuation == nil && receipt.ContinuationID == "" {
		return receipt
	}
	if validExecutionIdentity(executionIdentityFromReceipt(receipt)) && gateErr == nil && gate.ContinuationEligible {
		return receipt
	}
	receipt.ContinuationID = ""
	receipt.Continuation = nil
	return receipt
}

func isOpaqueReference(value string) bool {
	if value == "" || len(value) > 255 {
		return false
	}
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("._:-", r)) {
			return false
		}
	}
	return true
}

func evidenceID(prefix, idempotencyKey string) string {
	digest := sha256.Sum256([]byte(idempotencyKey))
	return prefix + "-" + hex.EncodeToString(digest[:8])
}

func artifactFromReceipt(receipt Receipt) Artifact {
	return Artifact{
		ArtifactInput: ArtifactInput{
			OrganizationID: receipt.OrganizationID,
			WorkspaceID:    receipt.WorkspaceID,
			ProjectID:      receipt.ProjectID,
			TaskID:         receipt.TaskID,
			JobID:          receipt.JobID,
			Digest:         stringValue(receipt.OutputRefs["digest"]),
			MediaType:      stringValue(receipt.OutputRefs["mediaType"]),
			SizeBytes:      int64Value(receipt.OutputRefs["sizeBytes"]),
			StorageRef:     stringValue(receipt.OutputRefs["storageRef"]),
		},
		ArtifactID: receipt.ArtifactID,
		ReceiptID:  receipt.ReceiptID,
		CreatedAt:  receipt.CreatedAt,
		Replayed:   receipt.Replayed,
	}
}

func reviewFromReceipt(receipt Receipt) Review {
	checks, _ := receipt.ReviewerChecks["checks"].(map[string]any)
	return Review{
		ReviewInput: ReviewInput{
			OrganizationID:       receipt.OrganizationID,
			WorkspaceID:          receipt.WorkspaceID,
			ProjectID:            receipt.ProjectID,
			TaskID:               receipt.TaskID,
			JobID:                receipt.JobID,
			ReviewerRef:          stringValue(receipt.ReviewerChecks["reviewerRef"]),
			ReviewerVersion:      stringValue(receipt.ReviewerChecks["reviewerVersion"]),
			InputArtifactDigests: stringSlice(receipt.ReviewerChecks["inputArtifactDigests"]),
			Checks:               checks,
			Decision:             stringValue(receipt.ReviewerChecks["decision"]),
		},
		ReviewID:  receipt.ReviewID,
		ReceiptID: receipt.ReceiptID,
		CreatedAt: receipt.CreatedAt,
		Replayed:  receipt.Replayed,
	}
}

func stringValue(value any) string {
	result, _ := value.(string)
	return result
}

func int64Value(value any) int64 {
	switch typed := value.(type) {
	case int64:
		return typed
	case float64:
		return int64(typed)
	case json.Number:
		result, _ := typed.Int64()
		return result
	default:
		return 0
	}
}

func stringSlice(value any) []string {
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				result = append(result, text)
			}
		}
		return result
	default:
		return nil
	}
}

func finalizeReceiptContinuation(receipt *Receipt) {
	if receipt.ContinuationID == "" && len(receipt.Continuation) == 0 {
		return
	}
	continuation := make(map[string]any, len(receipt.Continuation)+1)
	for key, value := range receipt.Continuation {
		continuation[key] = value
	}
	continuationID, _ := continuation["continuationId"].(string)
	if continuationID == "" {
		continuationID = receipt.ContinuationID
	}
	if continuationID == "" {
		continuationID = "continuation-" + receipt.ReceiptID
	}
	receipt.ContinuationID = continuationID
	continuation["continuationId"] = continuationID
	receipt.Continuation = continuation
}

func continuationFromReceipt(receipt Receipt) (map[string]any, error) {
	continuation := make(map[string]any, len(receipt.Continuation)+4)
	for key, value := range receipt.Continuation {
		continuation[key] = value
	}
	continuationID, _ := continuation["continuationId"].(string)
	if continuationID == "" {
		continuationID = receipt.ContinuationID
	}
	if continuationID == "" {
		return nil, ErrContinuationNotFound
	}
	continuation["continuationId"] = continuationID
	continuation["receiptId"] = receipt.ReceiptID
	continuation["projectId"] = receipt.ProjectID
	continuation["taskId"] = receipt.TaskID
	return continuation, nil
}

func validateReceiptInput(input ReceiptInput) error {
	if input.Type == "" || input.Status == "" || input.Surface == "" || input.IdempotencyKey == "" || input.WorkspaceID == "" && input.Type != "gateway.wallet_adjustment.v1" {
		return ErrInvalidReceiptInput
	}
	if (input.ContinuationID != "" || len(input.Continuation) > 0) && !validExecutionIdentity(executionIdentityFromReceipt(Receipt{ReceiptInput: input})) {
		return ErrInvalidReceiptInput
	}
	allowedStatus := map[string]bool{"planned": true, "approved": true, "running": true, "completed": true, "failed": true, "timed_out": true, "cancelled": true, "review_required": true, "review_blocked": true}
	billingCostValid := true
	if strings.HasPrefix(input.Type, "billing.") && strings.HasSuffix(input.Type, ".v1") {
		switch input.Type {
		case "billing.resource_purchased.v1", "billing.resource_renewed.v1", "billing.resource_expired.v1", "billing.resource_refunded.v1", "billing.charge_review_required.v1", "billing.reconciliation.v1":
			billingCostValid = validBillingCost(input.Cost)
		case "billing.workspace_purchased.v1", "billing.workspace_renewed.v1", "billing.workspace_expired.v1", "billing.workspace_refunded.v1":
			billingCostValid = validWorkspaceBillingCost(input.Cost, input.Type) && input.Cost["resourceId"] == input.WorkspaceID
		default:
			billingCostValid = false
		}
	}
	rotationEvidenceValid := input.Type != "workspace.gateway_key_rotated.v1" || validWorkspaceGatewayKeyRotationReceipt(input)
	walletAdjustmentValid := input.Type != "gateway.wallet_adjustment.v1" || validWalletAdjustmentReceipt(input)
	if !allowedStatus[input.Status] || containsForbiddenReceiptKey(input) || !billingCostValid || !rotationEvidenceValid || !walletAdjustmentValid {
		return ErrInvalidReceiptInput
	}
	return nil
}

func validWalletAdjustmentReceipt(input ReceiptInput) bool {
	if input.Status != "completed" || input.Surface != "control_plane" || input.AccountID == "" || input.WorkspaceID != "" ||
		input.OrganizationID != "" || input.ProjectID != "" || input.TaskID != "" || input.ApprovalID != "" || input.JobID != "" ||
		input.ArtifactID != "" || input.ReviewID != "" || input.ContinuationID != "" || input.SupersedesReceiptID != "" || input.RequestID == "" ||
		len(input.Actor) != 1 || len(input.Execution) != 3 || len(input.Owner) != 1 || len(input.Plan) != 0 || len(input.Environment) != 0 ||
		len(input.OutputRefs) != 0 || len(input.ReviewerChecks) != 0 || len(input.Cost) != 0 || len(input.Continuation) != 0 {
		return false
	}
	operationID, operationOK := input.Execution["operationId"].(string)
	kind, kindOK := input.Execution["kind"].(string)
	amount, amountOK := integerValue(input.Execution["amountUsdMicros"])
	actor, actorOK := input.Actor["userId"].(string)
	owner, ownerOK := input.Owner["accountId"].(string)
	historyRef, historyOK := input.InputRefs["balanceHistoryRef"].(string)
	if !operationOK || !isOpaqueReference(operationID) || input.RequestID != operationID || !kindOK || amount <= 0 || !amountOK ||
		!actorOK || !isOpaqueReference(actor) || !ownerOK || owner != input.AccountID || !historyOK || !isOpaqueReference(historyRef) {
		return false
	}
	related, hasRelated := input.InputRefs["relatedOperationId"].(string)
	switch kind {
	case "recharge", "debit":
		return len(input.InputRefs) == 1 && !hasRelated
	case "business_refund":
		return len(input.InputRefs) == 2 && hasRelated && isOpaqueReference(related)
	default:
		return false
	}
}

func validWorkspaceGatewayKeyRotationReceipt(input ReceiptInput) bool {
	if input.Status != "completed" || input.Surface != "control_plane" || input.AccountID == "" || len(input.Execution) != 3 || len(input.OutputRefs) != 1 || len(input.Owner) != 1 {
		return false
	}
	operationID, operationOK := input.Execution["operationId"].(string)
	oldKeyID, oldOK := integerValue(input.Execution["oldKeyId"])
	newKeyID, newOK := integerValue(input.Execution["newKeyId"])
	fingerprint, fingerprintOK := input.OutputRefs["secretFingerprint"].(string)
	ownerID, ownerOK := input.Owner["userId"].(string)
	return operationOK && strings.TrimSpace(operationID) != "" && oldOK && newOK && oldKeyID > 0 && newKeyID > 0 && oldKeyID != newKeyID &&
		fingerprintOK && strings.TrimSpace(fingerprint) != "" && ownerOK && strings.TrimSpace(ownerID) != ""
}

func containsForbiddenReceiptKey(value any) bool {
	normalized, err := normalizedJSONValue(value)
	return err != nil || containsForbiddenJSONKey(normalized)
}

func containsForbiddenJSONKey(value any) bool {
	forbidden := map[string]bool{
		"apikey": true, "admintoken": true, "rawsub2apiresponse": true, "rawproviderresponse": true,
		"rawcredential": true, "credential": true, "password": true, "token": true, "secret": true,
		"signedurl": true, "presignedurl": true, "objectkey": true, "kubeconfig": true,
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if forbidden[strings.ToLower(key)] || containsForbiddenJSONKey(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if containsForbiddenJSONKey(child) {
				return true
			}
		}
	}
	return false
}

func normalizedJSONValue(value any) (any, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var normalized any
	err = decoder.Decode(&normalized)
	return normalized, err
}

func validBillingCost(cost map[string]any) bool {
	for _, key := range []string{"pricingVersion", "sub2apiRedeemCode", "resourceType", "resourceId"} {
		value, ok := cost[key].(string)
		if !ok || !isOpaqueReference(value) {
			return false
		}
	}
	for _, key := range []string{"periodStart", "paidThrough"} {
		value, ok := cost[key].(string)
		if !ok {
			return false
		}
		if _, err := time.Parse(time.RFC3339, value); err != nil {
			return false
		}
	}
	for _, key := range []string{"monthlyPriceCnyCents", "chargeUsdMicros"} {
		value, ok := integerValue(cost[key])
		if !ok || value < 0 {
			return false
		}
	}
	userID, ok := integerValue(cost["sub2apiUserId"])
	return ok && userID > 0
}

func validWorkspaceBillingCost(cost map[string]any, receiptType string) bool {
	wantFields := map[string]int{
		"billing.workspace_purchased.v1": 12,
		"billing.workspace_renewed.v1":   12,
		"billing.workspace_expired.v1":   9,
		"billing.workspace_refunded.v1":  13,
	}[receiptType]
	if len(cost) != wantFields || cost["currency"] != "USD" || cost["billingUnit"] != "calendar_month" || cost["resourceType"] != "workspace" {
		return false
	}
	for _, key := range []string{"priceVersion", "resourceId"} {
		value, ok := cost[key].(string)
		if !ok || !isOpaqueReference(value) {
			return false
		}
	}
	periodStartText, periodOK := cost["periodStart"].(string)
	paidThroughText, paidOK := cost["paidThrough"].(string)
	periodStart, periodErr := time.Parse(time.RFC3339, periodStartText)
	paidThrough, paidErr := time.Parse(time.RFC3339, paidThroughText)
	if !periodOK || !paidOK || periodErr != nil || paidErr != nil || !paidThrough.After(periodStart) {
		return false
	}
	total, totalOK := integerValue(cost["totalUsdMicros"])
	components, componentsOK := cost["components"].(map[string]any)
	if !totalOK || total <= 0 || !componentsOK || len(components) != 2 {
		return false
	}
	compute, computeOK := components["compute"].(map[string]any)
	storage, storageOK := components["storage"].(map[string]any)
	if !computeOK || !storageOK || len(compute) != 3 || len(storage) != 4 || compute["resourceType"] != "compute" || storage["resourceType"] != "storage" {
		return false
	}
	for _, component := range []map[string]any{compute, storage} {
		resourceID, ok := component["resourceId"].(string)
		if !ok || !isOpaqueReference(resourceID) {
			return false
		}
	}
	computeCost, computeCostOK := integerValue(compute["chargeUsdMicros"])
	storageCost, storageCostOK := integerValue(storage["chargeUsdMicros"])
	storageGB, storageGBOK := integerValue(storage["sizeGb"])
	if !computeCostOK || !storageCostOK || !storageGBOK || computeCost <= 0 || storageCost <= 0 || storageGB <= 0 || computeCost > math.MaxInt64-storageCost || computeCost+storageCost != total {
		return false
	}
	if receiptType == "billing.workspace_expired.v1" {
		return true
	}
	userID, userOK := integerValue(cost["sub2apiUserId"])
	redeemCode, redeemOK := cost["sub2apiRedeemCode"].(string)
	if !userOK || userID <= 0 || !redeemOK || !isOpaqueReference(redeemCode) {
		return false
	}
	if receiptType == "billing.workspace_purchased.v1" || receiptType == "billing.workspace_renewed.v1" {
		postCharge, ok := integerValue(cost["postChargeBalanceUsdMicros"])
		return ok && postCharge >= 0
	}
	refund, refundOK := integerValue(cost["refundUsdMicros"])
	refundCode, refundCodeOK := cost["sub2apiRefundCode"].(string)
	return refundOK && refund == total && refundCodeOK && isOpaqueReference(refundCode)
}

func integerValue(value any) (int64, bool) {
	if number, ok := value.(json.Number); ok {
		parsed, err := number.Int64()
		return parsed, err == nil
	}
	reflected := reflect.ValueOf(value)
	if !reflected.IsValid() {
		return 0, false
	}
	switch reflected.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return reflected.Int(), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if reflected.Uint() > uint64(1<<63-1) {
			return 0, false
		}
		return int64(reflected.Uint()), true
	case reflect.Float32, reflect.Float64:
		value := reflected.Float()
		maxExactInteger := float64((1 << 53) - 1)
		if reflected.Kind() == reflect.Float32 {
			maxExactInteger = (1 << 24) - 1
		}
		if math.IsNaN(value) || math.IsInf(value, 0) || value != math.Trunc(value) || math.Abs(value) > maxExactInteger {
			return 0, false
		}
		return int64(value), true
	default:
		return 0, false
	}
}

var reconciliationExceptionCodes = map[string]bool{
	"billing_operation_invalid":           true,
	"sub2api_balance_history_unavailable": true,
	"sub2api_charge_missing":              true,
	"sub2api_charge_mismatch":             true,
	"fabric_operations_unavailable":       true,
	"fabric_operation_missing":            true,
	"fabric_operation_mismatch":           true,
	"ledger_receipts_unavailable":         true,
	"ledger_receipt_missing":              true,
	"ledger_receipt_mismatch":             true,
}

func validateReconciliationInput(input ReconciliationInput) error {
	if strings.TrimSpace(input.IdempotencyKey) == "" || validateReconciliationReport(input.Report) != nil {
		return ErrInvalidReconciliationInput
	}
	return nil
}

func validateReconciliationReport(report map[string]any) error {
	normalizedValue, err := normalizedJSONValue(report)
	if err != nil {
		return ErrInvalidReconciliationInput
	}
	normalized, ok := normalizedValue.(map[string]any)
	if !ok || containsForbiddenJSONKey(normalized) {
		return ErrInvalidReconciliationInput
	}
	id, idOK := normalized["id"].(string)
	status, statusOK := normalized["status"].(string)
	counts, countsOK := report["counts"].(map[string]any)
	exceptions, exceptionsOK := normalized["exceptions"].([]any)
	if !idOK || strings.TrimSpace(id) == "" || !statusOK || (status != "ok" && status != "mismatch") || !countsOK || !exceptionsOK {
		return ErrInvalidReconciliationInput
	}
	checked, checkedOK := integerValue(counts["billingOperations"])
	matched, matchedOK := integerValue(counts["matched"])
	exceptionCount, exceptionCountOK := integerValue(counts["exceptions"])
	if !checkedOK || !matchedOK || !exceptionCountOK || checked < 0 || matched < 0 || exceptionCount < 0 || matched > checked || exceptionCount != int64(len(exceptions)) {
		return ErrInvalidReconciliationInput
	}
	for _, value := range counts {
		count, ok := integerValue(value)
		if !ok || count < 0 {
			return ErrInvalidReconciliationInput
		}
	}
	exceptionResources := map[string]bool{}
	for _, value := range exceptions {
		exception, ok := value.(map[string]any)
		if !ok {
			return ErrInvalidReconciliationInput
		}
		resourceType, resourceTypeOK := exception["resourceType"].(string)
		resourceID, resourceIDOK := exception["resourceId"].(string)
		code, codeOK := exception["code"].(string)
		if !resourceTypeOK || (resourceType != "compute" && resourceType != "storage" && resourceType != "workspace") || !resourceIDOK || !isOpaqueReference(resourceID) || !codeOK || !reconciliationExceptionCodes[code] {
			return ErrInvalidReconciliationInput
		}
		exceptionResources[resourceType+"\x00"+resourceID] = true
	}
	if checked-matched != int64(len(exceptionResources)) || (status == "ok" && len(exceptions) != 0) || (status == "mismatch" && len(exceptions) == 0) {
		return ErrInvalidReconciliationInput
	}
	return nil
}

func validateReconciliationResult(result ReconciliationResult) error {
	if validateReconciliationReport(result.Report) != nil {
		return ErrInvalidReconciliationInput
	}
	id, _ := result.Report["id"].(string)
	status, _ := result.Report["status"].(string)
	if result.ID != id || result.Status != status || result.BlockNewWorkspaces != (status == "mismatch") || result.Reason != "operator_reconciliation" {
		return ErrInvalidReconciliationInput
	}
	return nil
}

type ReconciliationInput struct {
	Report         map[string]any `json:"report"`
	IdempotencyKey string         `json:"-"`
}

type ReconciliationResult struct {
	ID                 string         `json:"id"`
	Status             string         `json:"status"`
	Report             map[string]any `json:"report"`
	BlockNewWorkspaces bool           `json:"blockNewWorkspaces"`
	Reason             string         `json:"reason"`
	CreatedAt          time.Time      `json:"createdAt"`
	Replayed           bool           `json:"replayed"`
}
