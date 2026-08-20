package server

import (
	"errors"
	"os"
	"strings"
)

type deploymentMode string

const (
	deploymentPlatformOwned deploymentMode = "platform_owned"
	deploymentManagedTKE    deploymentMode = "managed_tke"
	deploymentCustomerOwned deploymentMode = "customer_owned"
)

type fabricProvider string

const (
	fabricLocalDocker fabricProvider = "local-docker"
	fabricTencentTKE  fabricProvider = "tencent-tke"
)

type deploymentProfile struct {
	Mode           deploymentMode
	FabricProvider fabricProvider
}

func deploymentProfileFromEnv() (deploymentProfile, error) {
	mode := deploymentMode(strings.TrimSpace(os.Getenv("OPL_DEPLOYMENT_MODE")))
	if mode == "" {
		return deploymentProfile{}, errors.New("OPL_DEPLOYMENT_MODE is required")
	}
	switch mode {
	case deploymentPlatformOwned, deploymentManagedTKE, deploymentCustomerOwned:
		if workspaceDomain() == "" {
			return deploymentProfile{}, errors.New("OPL_WORKSPACE_DOMAIN is required")
		}
		provider := fabricProvider(strings.TrimSpace(os.Getenv("OPL_FABRIC_PROVIDER")))
		if provider == "" {
			return deploymentProfile{}, errors.New("OPL_FABRIC_PROVIDER is required")
		}
		switch provider {
		case fabricLocalDocker, fabricTencentTKE:
			return deploymentProfile{Mode: mode, FabricProvider: provider}, nil
		default:
			return deploymentProfile{}, errors.New("OPL_FABRIC_PROVIDER must be local-docker or tencent-tke")
		}
	default:
		return deploymentProfile{}, errors.New("OPL_DEPLOYMENT_MODE must be platform_owned, managed_tke, or customer_owned")
	}
}

func (profile deploymentProfile) customerOwned() bool          { return profile.Mode == deploymentCustomerOwned }
func (profile deploymentProfile) resourceBillingEnabled() bool { return !profile.customerOwned() }

func boolPtr(value bool) *bool { return &value }
