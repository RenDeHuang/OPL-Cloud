package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/controlplane"
)

const workspaceRecoveryPlanSchemaVersion = 1

type workspaceRecoveryReleaseBinding struct {
	MainSHA              string `json:"mainSha"`
	CloudImageDigest     string `json:"cloudImageDigest"`
	WorkspaceImageDigest string `json:"workspaceImageDigest"`
}

type workspaceRecoveryTargetBinding struct {
	LaunchOperationID    string `json:"launchOperationId"`
	AccountID            string `json:"accountId"`
	WorkspaceID          string `json:"workspaceId"`
	ComputeAllocationID  string `json:"computeAllocationId"`
	StorageID            string `json:"storageId"`
	Stage                string `json:"stage"`
	WorkspaceImageDigest string `json:"workspaceImageDigest"`
	AuthorityDigest      string `json:"authorityDigest"`
	PoolID               string `json:"poolId,omitempty"`
	NodePoolID           string `json:"nodePoolId,omitempty"`
	MachineName          string `json:"machineName,omitempty"`
	NodeName             string `json:"nodeName,omitempty"`
	CVMInstanceID        string `json:"cvmInstanceId,omitempty"`
	PrivateIPDigest      string `json:"privateIpDigest,omitempty"`
	InstanceType         string `json:"instanceType,omitempty"`
	Zone                 string `json:"zone,omitempty"`
	NodeOwnershipState   string `json:"nodeOwnershipState,omitempty"`
	CVMOwnershipState    string `json:"cvmOwnershipState,omitempty"`
	StorageState         string `json:"storageState,omitempty"`
	StorageProviderID    string `json:"storageProviderResourceId,omitempty"`
	WorkspaceAPIKeyID    int64  `json:"workspaceApiKeyId,omitempty"`
}

type workspaceRecoveryMutationCounts struct {
	Sub2API    int `json:"sub2api"`
	Tencent    int `json:"tencent"`
	Kubernetes int `json:"kubernetes"`
}

type workspaceRecoveryPlanStage struct {
	Stage  string `json:"stage"`
	Status string `json:"status"`
}

type workspaceRecoveryPlanMismatch struct {
	Field          string `json:"field"`
	Expected       string `json:"expected,omitempty"`
	Actual         string `json:"actual,omitempty"`
	ExpectedDigest string `json:"expectedDigest,omitempty"`
	ActualDigest   string `json:"actualDigest,omitempty"`
}

type workspaceRecoveryPlan struct {
	SchemaVersion          int                                 `json:"schemaVersion"`
	Generation             int                                 `json:"generation,omitempty"`
	PredecessorPlanDigest  string                              `json:"predecessorPlanDigest,omitempty"`
	PredecessorExecutionID string                              `json:"predecessorExecutionId,omitempty"`
	PlanID                 string                              `json:"planId"`
	PlanDigest             string                              `json:"planDigest"`
	Status                 string                              `json:"status"`
	Action                 string                              `json:"action"`
	GeneratedAt            string                              `json:"generatedAt"`
	ValidatedAt            string                              `json:"validatedAt,omitempty"`
	ReleaseBinding         workspaceRecoveryReleaseBinding     `json:"releaseBinding"`
	TargetBinding          workspaceRecoveryTargetBinding      `json:"targetBinding"`
	Stages                 []workspaceRecoveryPlanStage        `json:"stages"`
	AllowedDecisions       []string                            `json:"allowedDecisions"`
	IdentityEvidence       []clients.ComputeClaimIdentityCheck `json:"identityEvidence"`
	MutationCounts         workspaceRecoveryMutationCounts     `json:"mutationCounts"`
	OperationID            string                              `json:"operationId"`
	Mismatches             []workspaceRecoveryPlanMismatch     `json:"mismatches"`
	ExecutionID            string                              `json:"executionId,omitempty"`
	RunID                  string                              `json:"runId,omitempty"`
	URL                    string                              `json:"url,omitempty"`
	ReceiptID              string                              `json:"receiptId,omitempty"`
	ErrorCode              string                              `json:"errorCode,omitempty"`
}

type workspaceRecoveryMutationOutcome struct {
	Status                   string                          `json:"status"`
	Counts                   workspaceRecoveryMutationCounts `json:"counts"`
	FabricOperationMutations int                             `json:"fabricOperationMutations"`
	Source                   string                          `json:"source,omitempty"`
	EvidenceDigest           string                          `json:"evidenceDigest,omitempty"`
}

type workspaceRecoveryPlanDTO struct {
	PlanID         string                          `json:"planId"`
	PlanDigest     string                          `json:"planDigest"`
	Status         string                          `json:"status"`
	OperationID    string                          `json:"operationId,omitempty"`
	Stages         []workspaceRecoveryPlanStage    `json:"stages"`
	Mismatches     []workspaceRecoveryPlanMismatch `json:"mismatches"`
	MutationCounts workspaceRecoveryMutationCounts `json:"mutationCounts"`
	ExecutionID    string                          `json:"executionId,omitempty"`
	RunID          string                          `json:"runId,omitempty"`
	URL            string                          `json:"url,omitempty"`
	ReceiptID      string                          `json:"receiptId,omitempty"`
	ErrorCode      string                          `json:"errorCode,omitempty"`
}

func workspaceRecoveryPlanHTTPProjection(plan workspaceRecoveryPlan) workspaceRecoveryPlanDTO {
	mismatches := make([]workspaceRecoveryPlanMismatch, 0, len(plan.Mismatches))
	for _, mismatch := range plan.Mismatches {
		switch mismatch.Field {
		case "release.mainSha", "controlPlane.stage", "provider.nodeOwnership", "provider.cvmOwnership", "provider.storageState":
		default:
			if mismatch.ExpectedDigest == "" && mismatch.Expected != "" {
				check := workspaceComputeClaimIdentityDigestCheck(mismatch.Field, mismatch.Expected, mismatch.Actual)
				mismatch.ExpectedDigest, mismatch.ActualDigest = check.ExpectedDigest, check.ActualDigest
			}
			mismatch.Expected, mismatch.Actual = "", ""
		}
		mismatches = append(mismatches, mismatch)
	}
	return workspaceRecoveryPlanDTO{
		PlanID: plan.PlanID, PlanDigest: plan.PlanDigest, Status: plan.Status, OperationID: plan.OperationID,
		Stages: append([]workspaceRecoveryPlanStage(nil), plan.Stages...), Mismatches: mismatches, MutationCounts: plan.MutationCounts,
		ExecutionID: plan.ExecutionID, RunID: plan.RunID, URL: plan.URL, ReceiptID: plan.ReceiptID, ErrorCode: plan.ErrorCode,
	}
}

type workspaceRecoveryExecution struct {
	ExecutionID         string                                   `json:"executionId"`
	RunIdentity         string                                   `json:"runIdentity"`
	PlanID              string                                   `json:"planId"`
	PlanDigest          string                                   `json:"planDigest"`
	ApprovalDigest      string                                   `json:"approvalDigest"`
	Decision            string                                   `json:"decision"`
	Status              string                                   `json:"status"`
	LeaseToken          string                                   `json:"leaseToken,omitempty"`
	LeaseExpiresAt      string                                   `json:"leaseExpiresAt,omitempty"`
	StartedAt           string                                   `json:"startedAt"`
	CompletedAt         string                                   `json:"completedAt,omitempty"`
	ErrorCode           string                                   `json:"errorCode,omitempty"`
	MutationOutcome     workspaceRecoveryMutationOutcome         `json:"mutationOutcome"`
	Approval            *workspaceLaunchReadbackRecoveryApproval `json:"approval,omitempty"`
	ComputeClaimRequest *workspaceComputeClaimRecoveryRequest    `json:"computeClaimRequest,omitempty"`
}

type workspaceRecoveryPlanHistoryEntry struct {
	Plan       workspaceRecoveryPlan      `json:"plan"`
	Execution  workspaceRecoveryExecution `json:"execution"`
	ArchivedAt string                     `json:"archivedAt"`
}

func deployedImageDigest(value string) string {
	value = strings.TrimSpace(value)
	_, digest, ok := strings.Cut(value, "@")
	if !ok || !computeClaimCloudDigestPattern.MatchString(digest) {
		return ""
	}
	return digest
}

func currentWorkspaceRecoveryReleaseBinding() (workspaceRecoveryReleaseBinding, error) {
	binding := workspaceRecoveryReleaseBinding{
		MainSHA:              strings.TrimSpace(os.Getenv("OPL_RELEASE_SHA")),
		CloudImageDigest:     deployedImageDigest(os.Getenv("OPL_CLOUD_IMAGE")),
		WorkspaceImageDigest: deployedImageDigest(os.Getenv("OPL_WORKSPACE_IMAGE")),
	}
	if !computeClaimMergedSHAPattern.MatchString(binding.MainSHA) || !computeClaimCloudDigestPattern.MatchString(binding.CloudImageDigest) ||
		!computeClaimCloudDigestPattern.MatchString(binding.WorkspaceImageDigest) {
		return workspaceRecoveryReleaseBinding{}, errWorkspaceComputeClaimIdentity
	}
	return binding, nil
}

func workspaceRecoveryAuthorityDigest(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func workspaceRecoveryPlanDigest(plan workspaceRecoveryPlan) string {
	material := struct {
		SchemaVersion          int                             `json:"schemaVersion"`
		Generation             int                             `json:"generation,omitempty"`
		PredecessorPlanDigest  string                          `json:"predecessorPlanDigest,omitempty"`
		PredecessorExecutionID string                          `json:"predecessorExecutionId,omitempty"`
		Action                 string                          `json:"action"`
		ReleaseBinding         workspaceRecoveryReleaseBinding `json:"releaseBinding"`
		TargetBinding          workspaceRecoveryTargetBinding  `json:"targetBinding"`
		Stages                 []workspaceRecoveryPlanStage    `json:"stages"`
		AllowedDecisions       []string                        `json:"allowedDecisions"`
		MutationCounts         workspaceRecoveryMutationCounts `json:"mutationCounts"`
	}{
		SchemaVersion: plan.SchemaVersion, Generation: plan.Generation, PredecessorPlanDigest: plan.PredecessorPlanDigest,
		PredecessorExecutionID: plan.PredecessorExecutionID, Action: plan.Action, ReleaseBinding: plan.ReleaseBinding,
		TargetBinding: plan.TargetBinding, Stages: plan.Stages, AllowedDecisions: plan.AllowedDecisions,
		MutationCounts: plan.MutationCounts,
	}
	return workspaceRecoveryAuthorityDigest(material)
}

func workspaceRecoveryPlanStages(operation workspaceLaunchOperation) []workspaceRecoveryPlanStage {
	stages := make([]workspaceRecoveryPlanStage, 0, len(workspaceLaunchContinuationStages))
	for _, name := range workspaceLaunchContinuationStages {
		budget := operation.ContinuationAttemptBudgets[name]
		status := "pending"
		switch {
		case budget.Unknown > 0:
			status = "manual_review"
		case budget.Confirmed == budget.Max:
			status = "completed"
		}
		stages = append(stages, workspaceRecoveryPlanStage{Stage: name, Status: status})
	}
	return stages
}

func workspaceRecoveryPlanIdentityEvidence(operation workspaceLaunchOperation, proof workspaceLaunchReadbackRecoveryProof, release workspaceRecoveryReleaseBinding, expectedPrivateIPs ...string) []clients.ComputeClaimIdentityCheck {
	expectedPrivateIP := operation.ComputePrivateIP
	if len(expectedPrivateIPs) != 0 {
		expectedPrivateIP = expectedPrivateIPs[0]
	}
	return []clients.ComputeClaimIdentityCheck{
		workspaceComputeClaimIdentityCheck("controlPlane.launchOperationId", operation.ID, proof.Target.LaunchOperationID),
		workspaceComputeClaimIdentityCheck("controlPlane.accountId", operation.AccountID, proof.Target.AccountID),
		workspaceComputeClaimIdentityCheck("controlPlane.workspaceId", operation.WorkspaceID, proof.Target.WorkspaceID),
		workspaceComputeClaimIdentityCheck("controlPlane.computeAllocationId", operation.ComputeID, proof.Target.ComputeAllocationID),
		workspaceComputeClaimIdentityCheck("controlPlane.storageId", operation.StorageID, proof.Target.StorageID),
		workspaceComputeClaimIdentityDigestCheck("release.workspaceImageDigest", deployedImageDigest(operation.WorkspaceImageDigest), release.WorkspaceImageDigest),
		workspaceComputeClaimIdentityDigestCheck("target.privateIp", expectedPrivateIP, proof.Target.PrivateIP),
	}
}

func newWorkspaceReadbackRecoveryPlan(operation workspaceLaunchOperation, proof workspaceLaunchReadbackRecoveryProof, release workspaceRecoveryReleaseBinding, expectedPrivateIP string) (workspaceRecoveryPlan, error) {
	authorityDigest := workspaceRecoveryAuthorityDigest(proof)
	if authorityDigest == "" || !proof.Eligible || proof.Reason != "none" || proof.Sub2APIMutationCount != 0 || proof.TencentMutationCount != 0 || proof.KubernetesMutationCount != 0 {
		return workspaceRecoveryPlan{}, errBillingReviewProviderFact
	}
	plan := workspaceRecoveryPlan{
		SchemaVersion: workspaceRecoveryPlanSchemaVersion, Status: "diagnosed", Action: "unknown_stage_continue",
		GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano), ReleaseBinding: release,
		OperationID: operation.ID, Mismatches: []workspaceRecoveryPlanMismatch{},
		TargetBinding: workspaceRecoveryTargetBinding{
			LaunchOperationID: operation.ID, AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID,
			ComputeAllocationID: operation.ComputeID, StorageID: operation.StorageID, Stage: proof.Stage,
			WorkspaceImageDigest: operation.WorkspaceImageDigest, AuthorityDigest: authorityDigest,
		},
		Stages: workspaceRecoveryPlanStages(operation), AllowedDecisions: []string{"continue", "escalate"},
		IdentityEvidence: workspaceRecoveryPlanIdentityEvidence(operation, proof, release, expectedPrivateIP),
		MutationCounts:   workspaceRecoveryMutationCounts{},
	}
	for _, check := range plan.IdentityEvidence {
		if !check.Matches {
			plan.Status = "blocked"
			plan.Mismatches = append(plan.Mismatches, workspaceRecoveryPlanMismatchFromCheck(check))
		}
	}
	plan.PlanDigest = workspaceRecoveryPlanDigest(plan)
	plan.PlanID = "recovery-plan-" + plan.PlanDigest[:20]
	return plan, nil
}

func (app *controlPlaneServer) workspaceRecoveryPlanExpectedPrivateIP(operation workspaceLaunchOperation) (string, error) {
	compute, ok := app.getCompute(operation.ComputeID)
	privateIP := strings.TrimSpace(stringValue(compute["privateIp"]))
	if !ok || !workspaceLaunchResourceIdentityMatches("compute", compute, operation) || privateIP == "" {
		return "", errBillingReviewIdentity
	}
	return privateIP, nil
}

func workspaceComputeClaimRecoveryRequestForOperation(operation workspaceLaunchOperation) workspaceComputeClaimRecoveryRequest {
	return workspaceComputeClaimRecoveryRequest{
		LaunchOperationID: operation.ID, AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID,
		ComputeID: operation.ComputeID, StorageID: operation.StorageID, PackageID: operation.PackageID,
		PoolID: operation.ComputePoolID, NodePoolID: operation.ComputeNodePoolID, MachineName: operation.ComputeMachineName,
		NodeName: operation.ComputeNodeName, CVMInstanceID: operation.ComputeCVMInstanceID, PrivateIP: operation.ComputePrivateIP,
		InstanceType: operation.ComputeInstanceType, Zone: operation.ComputeZone,
	}
}

func workspaceComputeClaimRecoveryRequestFromAllocation(operation workspaceLaunchOperation, allocation clients.ComputeAllocation) workspaceComputeClaimRecoveryRequest {
	request := workspaceComputeClaimRecoveryRequestForOperation(operation)
	request.PoolID, request.MachineName, request.NodeName = allocation.PoolID, allocation.MachineName, allocation.NodeName
	request.CVMInstanceID, request.PrivateIP = firstNonEmpty(allocation.CVMInstanceID, allocation.InstanceID), allocation.PrivateIP
	request.InstanceType, request.Zone = allocation.InstanceType, allocation.Zone
	return request
}

func (app *controlPlaneServer) workspaceComputeClaimRecoveryProofForPlan(ctx context.Context, service *controlplane.Service, operation workspaceLaunchOperation) (workspaceComputeClaimRecoveryRequest, clients.ComputeClaimRecoveryProof, *clients.ComputeClaimIdentityEvidence, error) {
	if !workspaceComputeClaimCanonical(operation) && !workspaceComputeClaimLegacyCandidate(operation) {
		return workspaceComputeClaimRecoveryRequest{}, clients.ComputeClaimRecoveryProof{}, nil, errWorkspaceComputeClaimNotPending
	}
	userID, err := app.sub2APIUserID(ctx, operation.AccountID)
	validIdentity := validWorkspaceLaunchComputeClaimIdentity(operation)
	if workspaceComputeClaimLegacyCandidate(operation) {
		validIdentity = validWorkspaceLaunchLegacyComputeClaimIdentity(operation)
	}
	if err != nil || !validIdentity || !workspaceLaunchChargeConfirmed(operation, userID) {
		return workspaceComputeClaimRecoveryRequest{}, clients.ComputeClaimRecoveryProof{}, nil, errWorkspaceComputeClaimIdentity
	}
	input := workspaceComputeClaimRecoveryRequestForOperation(operation)
	if workspaceComputeClaimLegacyCandidate(operation) {
		allocation, readErr := service.ReadMonthlyCompute(ctx, operation.ComputeID)
		if readErr != nil || allocation.ID != operation.ComputeID || allocation.AccountID != operation.AccountID || allocation.WorkspaceID != operation.WorkspaceID ||
			allocation.PackageID != operation.PackageID || allocation.NodePoolID != operation.ComputeNodePoolID {
			return workspaceComputeClaimRecoveryRequest{}, clients.ComputeClaimRecoveryProof{}, nil, errWorkspaceComputeClaimIdentity
		}
		input = workspaceComputeClaimRecoveryRequestFromAllocation(operation, allocation)
	}
	proof, proofErr := service.ComputeClaimRecoveryProof(ctx, workspaceComputeClaimRecoveryInput(operation, input))
	if proofErr != nil || proof.SchemaVersion != 1 || proof.TencentMutationCount != 0 || proof.KubernetesMutationCount != 0 || proof.Sub2APIMutationCount != 0 {
		return input, proof, nil, errWorkspaceComputeClaimProof
	}
	evidence, evidenceErr := service.ComputeClaimRecoveryIdentityEvidence(ctx, clients.ComputeClaimRecoveryClaimInput{
		ComputeClaimRecoveryInput: workspaceComputeClaimRecoveryInput(operation, input), MachineName: proof.MachineName,
		NodeName: proof.NodeName, CVMInstanceID: proof.CVMInstanceID, PrivateIP: proof.PrivateIP,
		InstanceType: proof.InstanceType, Zone: proof.Zone,
	})
	if evidenceErr != nil || evidence == nil {
		return input, proof, nil, errWorkspaceComputeClaimIdentity
	}
	return input, proof, evidence, nil
}

func workspaceComputeClaimPlanIdentityEvidence(operation workspaceLaunchOperation, input workspaceComputeClaimRecoveryRequest, proof clients.ComputeClaimRecoveryProof, evidence *clients.ComputeClaimIdentityEvidence) []clients.ComputeClaimIdentityCheck {
	checks := []clients.ComputeClaimIdentityCheck{
		workspaceComputeClaimIdentityCheck("controlPlane.launchOperationId", operation.ID, proof.LaunchOperationID),
		workspaceComputeClaimIdentityCheck("controlPlane.accountId", operation.AccountID, proof.AccountID),
		workspaceComputeClaimIdentityCheck("controlPlane.workspaceId", operation.WorkspaceID, proof.WorkspaceID),
		workspaceComputeClaimIdentityCheck("controlPlane.computeAllocationId", operation.ComputeID, proof.ComputeAllocationID),
		workspaceComputeClaimIdentityCheck("controlPlane.storageId", operation.StorageID, proof.StorageVolumeID),
		workspaceComputeClaimIdentityCheck("fabric.poolId", input.PoolID, proof.PoolID),
		workspaceComputeClaimIdentityCheck("fabric.nodePoolId", operation.ComputeNodePoolID, proof.NodePoolID),
		workspaceComputeClaimIdentityCheck("provider.machineName", input.MachineName, proof.MachineName),
		workspaceComputeClaimIdentityCheck("kubernetes.nodeName", input.NodeName, proof.NodeName),
		workspaceComputeClaimIdentityCheck("tencent.cvmInstanceId", input.CVMInstanceID, proof.CVMInstanceID),
		workspaceComputeClaimIdentityDigestCheck("tencent.privateIp", input.PrivateIP, proof.PrivateIP),
		workspaceComputeClaimIdentityCheck("tencent.instanceType", input.InstanceType, proof.InstanceType),
		workspaceComputeClaimIdentityCheck("tencent.zone", input.Zone, proof.Zone),
		workspaceComputeClaimIdentityAllowedCheck("provider.nodeOwnership", "unallocated_or_target_owned", proof.NodeOwnershipState, "unallocated", "target_owned"),
		workspaceComputeClaimIdentityAllowedCheck("provider.cvmOwnership", "recoverable_or_target_owned", proof.CVMOwnershipState, "recoverable", "target_owned"),
	}
	checks = append(checks, evidence.Checks...)
	return checks
}

func newWorkspaceComputeClaimRecoveryPlan(operation workspaceLaunchOperation, input workspaceComputeClaimRecoveryRequest, proof clients.ComputeClaimRecoveryProof, evidence *clients.ComputeClaimIdentityEvidence, release workspaceRecoveryReleaseBinding) (workspaceRecoveryPlan, error) {
	authorityDigest := workspaceRecoveryAuthorityDigest(struct {
		Proof    clients.ComputeClaimRecoveryProof     `json:"proof"`
		Evidence *clients.ComputeClaimIdentityEvidence `json:"evidence"`
	}{Proof: proof, Evidence: evidence})
	if authorityDigest == "" || !proof.Eligible || proof.Reason != "none" || proof.Sub2APIMutationCount != 0 || proof.TencentMutationCount != 0 || proof.KubernetesMutationCount != 0 {
		return workspaceRecoveryPlan{}, errWorkspaceComputeClaimProof
	}
	privateIPDigest := workspaceRecoveryAuthorityDigest(proof.PrivateIP)
	plan := workspaceRecoveryPlan{
		SchemaVersion: workspaceRecoveryPlanSchemaVersion, Status: "diagnosed", Action: "compute_claim_continue",
		GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano), ReleaseBinding: release,
		OperationID: operation.ID, Mismatches: []workspaceRecoveryPlanMismatch{},
		TargetBinding: workspaceRecoveryTargetBinding{
			LaunchOperationID: operation.ID, AccountID: operation.AccountID, WorkspaceID: operation.WorkspaceID,
			ComputeAllocationID: operation.ComputeID, StorageID: operation.StorageID, Stage: "compute_claim",
			WorkspaceImageDigest: operation.WorkspaceImageDigest, AuthorityDigest: authorityDigest,
			PoolID: proof.PoolID, NodePoolID: proof.NodePoolID, MachineName: proof.MachineName, NodeName: proof.NodeName,
			CVMInstanceID: proof.CVMInstanceID, PrivateIPDigest: privateIPDigest, InstanceType: proof.InstanceType, Zone: proof.Zone,
			NodeOwnershipState: proof.NodeOwnershipState, CVMOwnershipState: proof.CVMOwnershipState,
			StorageState: proof.StorageState, StorageProviderID: proof.StorageProviderResourceID, WorkspaceAPIKeyID: operation.WorkspaceAPIKeyID,
		},
		Stages:           append([]workspaceRecoveryPlanStage{{Stage: "compute_claim", Status: "manual_review"}}, workspaceRecoveryPlanStages(operation)...),
		AllowedDecisions: []string{"continue", "escalate"}, MutationCounts: workspaceRecoveryMutationCounts{},
	}
	plan.IdentityEvidence = workspaceComputeClaimPlanIdentityEvidence(operation, input, proof, evidence)
	for _, check := range plan.IdentityEvidence {
		if !check.Matches {
			plan.Status = "blocked"
			plan.Mismatches = append(plan.Mismatches, workspaceRecoveryPlanMismatchFromCheck(check))
		}
	}
	plan.PlanDigest = workspaceRecoveryPlanDigest(plan)
	plan.PlanID = "recovery-plan-" + plan.PlanDigest[:20]
	return plan, nil
}

func workspaceRecoveryPlanProjection(operation workspaceLaunchOperation) workspaceRecoveryPlan {
	if operation.RecoveryPlan == nil {
		return workspaceRecoveryPlan{}
	}
	plan := *operation.RecoveryPlan
	plan.OperationID = operation.ID
	if plan.Mismatches == nil {
		plan.Mismatches = []workspaceRecoveryPlanMismatch{}
	}
	plan.URL, plan.ReceiptID = operation.URL, operation.ReceiptID
	if operation.RecoveryExecution != nil {
		plan.ExecutionID = operation.RecoveryExecution.ExecutionID
		plan.RunID = operation.RecoveryExecution.RunIdentity
		if operation.RecoveryExecution.ErrorCode != "" {
			plan.ErrorCode = operation.RecoveryExecution.ErrorCode
		}
	}
	return plan
}

func workspaceRecoveryPlanMismatchFromCheck(check clients.ComputeClaimIdentityCheck) workspaceRecoveryPlanMismatch {
	return workspaceRecoveryPlanMismatch{
		Field: check.Field, Expected: check.Expected, Actual: check.Actual,
		ExpectedDigest: check.ExpectedDigest, ActualDigest: check.ActualDigest,
	}
}

func workspaceRecoveryPlanMismatches(persisted, current workspaceRecoveryPlan) []workspaceRecoveryPlanMismatch {
	checks := []clients.ComputeClaimIdentityCheck{
		workspaceComputeClaimIdentityCheck("release.mainSha", persisted.ReleaseBinding.MainSHA, current.ReleaseBinding.MainSHA),
		workspaceComputeClaimIdentityDigestCheck("release.cloudImageDigest", persisted.ReleaseBinding.CloudImageDigest, current.ReleaseBinding.CloudImageDigest),
		workspaceComputeClaimIdentityDigestCheck("release.workspaceImageDigest", persisted.ReleaseBinding.WorkspaceImageDigest, current.ReleaseBinding.WorkspaceImageDigest),
		workspaceComputeClaimIdentityCheck("controlPlane.launchOperationId", persisted.TargetBinding.LaunchOperationID, current.TargetBinding.LaunchOperationID),
		workspaceComputeClaimIdentityCheck("controlPlane.accountId", persisted.TargetBinding.AccountID, current.TargetBinding.AccountID),
		workspaceComputeClaimIdentityCheck("controlPlane.workspaceId", persisted.TargetBinding.WorkspaceID, current.TargetBinding.WorkspaceID),
		workspaceComputeClaimIdentityCheck("controlPlane.computeAllocationId", persisted.TargetBinding.ComputeAllocationID, current.TargetBinding.ComputeAllocationID),
		workspaceComputeClaimIdentityCheck("controlPlane.storageId", persisted.TargetBinding.StorageID, current.TargetBinding.StorageID),
		workspaceComputeClaimIdentityCheck("controlPlane.stage", persisted.TargetBinding.Stage, current.TargetBinding.Stage),
		workspaceComputeClaimIdentityDigestCheck("controlPlane.workspaceImageDigest", persisted.TargetBinding.WorkspaceImageDigest, current.TargetBinding.WorkspaceImageDigest),
		workspaceComputeClaimIdentityDigestCheck("authority.binding", persisted.TargetBinding.AuthorityDigest, current.TargetBinding.AuthorityDigest),
		workspaceComputeClaimIdentityCheck("fabric.poolId", persisted.TargetBinding.PoolID, current.TargetBinding.PoolID),
		workspaceComputeClaimIdentityCheck("fabric.nodePoolId", persisted.TargetBinding.NodePoolID, current.TargetBinding.NodePoolID),
		workspaceComputeClaimIdentityCheck("provider.machineName", persisted.TargetBinding.MachineName, current.TargetBinding.MachineName),
		workspaceComputeClaimIdentityCheck("kubernetes.nodeName", persisted.TargetBinding.NodeName, current.TargetBinding.NodeName),
		workspaceComputeClaimIdentityCheck("tencent.cvmInstanceId", persisted.TargetBinding.CVMInstanceID, current.TargetBinding.CVMInstanceID),
		workspaceComputeClaimIdentityDigestCheck("tencent.privateIp", persisted.TargetBinding.PrivateIPDigest, current.TargetBinding.PrivateIPDigest),
		workspaceComputeClaimIdentityCheck("tencent.instanceType", persisted.TargetBinding.InstanceType, current.TargetBinding.InstanceType),
		workspaceComputeClaimIdentityCheck("tencent.zone", persisted.TargetBinding.Zone, current.TargetBinding.Zone),
		workspaceComputeClaimIdentityCheck("provider.nodeOwnership", persisted.TargetBinding.NodeOwnershipState, current.TargetBinding.NodeOwnershipState),
		workspaceComputeClaimIdentityCheck("provider.cvmOwnership", persisted.TargetBinding.CVMOwnershipState, current.TargetBinding.CVMOwnershipState),
		workspaceComputeClaimIdentityCheck("provider.storageState", persisted.TargetBinding.StorageState, current.TargetBinding.StorageState),
		workspaceComputeClaimIdentityCheck("provider.storageResourceId", persisted.TargetBinding.StorageProviderID, current.TargetBinding.StorageProviderID),
		workspaceComputeClaimIdentityCheck("controlPlane.workspaceApiKeyId", persisted.TargetBinding.WorkspaceAPIKeyID, current.TargetBinding.WorkspaceAPIKeyID),
	}
	mismatches := make([]workspaceRecoveryPlanMismatch, 0)
	for _, check := range checks {
		if !check.Matches {
			mismatches = append(mismatches, workspaceRecoveryPlanMismatchFromCheck(check))
		}
	}
	return mismatches
}

func workspaceRecoveryExecutionConfirmedZero(operation workspaceLaunchOperation, evidence *clients.ComputeClaimIdentityEvidence) (workspaceRecoveryMutationOutcome, bool) {
	if operation.RecoveryPlan == nil || operation.RecoveryExecution == nil || operation.RecoveryPlan.Status != "failed" ||
		operation.RecoveryExecution.Status != "failed" || operation.RecoveryExecution.CompletedAt == "" ||
		operation.RecoveryExecution.LeaseToken != "" || operation.RecoveryExecution.LeaseExpiresAt != "" ||
		operation.RecoveryExecution.PlanID != operation.RecoveryPlan.PlanID || operation.RecoveryExecution.PlanDigest != operation.RecoveryPlan.PlanDigest {
		return workspaceRecoveryMutationOutcome{}, false
	}
	outcome := operation.RecoveryExecution.MutationOutcome
	if outcome.Status == "confirmed_zero" && outcome.Counts == (workspaceRecoveryMutationCounts{}) && outcome.FabricOperationMutations == 0 {
		return outcome, true
	}
	if outcome.Status != "" && outcome.Status != "unknown" || operation.RecoveryPlan.Action != "compute_claim_continue" || evidence == nil {
		return workspaceRecoveryMutationOutcome{}, false
	}
	source, evidenceDigest := "", ""
	switch {
	case evidence.MutationLedger == "absent" && (evidence.MutationLedgerOutcome == "" || evidence.MutationLedgerOutcome == "confirmed_zero"):
		source = "fabric_mutation_ledger_absent"
		if computeClaimApprovalDigestPattern.MatchString(evidence.MutationLedgerDigest) {
			evidenceDigest = evidence.MutationLedgerDigest
		}
	case evidence.MutationLedger == "observed" && evidence.MutationLedgerOutcome == "confirmed_zero" &&
		computeClaimApprovalDigestPattern.MatchString(evidence.MutationLedgerDigest):
		source, evidenceDigest = "fabric_mutation_ledger_confirmed_zero", evidence.MutationLedgerDigest
	default:
		return workspaceRecoveryMutationOutcome{}, false
	}
	return workspaceRecoveryMutationOutcome{Status: "confirmed_zero", Source: source, EvidenceDigest: evidenceDigest}, true
}

func workspaceRecoveryMutationOutcomeFromComputeClaim(proof clients.ComputeClaimRecoveryProof) workspaceRecoveryMutationOutcome {
	outcome := workspaceRecoveryMutationOutcome{Status: "unknown", Source: "compute_claim_response"}
	if !workspaceComputeClaimEvidenceMatches(proof, false) || proof.Sub2APIMutationCount < 0 || proof.TencentMutationCount < 0 || proof.KubernetesMutationCount < 0 {
		return outcome
	}
	outcome.Counts = workspaceRecoveryMutationCounts{
		Sub2API: proof.Sub2APIMutationCount, Tencent: proof.TencentMutationCount, Kubernetes: proof.KubernetesMutationCount,
	}
	if outcome.Counts != (workspaceRecoveryMutationCounts{}) {
		outcome.Status = "nonzero"
	}
	return outcome
}

func newWorkspaceRecoverySuccessor(plan workspaceRecoveryPlan, predecessor workspaceRecoveryPlan, execution workspaceRecoveryExecution, historyLength int) workspaceRecoveryPlan {
	plan.Generation = predecessor.Generation + 1
	if plan.Generation <= historyLength {
		plan.Generation = historyLength + 1
	}
	plan.PredecessorPlanDigest = predecessor.PlanDigest
	plan.PredecessorExecutionID = execution.ExecutionID
	plan.PlanDigest = workspaceRecoveryPlanDigest(plan)
	plan.PlanID = "recovery-plan-" + plan.PlanDigest[:20]
	return plan
}

func (app *controlPlaneServer) diagnoseWorkspaceRecoveryPlan(ctx context.Context, service *controlplane.Service, accountID, operationID string) (workspaceRecoveryPlan, error) {
	operation, ok, err := app.workspaceLaunchOperation(ctx, operationID)
	if err != nil || !ok {
		if err == nil {
			err = errBillingReviewNotFound
		}
		return workspaceRecoveryPlan{}, err
	}
	if accountID == "" || operation.AccountID != accountID {
		return workspaceRecoveryPlan{}, errBillingReviewIdentity
	}
	release, err := currentWorkspaceRecoveryReleaseBinding()
	if err != nil {
		return workspaceRecoveryPlan{}, err
	}

	unlock := app.lockResource("workspace-launch", operation.AccountID)
	defer unlock()
	operation, ok, err = app.workspaceLaunchOperation(ctx, operationID)
	if err != nil || !ok || operation.AccountID != accountID {
		if err == nil {
			err = errBillingReviewIdentity
		}
		return workspaceRecoveryPlan{}, err
	}
	recovered, proof, err := app.workspaceLaunchReadbackRecoveryProofForOperation(ctx, service, operation)
	var plan workspaceRecoveryPlan
	var computeEvidence *clients.ComputeClaimIdentityEvidence
	if workspaceComputeClaimCanonical(operation) || workspaceComputeClaimLegacyCandidate(operation) {
		computeInput, computeProof, evidence, computeErr := app.workspaceComputeClaimRecoveryProofForPlan(ctx, service, operation)
		if computeErr != nil {
			return workspaceRecoveryPlan{}, computeErr
		}
		computeEvidence = evidence
		plan, err = newWorkspaceComputeClaimRecoveryPlan(operation, computeInput, computeProof, evidence, release)
	} else {
		if err != nil {
			return workspaceRecoveryPlan{}, err
		}
		expectedPrivateIP, privateIPErr := app.workspaceRecoveryPlanExpectedPrivateIP(recovered)
		if privateIPErr != nil {
			return workspaceRecoveryPlan{}, privateIPErr
		}
		plan, err = newWorkspaceReadbackRecoveryPlan(recovered, proof, release, expectedPrivateIP)
	}
	if err != nil {
		return workspaceRecoveryPlan{}, err
	}
	if operation.RecoveryExecution != nil {
		if operation.RecoveryExecution.Status != "failed" {
			return workspaceRecoveryPlanProjection(operation), nil
		}
		outcome, successorAllowed := workspaceRecoveryExecutionConfirmedZero(operation, computeEvidence)
		if !successorAllowed {
			return workspaceRecoveryPlanProjection(operation), nil
		}
		predecessorPlan := *operation.RecoveryPlan
		predecessorExecution := *operation.RecoveryExecution
		predecessorExecution.MutationOutcome = outcome
		operation.RecoveryHistory = append(operation.RecoveryHistory, workspaceRecoveryPlanHistoryEntry{
			Plan: predecessorPlan, Execution: predecessorExecution, ArchivedAt: time.Now().UTC().Format(time.RFC3339Nano),
		})
		plan = newWorkspaceRecoverySuccessor(plan, predecessorPlan, predecessorExecution, len(operation.RecoveryHistory)-1)
		operation.RecoveryPlan = &plan
		operation.RecoveryExecution = nil
		if err := app.persistWorkspaceLaunch(ctx, &operation); err != nil {
			if errors.Is(err, errWorkspaceLaunchCASConflict) {
				current, found, loadErr := app.workspaceLaunchOperation(ctx, operationID)
				if loadErr == nil && found && current.RecoveryPlan != nil && current.RecoveryPlan.PlanDigest == plan.PlanDigest &&
					len(current.RecoveryHistory) >= len(operation.RecoveryHistory) {
					return workspaceRecoveryPlanProjection(current), nil
				}
			}
			return workspaceRecoveryPlan{}, err
		}
		return plan, nil
	}
	if operation.RecoveryPlan != nil && operation.RecoveryPlan.Generation > 0 {
		plan.Generation = operation.RecoveryPlan.Generation
		plan.PredecessorPlanDigest = operation.RecoveryPlan.PredecessorPlanDigest
		plan.PredecessorExecutionID = operation.RecoveryPlan.PredecessorExecutionID
		plan.PlanDigest = workspaceRecoveryPlanDigest(plan)
		plan.PlanID = "recovery-plan-" + plan.PlanDigest[:20]
	}
	if operation.RecoveryPlan != nil && operation.RecoveryPlan.PlanDigest == plan.PlanDigest {
		return *operation.RecoveryPlan, nil
	}
	operation.RecoveryPlan = &plan
	operation.RecoveryExecution = nil
	if err := app.persistWorkspaceLaunch(ctx, &operation); err != nil {
		if errors.Is(err, errWorkspaceLaunchCASConflict) {
			current, found, loadErr := app.workspaceLaunchOperation(ctx, operationID)
			if loadErr == nil && found && current.RecoveryPlan != nil && current.RecoveryPlan.PlanDigest == plan.PlanDigest {
				return *current.RecoveryPlan, nil
			}
		}
		return workspaceRecoveryPlan{}, err
	}
	return plan, nil
}

func (app *controlPlaneServer) getWorkspaceRecoveryPlan(ctx context.Context, operationID string) (workspaceRecoveryPlan, error) {
	operation, ok, err := app.workspaceLaunchOperation(ctx, operationID)
	if err != nil || !ok || operation.RecoveryPlan == nil {
		if err == nil {
			err = errBillingReviewNotFound
		}
		return workspaceRecoveryPlan{}, err
	}
	return workspaceRecoveryPlanProjection(operation), nil
}

func (app *controlPlaneServer) validateWorkspaceRecoveryPlan(ctx context.Context, service *controlplane.Service, operationID, planID, planDigest string) (workspaceRecoveryPlan, error) {
	operation, ok, err := app.workspaceLaunchOperation(ctx, operationID)
	if err != nil || !ok || operation.RecoveryPlan == nil {
		if err == nil {
			err = errBillingReviewNotFound
		}
		return workspaceRecoveryPlan{}, err
	}
	if operation.RecoveryPlan.PlanID != planID || operation.RecoveryPlan.PlanDigest != planDigest {
		return workspaceRecoveryPlan{}, errBillingReviewIdentity
	}

	unlock := app.lockResource("workspace-launch", operation.AccountID)
	defer unlock()
	operation, ok, err = app.workspaceLaunchOperation(ctx, operationID)
	if err != nil || !ok || operation.RecoveryPlan == nil || operation.RecoveryPlan.PlanID != planID || operation.RecoveryPlan.PlanDigest != planDigest {
		if err == nil {
			err = errBillingReviewIdentity
		}
		return workspaceRecoveryPlan{}, err
	}
	release, err := currentWorkspaceRecoveryReleaseBinding()
	if err != nil {
		return workspaceRecoveryPlan{}, err
	}
	var current workspaceRecoveryPlan
	if operation.RecoveryPlan.Action == "compute_claim_continue" {
		computeInput, computeProof, evidence, computeErr := app.workspaceComputeClaimRecoveryProofForPlan(ctx, service, operation)
		if computeErr != nil {
			return workspaceRecoveryPlan{}, computeErr
		}
		current, err = newWorkspaceComputeClaimRecoveryPlan(operation, computeInput, computeProof, evidence, release)
	} else {
		recovered, proof, proofErr := app.workspaceLaunchReadbackRecoveryProofForOperation(ctx, service, operation)
		if proofErr != nil {
			return workspaceRecoveryPlan{}, proofErr
		}
		expectedPrivateIP, privateIPErr := app.workspaceRecoveryPlanExpectedPrivateIP(recovered)
		if privateIPErr != nil {
			return workspaceRecoveryPlan{}, privateIPErr
		}
		current, err = newWorkspaceReadbackRecoveryPlan(recovered, proof, release, expectedPrivateIP)
	}
	if err != nil {
		return workspaceRecoveryPlan{}, err
	}
	validated := *operation.RecoveryPlan
	validated.ValidatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	validated.Mismatches = workspaceRecoveryPlanMismatches(validated, current)
	validated.Status, validated.ErrorCode = "validated", ""
	if len(validated.Mismatches) != 0 {
		validated.Status, validated.ErrorCode = "blocked", "identity_mismatch"
	}
	operation.RecoveryPlan = &validated
	if err := app.persistWorkspaceLaunch(ctx, &operation); err != nil {
		return workspaceRecoveryPlan{}, err
	}
	return workspaceRecoveryPlanProjection(operation), nil
}

func newWorkspaceRecoveryExecution(plan workspaceRecoveryPlan, proof workspaceLaunchReadbackRecoveryProof, decision string) workspaceRecoveryExecution {
	executionID := "recovery-exec-" + workspaceRecoveryAuthorityDigest([]string{plan.PlanID, plan.PlanDigest, decision})[:20]
	approval := workspaceLaunchReadbackRecoveryApproval{
		SchemaVersion:        1,
		ApprovalID:           "recovery-approval-" + plan.PlanDigest[:20],
		ExpiresAt:            time.Now().UTC().Add(15 * time.Minute).Format(time.RFC3339),
		MergedMainSHA:        plan.ReleaseBinding.MainSHA,
		CloudImageDigest:     plan.ReleaseBinding.CloudImageDigest,
		WorkspaceImageDigest: proof.WorkspaceImageDigest,
		Confirmation:         workspaceLaunchReadbackRecoveryConfirmation,
		IdempotencyKey:       executionID,
		RecoveryKey:          "recovery-plan-" + plan.PlanDigest[:20],
		Stage:                proof.Stage,
		Customer:             proof.Customer,
		Target:               proof.Target,
		Resources:            proof.Resources,
		OperationIDs:         proof.OperationIDs,
		AttemptBudget:        proof.AttemptBudget,
		AllowedWrites:        append([]string(nil), proof.AllowedWrites...),
		ForbiddenWrites:      append([]string(nil), proof.ForbiddenWrites...),
	}
	approval.ApprovalDigest = workspaceLaunchReadbackRecoveryApprovalDigest(approval)
	now := time.Now().UTC()
	return workspaceRecoveryExecution{
		ExecutionID:    executionID,
		RunIdentity:    "control-plane-run-" + workspaceRecoveryAuthorityDigest([]string{executionID, approval.ApprovalDigest})[:20],
		PlanID:         plan.PlanID,
		PlanDigest:     plan.PlanDigest,
		ApprovalDigest: approval.ApprovalDigest,
		Decision:       decision,
		Status:         "running",
		LeaseToken:     workspaceRecoveryAuthorityDigest([]string{"lease", executionID, approval.ApprovalDigest}),
		LeaseExpiresAt: now.Add(5 * time.Minute).Format(time.RFC3339Nano),
		StartedAt:      now.Format(time.RFC3339Nano),
		Approval:       &approval,
	}
}

func newWorkspaceComputeClaimRecoveryExecution(operation workspaceLaunchOperation, plan workspaceRecoveryPlan, input workspaceComputeClaimRecoveryRequest, proof clients.ComputeClaimRecoveryProof, customer workspaceLaunchReadbackRecoveryCustomer, decision string) (workspaceRecoveryExecution, error) {
	targetOperation := operation
	if !persistWorkspaceComputeClaimIdentityFromProof(&targetOperation, proof) {
		return workspaceRecoveryExecution{}, errWorkspaceComputeClaimIdentity
	}
	executionID := "recovery-exec-" + workspaceRecoveryAuthorityDigest([]string{plan.PlanID, plan.PlanDigest, decision})[:20]
	now := time.Now().UTC()
	binding := workspaceComputeClaimApprovalBinding{
		SchemaVersion:        2,
		ApprovalID:           "recovery-approval-" + plan.PlanDigest[:20],
		ExpiresAt:            now.Add(15 * time.Minute).Format(time.RFC3339),
		MergedMainSHA:        plan.ReleaseBinding.MainSHA,
		CloudImageDigest:     plan.ReleaseBinding.CloudImageDigest,
		WorkspaceImageDigest: operation.WorkspaceImageDigest,
		Confirmation:         "RECOVER_PROVEN_COMPUTE_AND_CONTINUE_ORIGINAL_LAUNCH",
		IdempotencyKey:       executionID,
		RecoveryKey:          "recovery-plan-" + plan.PlanDigest[:20],
		Customer:             workspaceComputeClaimApprovalCustomer{Email: customer.Email, AccountID: operation.AccountID},
		Target:               workspaceComputeClaimApprovalTargetFromOperation(targetOperation),
		Resources:            workspaceComputeClaimExpectedResources(targetOperation, proof.StorageState, proof.StorageProviderResourceID),
		AttemptLimits: workspaceComputeClaimAttemptLimits{
			Claim:   workspaceComputeClaimProviderAttemptLimits{Sub2API: 0, Tencent: 5, Kubernetes: 1},
			Storage: 1, Attachment: 1, Secret: 1, Runtime: 1, Activation: 1, Receipt: 1,
		},
		AllowedWrites:   workspaceComputeClaimAllowedWritesForStorage(proof.StorageState),
		ForbiddenWrites: append([]string(nil), workspaceComputeClaimForbiddenWrites...),
	}
	binding.ApprovalDigest = workspaceComputeClaimApprovalDigest(binding)
	input.MachineName, input.NodeName, input.CVMInstanceID, input.PrivateIP = proof.MachineName, proof.NodeName, proof.CVMInstanceID, proof.PrivateIP
	input.PoolID, input.InstanceType, input.Zone = proof.PoolID, proof.InstanceType, proof.Zone
	input.ApprovalID, input.ApprovalDigest, input.ExpiresAt = binding.ApprovalID, binding.ApprovalDigest, binding.ExpiresAt
	input.MergedMainSHA, input.CloudImageDigest, input.WorkspaceImageDigest = binding.MergedMainSHA, binding.CloudImageDigest, binding.WorkspaceImageDigest
	input.CustomerEmail, input.RecoveryKey, input.Confirmation = binding.Customer.Email, binding.RecoveryKey, binding.Confirmation
	input.Resources, input.AttemptLimits = binding.Resources, binding.AttemptLimits
	input.AllowedWrites, input.ForbiddenWrites = append([]string(nil), binding.AllowedWrites...), append([]string(nil), binding.ForbiddenWrites...)
	return workspaceRecoveryExecution{
		ExecutionID: executionID,
		RunIdentity: "control-plane-run-" + workspaceRecoveryAuthorityDigest([]string{executionID, binding.ApprovalDigest})[:20],
		PlanID:      plan.PlanID, PlanDigest: plan.PlanDigest, ApprovalDigest: binding.ApprovalDigest,
		Decision: decision, Status: "running",
		LeaseToken:     workspaceRecoveryAuthorityDigest([]string{"lease", executionID, binding.ApprovalDigest}),
		LeaseExpiresAt: now.Add(5 * time.Minute).Format(time.RFC3339Nano), StartedAt: now.Format(time.RFC3339Nano),
		ComputeClaimRequest: &input,
	}, nil
}

func (app *controlPlaneServer) reserveWorkspaceRecoveryExecution(ctx context.Context, service *controlplane.Service, operationID, planID, planDigest, decision string) (workspaceRecoveryExecution, bool, error) {
	operation, ok, err := app.workspaceLaunchOperation(ctx, operationID)
	if err != nil || !ok || operation.RecoveryPlan == nil {
		if err == nil {
			err = errBillingReviewNotFound
		}
		return workspaceRecoveryExecution{}, false, err
	}
	unlock := app.lockResource("workspace-launch", operation.AccountID)
	defer unlock()
	operation, ok, err = app.workspaceLaunchOperation(ctx, operationID)
	if err != nil || !ok || operation.RecoveryPlan == nil {
		if err == nil {
			err = errBillingReviewNotFound
		}
		return workspaceRecoveryExecution{}, false, err
	}
	plan := operation.RecoveryPlan
	if plan.PlanID != planID || plan.PlanDigest != planDigest || plan.Status != "validated" || len(plan.Mismatches) != 0 || plan.ErrorCode != "" {
		return workspaceRecoveryExecution{}, false, errBillingReviewIdentity
	}
	if operation.RecoveryExecution != nil {
		execution := *operation.RecoveryExecution
		readbackApprovalValid := execution.Approval != nil && execution.ApprovalDigest == execution.Approval.ApprovalDigest
		computeApprovalValid := execution.ComputeClaimRequest != nil && execution.ApprovalDigest == execution.ComputeClaimRequest.ApprovalDigest
		if execution.PlanID != planID || execution.PlanDigest != planDigest || execution.Decision != decision ||
			execution.ApprovalDigest == "" || !readbackApprovalValid && !computeApprovalValid {
			return workspaceRecoveryExecution{}, false, errBillingReviewIdentity
		}
		return execution, false, nil
	}

	release, err := currentWorkspaceRecoveryReleaseBinding()
	if err != nil {
		return workspaceRecoveryExecution{}, false, err
	}
	var current workspaceRecoveryPlan
	var execution workspaceRecoveryExecution
	if plan.Action == "compute_claim_continue" {
		input, proof, evidence, proofErr := app.workspaceComputeClaimRecoveryProofForPlan(ctx, service, operation)
		if proofErr != nil {
			return workspaceRecoveryExecution{}, false, proofErr
		}
		current, err = newWorkspaceComputeClaimRecoveryPlan(operation, input, proof, evidence, release)
		if err == nil {
			customer, customerErr := app.workspaceLaunchReadbackRecoveryCustomer(ctx, operation)
			if customerErr != nil {
				return workspaceRecoveryExecution{}, false, customerErr
			}
			execution, err = newWorkspaceComputeClaimRecoveryExecution(operation, *plan, input, proof, customer, decision)
		}
	} else {
		recovered, proof, proofErr := app.workspaceLaunchReadbackRecoveryProofForOperation(ctx, service, operation)
		if proofErr != nil {
			return workspaceRecoveryExecution{}, false, proofErr
		}
		expectedPrivateIP, privateIPErr := app.workspaceRecoveryPlanExpectedPrivateIP(recovered)
		if privateIPErr != nil {
			return workspaceRecoveryExecution{}, false, privateIPErr
		}
		current, err = newWorkspaceReadbackRecoveryPlan(recovered, proof, release, expectedPrivateIP)
		if err == nil {
			execution = newWorkspaceRecoveryExecution(*plan, proof, decision)
		}
	}
	if err != nil {
		return workspaceRecoveryExecution{}, false, err
	}
	if mismatches := workspaceRecoveryPlanMismatches(*plan, current); len(mismatches) != 0 {
		plan.Status, plan.ErrorCode, plan.Mismatches = "blocked", "identity_mismatch", mismatches
		if persistErr := app.persistWorkspaceLaunch(ctx, &operation); persistErr != nil {
			return workspaceRecoveryExecution{}, false, persistErr
		}
		return workspaceRecoveryExecution{}, false, errBillingReviewIdentity
	}
	operation.RecoveryExecution = &execution
	plan.Status = "executing"
	if err := app.persistWorkspaceLaunch(ctx, &operation); err != nil {
		if errors.Is(err, errWorkspaceLaunchCASConflict) {
			currentOperation, found, loadErr := app.workspaceLaunchOperation(ctx, operationID)
			if loadErr == nil && found && currentOperation.RecoveryExecution != nil && currentOperation.RecoveryExecution.PlanDigest == planDigest {
				return *currentOperation.RecoveryExecution, false, nil
			}
		}
		return workspaceRecoveryExecution{}, false, err
	}
	return execution, true, nil
}

func (app *controlPlaneServer) reacquireWorkspaceRecoveryExecution(ctx context.Context, operationID, planID, planDigest, decision string) (workspaceRecoveryExecution, bool, error) {
	operation, ok, err := app.workspaceLaunchOperation(ctx, operationID)
	if err != nil || !ok || operation.RecoveryPlan == nil || operation.RecoveryExecution == nil {
		if err == nil {
			err = errBillingReviewNotFound
		}
		return workspaceRecoveryExecution{}, false, err
	}
	unlock := app.lockResource("workspace-launch", operation.AccountID)
	defer unlock()
	operation, ok, err = app.workspaceLaunchOperation(ctx, operationID)
	if err != nil || !ok || operation.RecoveryPlan == nil || operation.RecoveryExecution == nil {
		if err == nil {
			err = errBillingReviewNotFound
		}
		return workspaceRecoveryExecution{}, false, err
	}
	execution := *operation.RecoveryExecution
	readbackApprovalValid := execution.Approval != nil && execution.ApprovalDigest == execution.Approval.ApprovalDigest
	computeApprovalValid := execution.ComputeClaimRequest != nil && execution.ApprovalDigest == execution.ComputeClaimRequest.ApprovalDigest
	if operation.RecoveryPlan.PlanID != planID || operation.RecoveryPlan.PlanDigest != planDigest || execution.PlanID != planID || execution.PlanDigest != planDigest ||
		execution.Decision != decision || execution.ApprovalDigest == "" || !readbackApprovalValid && !computeApprovalValid {
		return workspaceRecoveryExecution{}, false, errBillingReviewIdentity
	}
	if execution.Status == "completed" || execution.Status == "failed" {
		return execution, false, nil
	}
	leaseExpiresAt, parseErr := time.Parse(time.RFC3339Nano, execution.LeaseExpiresAt)
	if parseErr != nil {
		return workspaceRecoveryExecution{}, false, errBillingReviewIdentity
	}
	now := time.Now().UTC()
	if leaseExpiresAt.After(now) {
		return execution, false, nil
	}
	execution.LeaseExpiresAt = now.Add(5 * time.Minute).Format(time.RFC3339Nano)
	execution.LeaseToken = workspaceRecoveryAuthorityDigest([]string{"lease", execution.ExecutionID, execution.RunIdentity, execution.LeaseExpiresAt})
	operation.RecoveryExecution = &execution
	if err := app.persistWorkspaceLaunch(ctx, &operation); err != nil {
		if errors.Is(err, errWorkspaceLaunchCASConflict) {
			current, found, loadErr := app.workspaceLaunchOperation(ctx, operationID)
			if loadErr == nil && found && current.RecoveryExecution != nil && current.RecoveryExecution.ExecutionID == execution.ExecutionID {
				return *current.RecoveryExecution, false, nil
			}
		}
		return workspaceRecoveryExecution{}, false, err
	}
	return execution, true, nil
}

func workspaceRecoveryExecutionErrorCode(operation workspaceLaunchOperation, err error) string {
	if operation.ErrorCode != "" {
		return operation.ErrorCode
	}
	switch {
	case errors.Is(err, errBillingReviewIdentity):
		return "identity_mismatch"
	case errors.Is(err, errBillingReviewProviderFact):
		return "provider_truth_unavailable"
	case err != nil:
		return "recovery_execution_failed"
	default:
		return ""
	}
}

func (app *controlPlaneServer) finalizeWorkspaceRecoveryExecution(ctx context.Context, operationID, executionID, leaseToken string, mutationOutcome workspaceRecoveryMutationOutcome, executionErr error) (workspaceRecoveryPlan, error) {
	if leaseToken == "" {
		return workspaceRecoveryPlan{}, errBillingReviewIdentity
	}
	operation, ok, err := app.workspaceLaunchOperation(ctx, operationID)
	if err != nil || !ok || operation.RecoveryPlan == nil || operation.RecoveryExecution == nil {
		if err == nil {
			err = errBillingReviewNotFound
		}
		return workspaceRecoveryPlan{}, err
	}
	unlock := app.lockResource("workspace-launch", operation.AccountID)
	defer unlock()
	operation, ok, err = app.workspaceLaunchOperation(ctx, operationID)
	if err != nil || !ok || operation.RecoveryPlan == nil || operation.RecoveryExecution == nil || operation.RecoveryExecution.ExecutionID != executionID ||
		operation.RecoveryExecution.LeaseToken != leaseToken {
		if err == nil {
			err = errBillingReviewIdentity
		}
		return workspaceRecoveryPlan{}, err
	}
	execution, plan := operation.RecoveryExecution, operation.RecoveryPlan
	execution.MutationOutcome = mutationOutcome
	plan.Stages = workspaceRecoveryPlanStages(operation)
	if plan.Action == "compute_claim_continue" {
		computeStatus := "manual_review"
		if operation.ComputeClaimProof != nil {
			computeStatus = "completed"
		}
		plan.Stages = append([]workspaceRecoveryPlanStage{{Stage: "compute_claim", Status: computeStatus}}, plan.Stages...)
	}
	plan.URL, plan.ReceiptID = operation.URL, operation.ReceiptID
	execution.LeaseToken, execution.LeaseExpiresAt = "", ""
	switch {
	case operation.Status == "succeeded" && operation.Phase == "succeeded" && operation.URL != "" && operation.ReceiptID != "":
		execution.Status, plan.Status, execution.ErrorCode, plan.ErrorCode = "completed", "completed", "", ""
		execution.CompletedAt = time.Now().UTC().Format(time.RFC3339Nano)
	case operation.Status == "manual_review" || executionErr != nil:
		code := workspaceRecoveryExecutionErrorCode(operation, executionErr)
		execution.Status, plan.Status, execution.ErrorCode, plan.ErrorCode = "failed", "failed", code, code
		execution.CompletedAt = time.Now().UTC().Format(time.RFC3339Nano)
	default:
		execution.Status, plan.Status = "running", "executing"
	}
	if err := app.persistWorkspaceLaunch(ctx, &operation); err != nil {
		if errors.Is(err, errWorkspaceLaunchCASConflict) {
			current, found, loadErr := app.workspaceLaunchOperation(ctx, operationID)
			if loadErr == nil && found && current.RecoveryPlan != nil && current.RecoveryExecution != nil && current.RecoveryExecution.ExecutionID == executionID {
				return workspaceRecoveryPlanProjection(current), nil
			}
		}
		return workspaceRecoveryPlan{}, err
	}
	return workspaceRecoveryPlanProjection(operation), nil
}

func (app *controlPlaneServer) executeWorkspaceRecoveryPlan(ctx context.Context, service *controlplane.Service, operationID, planID, planDigest, decision, reviewer string) (workspaceRecoveryPlan, error) {
	operation, found, err := app.workspaceLaunchOperation(ctx, operationID)
	if err != nil {
		return workspaceRecoveryPlan{}, err
	}
	var execution workspaceRecoveryExecution
	if found && operation.RecoveryExecution != nil {
		execution = *operation.RecoveryExecution
		if execution.PlanID != planID || execution.PlanDigest != planDigest || execution.Decision != decision ||
			execution.Approval == nil && execution.ComputeClaimRequest == nil {
			return workspaceRecoveryPlan{}, errBillingReviewIdentity
		}
		if execution.Status == "completed" || execution.Status == "failed" {
			return workspaceRecoveryPlanProjection(operation), nil
		}
		var won bool
		execution, won, err = app.reacquireWorkspaceRecoveryExecution(ctx, operationID, planID, planDigest, decision)
		if err != nil {
			return workspaceRecoveryPlan{}, err
		}
		if !won {
			current, currentFound, loadErr := app.workspaceLaunchOperation(ctx, operationID)
			if loadErr != nil || !currentFound {
				return workspaceRecoveryPlan{}, errors.Join(loadErr, errBillingReviewNotFound)
			}
			return workspaceRecoveryPlanProjection(current), nil
		}
	} else {
		validated, validateErr := app.validateWorkspaceRecoveryPlan(ctx, service, operationID, planID, planDigest)
		if validateErr != nil {
			return workspaceRecoveryPlan{}, validateErr
		}
		if validated.Status != "validated" || len(validated.Mismatches) != 0 {
			return workspaceRecoveryPlan{}, errBillingReviewIdentity
		}
		var won bool
		execution, won, err = app.reserveWorkspaceRecoveryExecution(ctx, service, operationID, planID, planDigest, decision)
		if err != nil {
			return workspaceRecoveryPlan{}, err
		}
		if !won {
			current, currentFound, loadErr := app.workspaceLaunchOperation(ctx, operationID)
			if loadErr != nil || !currentFound {
				return workspaceRecoveryPlan{}, errors.Join(loadErr, errBillingReviewNotFound)
			}
			return workspaceRecoveryPlanProjection(current), nil
		}
	}
	var executionErr error
	mutationOutcome := workspaceRecoveryMutationOutcome{Status: "unknown", Source: "recovery_execution"}
	if execution.ComputeClaimRequest != nil {
		claimProof, claimErr := app.claimWorkspaceCompute(ctx, service, *execution.ComputeClaimRequest, execution.ExecutionID)
		mutationOutcome = workspaceRecoveryMutationOutcomeFromComputeClaim(claimProof)
		executionErr = claimErr
		if executionErr == nil {
			current, currentFound, loadErr := app.workspaceLaunchOperation(ctx, operationID)
			if loadErr != nil || !currentFound {
				executionErr = errors.Join(loadErr, errBillingReviewNotFound)
			} else if current.Status == "preparing" && current.Phase == "storage_fulfilling" {
				executionErr = app.fulfillWorkspaceLaunch(ctx, service, &current)
			}
		}
	} else if execution.Approval != nil {
		_, _, executionErr = app.recoverWorkspaceLaunchReviewWithReplay(ctx, service, billingReviewResolutionInput{
			ResourceType: "workspace_launch", ResourceID: operationID, AccountID: execution.Approval.Customer.AccountID,
			BillingOperationID: operationID, EvidenceRef: "recovery-plan:" + planID, IdempotencyKey: execution.ExecutionID,
			Reviewer: reviewer, ReadbackApproval: execution.Approval,
		})
	} else {
		return workspaceRecoveryPlan{}, errBillingReviewIdentity
	}
	return app.finalizeWorkspaceRecoveryExecution(ctx, operationID, execution.ExecutionID, execution.LeaseToken, mutationOutcome, executionErr)
}
