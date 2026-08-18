package fabric

import (
	"os"
	"strings"
)

// Legacy fixtures exercise manifest and generic-service helpers directly. The
// live Tencent provider no longer uses this test-only shape lookup; production
// paths resolve plans from the provider profile or persisted allocation facts.
func packagePlan(packageID string) plan {
	if packageID == "pro" {
		return plan{ID: "pool-pro-8c16g", Server: "8c16g", CPU: 8, MemoryGB: 16, DiskGB: 100, InstanceType: strings.TrimSpace(os.Getenv("OPL_PRO_COMPUTE_INSTANCE_TYPE"))}
	}
	return plan{ID: "pool-basic-2c4g", Server: "2c4g", CPU: 2, MemoryGB: 4, DiskGB: 10, InstanceType: strings.TrimSpace(os.Getenv("OPL_BASIC_COMPUTE_INSTANCE_TYPE"))}
}

func workspaceManifest(input WorkspaceRuntimeInput, workspaceName, credentialSeed, runtimeID, serviceName string, compute ComputeAllocation, storage StorageVolume, tags map[string]string) []byte {
	return workspaceManifestWithGatewayPlan(input, workspaceName, credentialSeed, runtimeID, serviceName, compute, storage, tags, tencentWorkspaceRuntimeGatewayBinding{}, packagePlan(compute.PackageID))
}
