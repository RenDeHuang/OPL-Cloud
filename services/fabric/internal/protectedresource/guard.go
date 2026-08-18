package protectedresource

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
)

var ErrInvalidConfiguration = errors.New("protected_resource_guard_configuration_invalid")
var ErrProtectedResource = errors.New("protected_system_resource")
var ErrPackagePoolMismatch = errors.New("compute_package_node_pool_mismatch")

type Config struct {
	SystemNodePoolID  string
	SystemMachineID   string
	SystemNodeName    string
	SystemMachineType string
	SystemCVMID       string
	PackageNodePools  map[string]string
}

type Target struct {
	PackageID  string
	NodePoolID string
	MachineID  string
	NodeName   string
	CVMID      string
}

func FromEnv() Config {
	return FromMap(map[string]string{
		"OPL_SYSTEM_COMPUTE_NODE_POOL_ID":              os.Getenv("OPL_SYSTEM_COMPUTE_NODE_POOL_ID"),
		"OPL_SYSTEM_COMPUTE_MACHINE_ID":                os.Getenv("OPL_SYSTEM_COMPUTE_MACHINE_ID"),
		"OPL_SYSTEM_COMPUTE_NODE_NAME":                 os.Getenv("OPL_SYSTEM_COMPUTE_NODE_NAME"),
		"OPL_SYSTEM_COMPUTE_MACHINE_TYPE":              os.Getenv("OPL_SYSTEM_COMPUTE_MACHINE_TYPE"),
		"OPL_SYSTEM_COMPUTE_CVM_ID":                    os.Getenv("OPL_SYSTEM_COMPUTE_CVM_ID"),
		"OPL_FABRIC_TENCENT_TKE_PROVIDER_PROFILE_JSON": os.Getenv("OPL_FABRIC_TENCENT_TKE_PROVIDER_PROFILE_JSON"),
	})
}

func FromMap(values map[string]string) Config {
	packagePools := map[string]string{}
	var profile struct {
		Packages []struct {
			ID         string `json:"id"`
			Available  bool   `json:"available"`
			NodePoolID string `json:"nodePoolId"`
		} `json:"packages"`
	}
	if json.Unmarshal([]byte(values["OPL_FABRIC_TENCENT_TKE_PROVIDER_PROFILE_JSON"]), &profile) == nil {
		for _, item := range profile.Packages {
			if !item.Available {
				continue
			}
			packagePools[strings.TrimSpace(item.ID)] = strings.TrimSpace(item.NodePoolID)
		}
	}
	return Config{
		SystemNodePoolID:  strings.TrimSpace(values["OPL_SYSTEM_COMPUTE_NODE_POOL_ID"]),
		SystemMachineID:   strings.TrimSpace(values["OPL_SYSTEM_COMPUTE_MACHINE_ID"]),
		SystemNodeName:    strings.TrimSpace(values["OPL_SYSTEM_COMPUTE_NODE_NAME"]),
		SystemMachineType: strings.TrimSpace(values["OPL_SYSTEM_COMPUTE_MACHINE_TYPE"]),
		SystemCVMID:       strings.TrimSpace(values["OPL_SYSTEM_COMPUTE_CVM_ID"]),
		PackageNodePools:  packagePools,
	}
}

func (config Config) Validate() error {
	values := []string{
		config.SystemNodePoolID,
		config.SystemMachineID,
		config.SystemNodeName,
		config.SystemMachineType,
	}
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return ErrInvalidConfiguration
		}
	}
	if len(config.PackageNodePools) == 0 {
		return ErrInvalidConfiguration
	}
	seenPools := map[string]bool{config.SystemNodePoolID: true}
	for packageID, nodePoolID := range config.PackageNodePools {
		if strings.TrimSpace(packageID) == "" || strings.TrimSpace(nodePoolID) == "" || seenPools[nodePoolID] {
			return ErrInvalidConfiguration
		}
		seenPools[nodePoolID] = true
	}
	switch {
	case strings.EqualFold(config.SystemMachineType, "NativeCVM"):
		if len(config.SystemCVMID) <= len("ins-") || !strings.HasPrefix(config.SystemCVMID, "ins-") {
			return ErrInvalidConfiguration
		}
	case strings.EqualFold(config.SystemMachineType, "Native"), strings.EqualFold(config.SystemMachineType, "CXM"):
		if config.SystemCVMID != "" {
			return ErrInvalidConfiguration
		}
	default:
		return ErrInvalidConfiguration
	}
	return nil
}

func (config Config) Check(target Target) error {
	if err := config.Validate(); err != nil {
		return err
	}
	if target.NodePoolID == config.SystemNodePoolID || target.MachineID == config.SystemMachineID || target.NodeName == config.SystemNodeName ||
		(config.SystemCVMID != "" && target.CVMID == config.SystemCVMID) {
		return ErrProtectedResource
	}
	if target.PackageID != "" {
		expectedNodePoolID, knownPackage := config.PackageNodePools[target.PackageID]
		if !knownPackage || expectedNodePoolID == "" {
			return ErrPackagePoolMismatch
		}
		if target.NodePoolID != "" && target.NodePoolID != expectedNodePoolID {
			return ErrPackagePoolMismatch
		}
	}
	return nil
}
