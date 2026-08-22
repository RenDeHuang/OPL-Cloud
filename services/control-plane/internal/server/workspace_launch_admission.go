package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	controlledBasicPilotEnabledEnv                 = "OPL_CONTROLLED_BASIC_PILOT_ENABLED"
	controlledBasicPilotMaxInFlightEnv             = "OPL_CONTROLLED_BASIC_PILOT_MAX_IN_FLIGHT"
	controlledBasicPilotDefaultLimit               = 1
	productionAcceptanceBApprovalEnv               = "OPL_PRODUCTION_BASIC_ACCEPTANCE_B_APPROVAL_JSON"
	productionAcceptanceBResumeExistingApprovalEnv = "OPL_PRODUCTION_BASIC_ACCEPTANCE_B_RESUME_EXISTING_APPROVAL_JSON"
	productionAcceptanceBCapability                = "x-opl-acceptance-b-capability"
	productionAcceptanceBApprovalID                = "x-opl-acceptance-b-approval-id"
	productionAcceptanceBConfirmation              = "RUN_ONE_INDEPENDENT_FRESH_BASIC_ORDER_FOR_ACCEPTANCE_B"
	productionAcceptanceBResumePrepareLifetime     = 15 * time.Minute
	productionAcceptanceBResumeAuthorizationID     = "x-opl-resume-authorization-id"
	productionAcceptanceBResumeReasonSHA256        = "x-opl-resume-reason-sha256"
	productionAcceptanceBResumeReleaseSHA          = "x-opl-resume-release-sha"
	productionAcceptanceBResumeReleaseTree         = "x-opl-resume-release-tree"
	productionAcceptanceBResumeImageDigest         = "x-opl-resume-image-digest"
)

const workspaceLaunchOperationDiagnosticLimit = 20

var productionAcceptanceBAllowedWrites = []string{
	"submit_one_workspace_launch", "debit_one_basic_month", "create_one_workspace_key", "create_one_cvm",
	"claim_one_cvm_ownership", "claim_one_node", "create_one_cbs", "create_one_attachment", "upsert_one_gateway_secret",
	"create_one_runtime", "activate_one_workspace", "record_one_purchase_receipt",
}

var productionAcceptanceBApprovalIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{7,199}$`)
var productionAcceptanceBReleaseSHAPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)
var productionAcceptanceBIdentityDigestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

var productionAcceptanceBForbiddenWrites = []string{
	"provision_account", "adjust_wallet", "submit_second_workspace_launch", "create_second_cvm", "create_second_cbs",
	"refund", "renew", "delete", "replace", "send_model_request",
}

type productionAcceptanceBApproval struct {
	SchemaVersion int    `json:"schemaVersion"`
	OperationMode string `json:"operationMode"`
	ApprovalID    string `json:"approvalId"`
	ExpiresAt     string `json:"expiresAt"`
	Confirmation  string `json:"confirmation"`
	Release       struct {
		MergedMainSHA        string `json:"mergedMainSha"`
		CloudImageDigest     string `json:"cloudImageDigest"`
		WorkspaceImageDigest string `json:"workspaceImageDigest"`
	} `json:"release"`
	Customer struct {
		Email     string `json:"email"`
		AccountID string `json:"accountId"`
	} `json:"customer"`
	Launch struct {
		IdempotencyKey string `json:"idempotencyKey"`
		OperationID    string `json:"operationId"`
		WorkspaceID    string `json:"workspaceId"`
		Name           string `json:"name"`
		PackageID      string `json:"packageId"`
		SizeGB         int    `json:"sizeGb"`
		AutoRenew      bool   `json:"autoRenew"`
	} `json:"launch"`
	Expected struct {
		NodePoolID           string `json:"nodePoolId"`
		ResolvedInstanceType string `json:"resolvedInstanceType"`
	} `json:"expected"`
	AllowedWrites   []string `json:"allowedWrites"`
	ForbiddenWrites []string `json:"forbiddenWrites"`
}

type workspaceLaunchAcceptanceBIdentityDigestSet struct {
	AccountIdentitySHA256   string `json:"accountIdentitySha256"`
	OperationIdentitySHA256 string `json:"operationIdentitySha256"`
	WorkspaceIdentitySHA256 string `json:"workspaceIdentitySha256"`
	KeyIdentitySHA256       string `json:"keyIdentitySha256"`
	DebitIdentitySHA256     string `json:"debitIdentitySha256"`
	QuoteIdentitySHA256     string `json:"quoteIdentitySha256"`
	ProviderIdentitySHA256  string `json:"providerIdentitySha256"`
}

type productionAcceptanceBResumeExistingApproval struct {
	SchemaVersion int    `json:"schemaVersion"`
	OperationMode string `json:"operationMode"`
	ApprovalID    string `json:"approvalId"`
	ExpiresAt     string `json:"expiresAt"`
	Release       struct {
		CanonicalCloudSHA        string `json:"canonicalCloudSha"`
		CanonicalCloudTree       string `json:"canonicalCloudTree"`
		DeployedCloudImageDigest string `json:"deployedCloudImageDigest"`
	} `json:"release"`
	Authorization struct {
		AuthorizationID         string `json:"authorizationId"`
		OperationID             string `json:"operationId"`
		LaunchVersion           int    `json:"launchVersion"`
		AuthorizedStage         string `json:"authorizedStage"`
		ReasonSHA256            string `json:"reasonSha256"`
		MutationBudget          int    `json:"mutationBudget"`
		IdempotentReplayBudget  int    `json:"idempotentReplayBudget"`
		AuthoritativeReadBudget int    `json:"authoritativeReadBudget"`
	} `json:"authorization"`
	Reconciliation struct {
		OperationStatus         string `json:"operationStatus"`
		AuthoritativeStageState string `json:"authoritativeStageState"`
		Attempt                 struct {
			Attempted            int    `json:"attempted"`
			Confirmed            int    `json:"confirmed"`
			Unknown              int    `json:"unknown"`
			Max                  int    `json:"max"`
			Status               string `json:"status"`
			IdempotencyKeySHA256 string `json:"idempotencyKeySha256"`
		} `json:"attempt"`
	} `json:"reconciliation"`
	IdentityDigests workspaceLaunchAcceptanceBIdentityDigestSet `json:"identityDigests"`
}

type productionAcceptanceBResumeExistingPrepareRequest struct {
	ApprovalID      string `json:"approvalId"`
	AuthorizationID string `json:"authorizationId"`
	ReasonSHA256    string `json:"reasonSha256"`
	Release         struct {
		CanonicalCloudSHA        string `json:"canonicalCloudSha"`
		CanonicalCloudTree       string `json:"canonicalCloudTree"`
		DeployedCloudImageDigest string `json:"deployedCloudImageDigest"`
	} `json:"release"`
}

type workspaceLaunchAcceptanceBResumeExistingBinding struct {
	SchemaVersion            int                                         `json:"schemaVersion"`
	ApprovalID               string                                      `json:"approvalId"`
	ApprovalSHA256           string                                      `json:"approvalSha256"`
	CanonicalCloudSHA        string                                      `json:"canonicalCloudSha"`
	CanonicalCloudTree       string                                      `json:"canonicalCloudTree"`
	DeployedCloudImageDigest string                                      `json:"deployedCloudImageDigest"`
	AuthoritativeState       string                                      `json:"authoritativeState"`
	IdentityDigests          workspaceLaunchAcceptanceBIdentityDigestSet `json:"identityDigests"`
}

type controlledBasicPilotAdmission struct {
	Enabled     bool
	Configured  bool
	MaxInFlight int
}

func controlledBasicPilotAdmissionFromEnv() controlledBasicPilotAdmission {
	admission := controlledBasicPilotAdmission{
		MaxInFlight: controlledBasicPilotDefaultLimit,
	}
	valid := true
	switch strings.TrimSpace(os.Getenv(controlledBasicPilotEnabledEnv)) {
	case "", "0":
	case "1":
		admission.Enabled = true
	default:
		valid = false
	}
	if raw := strings.TrimSpace(os.Getenv(controlledBasicPilotMaxInFlightEnv)); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit <= 0 {
			valid = false
		} else {
			admission.MaxInFlight = limit
		}
	}
	admission.Configured = valid
	return admission
}

func (admission controlledBasicPilotAdmission) rejectNewLaunch(autoRenew bool) string {
	if !admission.Configured {
		return "workspace_launch_admission_invalid"
	}
	if autoRenew {
		return "autoRenew_unavailable"
	}
	if !admission.Enabled {
		return "workspace_launch_admission_disabled"
	}
	return ""
}

func workspacePurchaseEnabled(account map[string]any) bool {
	return boolValue(account["workspacePurchaseEnabled"])
}

func exactStringSlice(actual, expected []string) bool {
	if len(actual) != len(expected) {
		return false
	}
	for index := range expected {
		if actual[index] != expected[index] {
			return false
		}
	}
	return true
}

func parseProductionAcceptanceBApproval() (productionAcceptanceBApproval, bool) {
	raw := strings.TrimSpace(os.Getenv(productionAcceptanceBApprovalEnv))
	if raw == "" {
		return productionAcceptanceBApproval{}, false
	}
	var envelope map[string]any
	var approval productionAcceptanceBApproval
	if json.Unmarshal([]byte(raw), &envelope) != nil || !exactWorkspaceComputeClaimKeys(envelope, []string{
		"schemaVersion", "operationMode", "approvalId", "expiresAt", "confirmation", "release", "customer", "launch", "expected", "allowedWrites", "forbiddenWrites",
	}) || !exactNestedAcceptanceBApprovalKeys(envelope) || json.Unmarshal([]byte(raw), &approval) != nil {
		return productionAcceptanceBApproval{}, false
	}
	return approval, true
}

func parseProductionAcceptanceBResumeExistingApproval() (productionAcceptanceBResumeExistingApproval, bool) {
	raw := strings.TrimSpace(os.Getenv(productionAcceptanceBResumeExistingApprovalEnv))
	if raw == "" {
		return productionAcceptanceBResumeExistingApproval{}, false
	}
	var envelope map[string]any
	var approval productionAcceptanceBResumeExistingApproval
	if !jsonObjectKeysUnique([]byte(raw)) || json.Unmarshal([]byte(raw), &envelope) != nil || !exactWorkspaceComputeClaimKeys(envelope, []string{
		"schemaVersion", "operationMode", "approvalId", "expiresAt", "release", "authorization", "reconciliation", "identityDigests",
	}) || !exactNestedAcceptanceBResumeExistingApprovalKeys(envelope) || json.Unmarshal([]byte(raw), &approval) != nil {
		return productionAcceptanceBResumeExistingApproval{}, false
	}
	return approval, true
}

func jsonObjectKeysUnique(raw []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var readValue func() bool
	readValue = func() bool {
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return true
		}
		switch delim {
		case '{':
			seen := map[string]struct{}{}
			for decoder.More() {
				keyToken, keyErr := decoder.Token()
				key, keyOK := keyToken.(string)
				if keyErr != nil || !keyOK {
					return false
				}
				if _, duplicate := seen[key]; duplicate {
					return false
				}
				seen[key] = struct{}{}
				if !readValue() {
					return false
				}
			}
			closing, closingErr := decoder.Token()
			return closingErr == nil && closing == json.Delim('}')
		case '[':
			for decoder.More() {
				if !readValue() {
					return false
				}
			}
			closing, closingErr := decoder.Token()
			return closingErr == nil && closing == json.Delim(']')
		default:
			return false
		}
	}
	if !readValue() {
		return false
	}
	_, err := decoder.Token()
	return err == io.EOF
}

func exactNestedAcceptanceBResumeExistingApprovalKeys(envelope map[string]any) bool {
	wants := map[string][]string{
		"release":         {"canonicalCloudSha", "canonicalCloudTree", "deployedCloudImageDigest"},
		"authorization":   {"authorizationId", "operationId", "launchVersion", "authorizedStage", "reasonSha256", "mutationBudget", "idempotentReplayBudget", "authoritativeReadBudget"},
		"reconciliation":  {"operationStatus", "authoritativeStageState", "attempt"},
		"identityDigests": {"accountIdentitySha256", "operationIdentitySha256", "workspaceIdentitySha256", "keyIdentitySha256", "debitIdentitySha256", "quoteIdentitySha256", "providerIdentitySha256"},
	}
	for field, want := range wants {
		value, ok := envelope[field].(map[string]any)
		if !ok || !exactWorkspaceComputeClaimKeys(value, want) {
			return false
		}
	}
	reconciliation := envelope["reconciliation"].(map[string]any)
	attempt, ok := reconciliation["attempt"].(map[string]any)
	return ok && exactWorkspaceComputeClaimKeys(attempt, []string{"attempted", "confirmed", "unknown", "max", "status", "idempotencyKeySha256"})
}

func workspaceLaunchAcceptanceBIdentityDigests(operation workspaceLaunchReconcileOperation) workspaceLaunchAcceptanceBIdentityDigestSet {
	return workspaceLaunchAcceptanceBIdentityDigestSet{
		AccountIdentitySHA256: acceptanceBDigestParts(
			operation.stringFact("accountId"), operation.stringFact("ownerUserId"), strconv.FormatInt(operation.int64Fact("sub2apiUserId"), 10),
		),
		OperationIdentitySHA256: acceptanceBDigestParts(operation.ID, operation.stringFact("requestHash"), operation.CreatedAt),
		WorkspaceIdentitySHA256: acceptanceBDigestParts(
			operation.stringFact("workspaceId"), operation.stringFact("name"), operation.stringFact("packageId"),
			strconv.Itoa(operation.intFact("sizeGb")), strconv.FormatBool(operation.boolFact("autoRenew")), operation.stringFact("workspaceImageDigest"),
		),
		KeyIdentitySHA256: acceptanceBDigestParts(
			strconv.FormatInt(operation.int64Fact("sub2apiUserId"), 10), strconv.FormatInt(operation.int64Fact("workspaceKeyGroupId"), 10),
			strconv.FormatInt(operation.int64Fact("workspaceApiKeyId"), 10), operation.stringFact("workspaceKeyStatus"), operation.stringFact("workspaceKeyFingerprint"),
		),
		DebitIdentitySHA256: acceptanceBDigestParts(
			operation.stringFact("accountId"), strconv.FormatInt(operation.int64Fact("sub2apiUserId"), 10), operation.ID,
			operation.stringFact("workspaceId"), operation.stringFact("sub2apiRedeemCode"), strconv.FormatInt(operation.int64Fact("totalChargeUsdMicros"), 10),
		),
		QuoteIdentitySHA256: acceptanceBDigestParts(
			operation.stringFact("priceVersion"), pricingCurrency, strconv.FormatInt(operation.int64Fact("totalChargeUsdMicros"), 10),
		),
		ProviderIdentitySHA256: acceptanceBDigestParts(
			operation.stringFact("providerProfileRef"), operation.stringFact("preflightBindingRef"), operation.stringFact("specDigest"), operation.stringFact("workspaceImageDigest"),
			operation.stringFact("computeAllocationId"), operation.stringFact("computeBindingRef"), operation.stringFact("storageId"), operation.stringFact("storageBindingRef"),
			operation.stringFact("attachmentId"), operation.stringFact("attachmentBindingRef"), operation.stringFact("gatewaySecretRef"), operation.stringFact("secretBindingRef"),
			operation.stringFact("runtimeId"), operation.stringFact("runtimeBindingRef"),
		),
	}
}

func validWorkspaceLaunchAcceptanceBIdentityDigests(value workspaceLaunchAcceptanceBIdentityDigestSet) bool {
	return productionAcceptanceBIdentityDigestPattern.MatchString(value.AccountIdentitySHA256) &&
		productionAcceptanceBIdentityDigestPattern.MatchString(value.OperationIdentitySHA256) &&
		productionAcceptanceBIdentityDigestPattern.MatchString(value.WorkspaceIdentitySHA256) &&
		productionAcceptanceBIdentityDigestPattern.MatchString(value.KeyIdentitySHA256) &&
		productionAcceptanceBIdentityDigestPattern.MatchString(value.DebitIdentitySHA256) &&
		productionAcceptanceBIdentityDigestPattern.MatchString(value.QuoteIdentitySHA256) &&
		productionAcceptanceBIdentityDigestPattern.MatchString(value.ProviderIdentitySHA256)
}

func productionAcceptanceBResumeExistingApprovalDigest(approval productionAcceptanceBResumeExistingApproval) string {
	payload, err := json.Marshal(approval)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(payload)
	return fmt.Sprintf("sha256:%x", digest[:])
}

func productionAcceptanceBReleaseCurrent(release struct {
	CanonicalCloudSHA        string `json:"canonicalCloudSha"`
	CanonicalCloudTree       string `json:"canonicalCloudTree"`
	DeployedCloudImageDigest string `json:"deployedCloudImageDigest"`
}) bool {
	return productionAcceptanceBReleaseSHAPattern.MatchString(release.CanonicalCloudSHA) &&
		productionAcceptanceBReleaseSHAPattern.MatchString(release.CanonicalCloudTree) &&
		release.CanonicalCloudSHA == strings.TrimSpace(os.Getenv("OPL_RELEASE_SHA")) &&
		release.CanonicalCloudTree == strings.TrimSpace(os.Getenv("OPL_RELEASE_TREE")) &&
		release.DeployedCloudImageDigest == deployedImageDigest(os.Getenv("OPL_CLOUD_IMAGE"))
}

func productionAcceptanceBResumeExistingPrepareRequestValid(request productionAcceptanceBResumeExistingPrepareRequest) bool {
	return productionAcceptanceBApprovalIDPattern.MatchString(request.ApprovalID) &&
		validBillingReviewOpaqueID(request.AuthorizationID) && productionAcceptanceBIdentityDigestPattern.MatchString(request.ReasonSHA256) &&
		productionAcceptanceBReleaseShapeValid(request.Release)
}

func productionAcceptanceBReleaseShapeValid(release struct {
	CanonicalCloudSHA        string `json:"canonicalCloudSha"`
	CanonicalCloudTree       string `json:"canonicalCloudTree"`
	DeployedCloudImageDigest string `json:"deployedCloudImageDigest"`
}) bool {
	return productionAcceptanceBReleaseSHAPattern.MatchString(release.CanonicalCloudSHA) &&
		productionAcceptanceBReleaseSHAPattern.MatchString(release.CanonicalCloudTree) && workspaceImageDigestPattern.MatchString(release.DeployedCloudImageDigest)
}

func productionAcceptanceBResumeExistingCandidate(
	request productionAcceptanceBResumeExistingPrepareRequest,
	operation workspaceLaunchReconcileOperation,
	observation workspaceLaunchStageObservation,
	now time.Time,
) (productionAcceptanceBResumeExistingApproval, bool) {
	if !productionAcceptanceBResumeExistingPrepareRequestValid(request) || !productionAcceptanceBReleaseCurrent(request.Release) || operation.Status != "manual_review" ||
		!workspaceLaunchReconcileStageValid(operation.Stage) || operation.Stage == "succeeded" ||
		(observation.State != workspaceLaunchStageReady && observation.State != workspaceLaunchStageAbsent && observation.State != workspaceLaunchStagePending) ||
		operation.ResumeAuthorization != nil && operation.ResumeAuthorizationConsumedAt == "" {
		return productionAcceptanceBResumeExistingApproval{}, false
	}
	attempt, found := operation.Attempts[operation.Stage]
	if !found || attempt.Max != 1 {
		return productionAcceptanceBResumeExistingApproval{}, false
	}

	authorization := workspaceLaunchResumeAuthorization{
		AuthorizationID: request.AuthorizationID, LaunchVersion: operation.Version, AuthorizedStage: operation.Stage,
	}
	remainingBudget := attempt.Max - attempt.Attempted
	if attempt.Status == "reserved" || attempt.Attempted >= attempt.Max {
		replay := authorization
		replay.IdempotentReplayBudget, replay.AuthoritativeReadBudget = 1, workspaceLaunchAuthoritativeReadBudget
		read := authorization
		read.AuthoritativeReadBudget = workspaceLaunchAuthoritativeReadBudget
		switch {
		case workspaceLaunchReservedStageReplayEligible(operation, attempt, replay):
			authorization = replay
		case workspaceLaunchReservedStageReadEligible(operation, attempt, read):
			authorization = read
		default:
			return productionAcceptanceBResumeExistingApproval{}, false
		}
	} else if remainingBudget == 0 || remainingBudget > 1 {
		return productionAcceptanceBResumeExistingApproval{}, false
	} else {
		authorization.MutationBudget = remainingBudget
	}

	var approval productionAcceptanceBResumeExistingApproval
	approval.SchemaVersion, approval.OperationMode = 1, "acceptance_b_resume_existing"
	approval.ApprovalID = request.ApprovalID
	approval.ExpiresAt = now.UTC().Add(productionAcceptanceBResumePrepareLifetime).Format(time.RFC3339)
	approval.Release = request.Release
	approval.Authorization.AuthorizationID, approval.Authorization.OperationID = request.AuthorizationID, operation.ID
	approval.Authorization.LaunchVersion, approval.Authorization.AuthorizedStage = operation.Version, operation.Stage
	approval.Authorization.ReasonSHA256 = request.ReasonSHA256
	approval.Authorization.MutationBudget = authorization.MutationBudget
	approval.Authorization.IdempotentReplayBudget = authorization.IdempotentReplayBudget
	approval.Authorization.AuthoritativeReadBudget = authorization.AuthoritativeReadBudget
	approval.Reconciliation.OperationStatus, approval.Reconciliation.AuthoritativeStageState = operation.Status, observation.State
	approval.Reconciliation.Attempt.Attempted, approval.Reconciliation.Attempt.Confirmed = attempt.Attempted, attempt.Confirmed
	approval.Reconciliation.Attempt.Unknown, approval.Reconciliation.Attempt.Max = attempt.Unknown, attempt.Max
	approval.Reconciliation.Attempt.Status = attempt.Status
	approval.Reconciliation.Attempt.IdempotencyKeySHA256 = acceptanceBDigestParts(attempt.IdempotencyKey)
	approval.IdentityDigests = workspaceLaunchAcceptanceBIdentityDigests(operation)
	return approval, validWorkspaceLaunchAcceptanceBIdentityDigests(approval.IdentityDigests)
}

func productionAcceptanceBResumeExistingApproved(
	header http.Header,
	approval productionAcceptanceBResumeExistingApproval,
	authorization workspaceLaunchResumeAuthorization,
	operation workspaceLaunchReconcileOperation,
	observation workspaceLaunchStageObservation,
	now time.Time,
) (workspaceLaunchAcceptanceBResumeExistingBinding, bool) {
	expiresAt, expiryErr := time.Parse(time.RFC3339, approval.ExpiresAt)
	headerValue := func(name string) string {
		values := header.Values(name)
		if len(values) != 1 {
			return ""
		}
		return strings.TrimSpace(values[0])
	}
	attempt, attemptFound := operation.Attempts[authorization.AuthorizedStage]
	identityDigests := workspaceLaunchAcceptanceBIdentityDigests(operation)
	approvalAttempt := approval.Reconciliation.Attempt
	approved := expiryErr == nil && now.Before(expiresAt) && approval.SchemaVersion == 1 && approval.OperationMode == "acceptance_b_resume_existing" &&
		productionAcceptanceBApprovalIDPattern.MatchString(approval.ApprovalID) && headerValue(productionAcceptanceBApprovalID) == approval.ApprovalID &&
		secureHeaderMatches(headerValue(productionAcceptanceBCapability), strings.TrimSpace(os.Getenv("OPL_INTERNAL_SERVICE_TOKEN"))) &&
		productionAcceptanceBReleaseCurrent(approval.Release) &&
		approval.Authorization.AuthorizationID == authorization.AuthorizationID && approval.Authorization.OperationID == operation.ID &&
		approval.Authorization.LaunchVersion == authorization.LaunchVersion && approval.Authorization.AuthorizedStage == authorization.AuthorizedStage &&
		approval.Authorization.ReasonSHA256 == acceptanceBDigestParts(authorization.Reason) &&
		approval.Authorization.MutationBudget == authorization.MutationBudget && approval.Authorization.IdempotentReplayBudget == authorization.IdempotentReplayBudget &&
		approval.Authorization.AuthoritativeReadBudget == authorization.AuthoritativeReadBudget &&
		operation.Status == "manual_review" && operation.Stage == authorization.AuthorizedStage && operation.Version == authorization.LaunchVersion && attemptFound &&
		approval.Reconciliation.OperationStatus == operation.Status && approval.Reconciliation.AuthoritativeStageState == observation.State &&
		observation.State != workspaceLaunchStageUnknown && (observation.State == workspaceLaunchStageReady || observation.State == workspaceLaunchStageAbsent || observation.State == workspaceLaunchStagePending) &&
		approvalAttempt.Attempted == attempt.Attempted && approvalAttempt.Confirmed == attempt.Confirmed && approvalAttempt.Unknown == attempt.Unknown &&
		approvalAttempt.Max == attempt.Max && approvalAttempt.Status == attempt.Status &&
		approvalAttempt.IdempotencyKeySHA256 == acceptanceBDigestParts(attempt.IdempotencyKey) &&
		approval.IdentityDigests == identityDigests && validWorkspaceLaunchAcceptanceBIdentityDigests(approval.IdentityDigests)
	if !approved {
		return workspaceLaunchAcceptanceBResumeExistingBinding{}, false
	}
	return workspaceLaunchAcceptanceBResumeExistingBinding{
		SchemaVersion: 1, ApprovalID: approval.ApprovalID, ApprovalSHA256: productionAcceptanceBResumeExistingApprovalDigest(approval),
		CanonicalCloudSHA: approval.Release.CanonicalCloudSHA, CanonicalCloudTree: approval.Release.CanonicalCloudTree,
		DeployedCloudImageDigest: approval.Release.DeployedCloudImageDigest, AuthoritativeState: observation.State, IdentityDigests: identityDigests,
	}, true
}

func productionAcceptanceBResumeExistingRequestMode(header http.Header) (bool, bool) {
	approvalIDs := header.Values(productionAcceptanceBApprovalID)
	capabilities := header.Values(productionAcceptanceBCapability)
	requested := len(approvalIDs) > 0 || len(capabilities) > 0
	return requested, !requested || len(approvalIDs) == 1 && len(capabilities) == 1 &&
		strings.TrimSpace(approvalIDs[0]) != "" && strings.TrimSpace(capabilities[0]) != ""
}

func productionAcceptanceBResumeExistingReplayApproved(
	header http.Header,
	approval productionAcceptanceBResumeExistingApproval,
	authorization workspaceLaunchResumeAuthorization,
	operationID string,
	existing workspaceLaunchResumeAuthorization,
	consumed bool,
) bool {
	if existing.AcceptanceBResumeExisting == nil || existing.AuthorizationID != authorization.AuthorizationID || existing.LaunchVersion != authorization.LaunchVersion ||
		existing.AuthorizedStage != authorization.AuthorizedStage || existing.Reason != authorization.Reason || existing.MutationBudget != authorization.MutationBudget ||
		existing.IdempotentReplayBudget != authorization.IdempotentReplayBudget || existing.AuthoritativeReadBudget != authorization.AuthoritativeReadBudget {
		return false
	}
	headerValue := func(name string) string {
		values := header.Values(name)
		if len(values) != 1 {
			return ""
		}
		return strings.TrimSpace(values[0])
	}
	binding := existing.AcceptanceBResumeExisting
	releaseCurrent := productionAcceptanceBReleaseCurrent(approval.Release)
	expiresAt, expiryErr := time.Parse(time.RFC3339, approval.ExpiresAt)
	return approval.SchemaVersion == 1 && approval.OperationMode == "acceptance_b_resume_existing" && approval.ApprovalID == binding.ApprovalID &&
		headerValue(productionAcceptanceBApprovalID) == approval.ApprovalID &&
		secureHeaderMatches(headerValue(productionAcceptanceBCapability), strings.TrimSpace(os.Getenv("OPL_INTERNAL_SERVICE_TOKEN"))) &&
		approval.Authorization.AuthorizationID == authorization.AuthorizationID && approval.Authorization.OperationID == operationID &&
		approval.Authorization.LaunchVersion == authorization.LaunchVersion && approval.Authorization.AuthorizedStage == authorization.AuthorizedStage &&
		approval.Authorization.ReasonSHA256 == acceptanceBDigestParts(authorization.Reason) &&
		approval.Authorization.MutationBudget == authorization.MutationBudget && approval.Authorization.IdempotentReplayBudget == authorization.IdempotentReplayBudget &&
		approval.Authorization.AuthoritativeReadBudget == authorization.AuthoritativeReadBudget &&
		approval.Release.CanonicalCloudSHA == binding.CanonicalCloudSHA && approval.Release.CanonicalCloudTree == binding.CanonicalCloudTree &&
		approval.Release.DeployedCloudImageDigest == binding.DeployedCloudImageDigest && approval.IdentityDigests == binding.IdentityDigests &&
		productionAcceptanceBResumeExistingApprovalDigest(approval) == binding.ApprovalSHA256 &&
		(consumed || expiryErr == nil && time.Now().Before(expiresAt) && releaseCurrent)
}

func exactNestedAcceptanceBApprovalKeys(envelope map[string]any) bool {
	wants := map[string][]string{
		"release":  {"mergedMainSha", "cloudImageDigest", "workspaceImageDigest"},
		"customer": {"email", "accountId"},
		"launch":   {"idempotencyKey", "operationId", "workspaceId", "name", "packageId", "sizeGb", "autoRenew"},
		"expected": {"nodePoolId", "resolvedInstanceType"},
	}
	for field, want := range wants {
		value, ok := envelope[field].(map[string]any)
		if !ok || !exactWorkspaceComputeClaimKeys(value, want) {
			return false
		}
	}
	return true
}

func secureHeaderMatches(actual, expected string) bool {
	actualBytes, expectedBytes := []byte(actual), []byte(expected)
	return len(actualBytes) == len(expectedBytes) && len(expectedBytes) > 0 && subtle.ConstantTimeCompare(actualBytes, expectedBytes) == 1
}

func deployedImageDigest(value string) string {
	_, digest, ok := strings.Cut(strings.TrimSpace(value), "@")
	if !ok || !workspaceImageDigestPattern.MatchString(digest) {
		return ""
	}
	return digest
}

func productionAcceptanceBDeploymentApproved(approval productionAcceptanceBApproval, accountID, ownerEmail string, now time.Time) bool {
	expiresAt, expiryErr := time.Parse(time.RFC3339, approval.ExpiresAt)
	canonicalOwnerEmail, ownerEmailErr := canonicalEmail(ownerEmail)
	canonicalApprovedEmail, approvedEmailErr := canonicalEmail(approval.Customer.Email)
	currentCloudDigest := deployedImageDigest(os.Getenv("OPL_CLOUD_IMAGE"))
	currentWorkspaceDigest := deployedImageDigest(os.Getenv("OPL_WORKSPACE_IMAGE"))
	key := approval.Launch.IdempotencyKey
	operationID := workspaceLaunchOperationID(accountID, key)
	workspaceID := "ws-" + stableID("workspace-launch-v2", accountID, operationID)[:18]
	return expiryErr == nil && now.Before(expiresAt) && ownerEmailErr == nil && approvedEmailErr == nil &&
		approval.SchemaVersion == 1 && approval.OperationMode == "acceptance_b_fresh_order" && approval.Confirmation == productionAcceptanceBConfirmation &&
		productionAcceptanceBApprovalIDPattern.MatchString(approval.ApprovalID) &&
		approval.Release.MergedMainSHA == strings.TrimSpace(os.Getenv("OPL_RELEASE_SHA")) && productionAcceptanceBReleaseSHAPattern.MatchString(approval.Release.MergedMainSHA) &&
		approval.Release.CloudImageDigest == currentCloudDigest && approval.Release.WorkspaceImageDigest == currentWorkspaceDigest &&
		canonicalApprovedEmail == canonicalOwnerEmail && approval.Customer.Email == canonicalApprovedEmail && approval.Customer.AccountID == accountID &&
		key == strings.TrimSpace(key) && len(key) >= 8 && len(key) <= 200 && approval.Launch.OperationID == operationID && approval.Launch.WorkspaceID == workspaceID &&
		approval.Launch.Name != "" && approval.Launch.Name == strings.TrimSpace(approval.Launch.Name) &&
		approval.Launch.PackageID == "basic" && approval.Launch.SizeGB == 10 && !approval.Launch.AutoRenew &&
		approval.Expected.NodePoolID == strings.TrimSpace(os.Getenv("OPL_BASIC_COMPUTE_NODE_POOL_ID")) && approval.Expected.NodePoolID != "" &&
		approval.Expected.ResolvedInstanceType == strings.TrimSpace(os.Getenv("OPL_BASIC_COMPUTE_INSTANCE_TYPE")) && approval.Expected.ResolvedInstanceType != "" &&
		exactStringSlice(approval.AllowedWrites, productionAcceptanceBAllowedWrites) && exactStringSlice(approval.ForbiddenWrites, productionAcceptanceBForbiddenWrites)
}

func productionAcceptanceBLaunchApproved(rHeader http.Header, approval productionAcceptanceBApproval, accountID, ownerEmail, name, packageID string, storageGB int, autoRenew bool, key string) bool {
	internalToken := strings.TrimSpace(os.Getenv("OPL_INTERNAL_SERVICE_TOKEN"))
	header := func(name string) string {
		values := rHeader.Values(name)
		if len(values) != 1 {
			return ""
		}
		return strings.TrimSpace(values[0])
	}
	return productionAcceptanceBDeploymentApproved(approval, accountID, ownerEmail, time.Now()) &&
		productionAcceptanceBApprovalIDPattern.MatchString(approval.ApprovalID) && header(productionAcceptanceBApprovalID) == approval.ApprovalID &&
		secureHeaderMatches(header(productionAcceptanceBCapability), internalToken) &&
		approval.Launch.IdempotencyKey == key &&
		approval.Launch.Name == name && approval.Launch.PackageID == packageID && approval.Launch.SizeGB == storageGB && approval.Launch.AutoRenew == autoRenew &&
		packageID == "basic" && storageGB == 10 && !autoRenew
}

func controlledBasicPilotGlobalInFlightLimit() int {
	return controlledBasicPilotAdmissionFromEnv().MaxInFlight
}

func controlledBasicPilotMetrics(ctx context.Context, store controlPlaneTableStore) (map[string]any, error) {
	admission := controlledBasicPilotAdmissionFromEnv()
	rows, err := queryRuntimeOperations(ctx, store, runtimeOperationQuery{})
	if err != nil {
		return nil, err
	}
	stages, failures := map[string]int{}, map[string]int{}
	inFlight, manualReview := 0, 0
	for _, row := range rows {
		action := stringValue(row["action"])
		if !isWorkspaceLaunchAction(action) {
			continue
		}
		if action == "workspace.launch" {
			if !terminalWorkspaceLaunchStatus(stringValue(row["status"])) {
				inFlight++
				stages["legacy_operation"]++
			}
			continue
		}
		operation, decodeErr := decodeWorkspaceLaunchReconcileOperation(row)
		if decodeErr != nil {
			failures["operation_decode_failed"]++
			continue
		}
		if !terminalWorkspaceLaunchStatus(operation.Status) {
			inFlight++
			stages[safeControlledPilotMetricCode(operation.Stage)]++
			if operation.Status == "manual_review" {
				manualReview++
			}
		}
	}
	availableCapacity := admission.MaxInFlight - inFlight
	if availableCapacity < 0 {
		availableCapacity = 0
	}
	alerts := make([]any, 0, 3)
	if !admission.Configured {
		alerts = append(alerts, map[string]any{"code": "controlled_pilot_config_invalid", "severity": "error", "action": "disable_new_purchases"})
	}
	if len(failures) > 0 || manualReview > 0 {
		alerts = append(alerts, map[string]any{"code": "controlled_pilot_first_failure", "severity": "error", "action": "disable_new_purchases"})
	}
	if inFlight >= admission.MaxInFlight {
		alerts = append(alerts, map[string]any{"code": "controlled_pilot_capacity_exhausted", "severity": "warning", "action": "wait_for_authoritative_terminal_state"})
	}
	sort.Slice(alerts, func(i, j int) bool {
		return stringValue(alerts[i].(map[string]any)["code"]) < stringValue(alerts[j].(map[string]any)["code"])
	})
	return map[string]any{
		"enabled": admission.Enabled, "configured": admission.Configured, "accountEligibilityAuthority": "control-plane-account",
		"maxInFlight": admission.MaxInFlight, "inFlight": inFlight, "availableCapacity": availableCapacity, "manualReview": manualReview,
		"stages": stages, "failures": failures, "disableRequired": len(failures) > 0 || manualReview > 0 || !admission.Configured,
		"alerts": alerts,
	}, nil
}

func workspaceLaunchOperationDiagnostics(ctx context.Context, store controlPlaneTableStore) (map[string]any, error) {
	rows, err := queryRuntimeOperations(ctx, store, runtimeOperationQuery{Action: workspaceLaunchAction})
	if err != nil {
		return nil, err
	}
	operations := make([]any, 0)
	failedOperationCount := 0
	for _, row := range rows {
		if _, decodeErr := decodeWorkspaceLaunchReconcileOperation(row); decodeErr == nil {
			continue
		} else {
			failedOperationCount++
			if len(operations) < workspaceLaunchOperationDiagnosticLimit {
				operations = append(operations, workspaceLaunchOperationDiagnostic(row, decodeErr))
			}
		}
	}
	return map[string]any{
		"schemaVersion": 1, "failedOperationCount": failedOperationCount,
		"truncated": failedOperationCount > len(operations), "operations": operations,
	}, nil
}

func workspaceLaunchOperationDiagnostic(row map[string]any, decodeErr error) map[string]any {
	identity := firstNonEmpty(stringValue(row["operationId"]), stringValue(row["id"]))
	digest := sha256.Sum256([]byte(identity))
	diagnostic := map[string]any{
		"operationIdentityDigest": fmt.Sprintf("sha256:%x", digest),
		"action":                  workspaceLaunchAction,
		"failureCategory":         workspaceLaunchDecodeFailureCategory(decodeErr),
		"attemptsKeys":            []string{},
		"attemptsKeyCount":        0,
		"attemptsSummary":         map[string]int{},
		"observationsKeys":        []string{},
		"observationsKeyCount":    0,
		"observationsSummary":     map[string]int{},
		"missingCanonicalKeys":    []string{},
		"forbiddenLegacyKeys":     []string{},
	}
	if status := stringValue(row["status"]); validWorkspaceLaunchDiagnosticStatus(status) {
		diagnostic["status"] = status
	}
	for _, field := range []string{"createdAt", "updatedAt"} {
		if value := stringValue(row[field]); validWorkspaceLaunchDiagnosticTimestamp(value) {
			diagnostic[field] = value
		}
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal([]byte(stringValue(row["result"])), &raw) != nil || raw == nil {
		return diagnostic
	}
	for _, field := range []string{"schemaVersion", "version"} {
		var value int
		if json.Unmarshal(raw[field], &value) == nil {
			diagnostic[field] = value
		}
	}
	var stage string
	if json.Unmarshal(raw["stage"], &stage) == nil && workspaceLaunchReconcileStageValid(stage) {
		diagnostic["stage"] = stage
	}
	diagnostic["attemptsKeys"], diagnostic["attemptsKeyCount"], diagnostic["attemptsSummary"] = workspaceLaunchDiagnosticObjectSummary(
		raw["attempts"], "status", workspaceLaunchReconcileStageValid, validWorkspaceLaunchDiagnosticAttemptStatus,
	)
	diagnostic["observationsKeys"], diagnostic["observationsKeyCount"], diagnostic["observationsSummary"] = workspaceLaunchDiagnosticObjectSummary(
		raw["observations"], "state", workspaceLaunchReconcileStageValid, validWorkspaceLaunchDiagnosticObservationState,
	)
	diagnostic["missingCanonicalKeys"] = workspaceLaunchMissingCanonicalKeys(raw)
	forbidden := make([]string, 0)
	for _, field := range workspaceLaunchReconcileForbiddenFields {
		if _, exists := raw[field]; exists {
			forbidden = append(forbidden, field)
		}
	}
	sort.Strings(forbidden)
	diagnostic["forbiddenLegacyKeys"] = forbidden
	return diagnostic
}

func workspaceLaunchDiagnosticObjectSummary(encoded json.RawMessage, summaryField string, validKey, validCategory func(string) bool) ([]string, int, map[string]int) {
	var values map[string]json.RawMessage
	if len(encoded) == 0 || json.Unmarshal(encoded, &values) != nil || values == nil {
		return []string{}, 0, map[string]int{}
	}
	keys := make([]string, 0, len(values))
	summary := map[string]int{}
	for key, value := range values {
		if validKey(key) {
			keys = append(keys, key)
		}
		var item map[string]json.RawMessage
		var category string
		if json.Unmarshal(value, &item) == nil && json.Unmarshal(item[summaryField], &category) == nil && validCategory(category) {
			summary[category]++
		}
	}
	sort.Strings(keys)
	return keys, len(values), summary
}

func validWorkspaceLaunchDiagnosticTimestamp(value string) bool {
	_, err := time.Parse(time.RFC3339Nano, value)
	return err == nil
}

func validWorkspaceLaunchDiagnosticStatus(value string) bool {
	switch value {
	case "pending", "manual_review", "succeeded", "failed", "refunded",
		"running", "debit_pending", "provisioning", "completed":
		return true
	default:
		return false
	}
}

func validWorkspaceLaunchDiagnosticAttemptStatus(value string) bool {
	return value == "" || value == "reserved" || value == "confirmed" || value == "unknown"
}

func validWorkspaceLaunchDiagnosticObservationState(value string) bool {
	return value == workspaceLaunchStageAbsent || value == workspaceLaunchStagePending || value == workspaceLaunchStageReady || value == workspaceLaunchStageUnknown
}

func workspaceLaunchMissingCanonicalKeys(raw map[string]json.RawMessage) []string {
	required := []string{
		"schemaVersion", "version", "stage", "attempts", "requestHash", "accountId", "ownerUserId", "sub2apiUserId", "workspaceKeyGroupId",
		"workspaceId", "name", "packageId", "sizeGb", "priceVersion", "totalChargeUsdMicros", "providerProfileRef", "preflightBindingRef",
		"specDigest", "workspaceImageDigest", "sub2apiRedeemCode",
	}
	missing := make([]string, 0)
	for _, field := range required {
		if value, exists := raw[field]; !exists || len(value) == 0 || string(value) == "null" || string(value) == `""` {
			missing = append(missing, field)
		}
	}
	return missing
}

func safeControlledPilotMetricCode(value string) string {
	if value == "" {
		return "unknown"
	}
	for _, char := range value {
		if char != '_' && char != '-' && (char < 'a' || char > 'z') && (char < '0' || char > '9') {
			return "unknown"
		}
	}
	return value
}
