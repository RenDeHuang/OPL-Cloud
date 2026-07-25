package protectedresource

import (
	"errors"
	"os"
	"strings"
)

var ErrInvalidConfiguration = errors.New("protected_resource_guard_configuration_invalid")
var ErrProtectedResource = errors.New("protected_system_resource")
var ErrPackagePoolMismatch = errors.New("compute_package_node_pool_mismatch")

type Config struct {
	SystemNodePoolID string
	SystemMachineID  string
	SystemNodeName   string
	SystemCVMID      string
	BasicNodePoolID  string
	ProNodePoolID    string
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
		"OPL_SYSTEM_COMPUTE_NODE_POOL_ID": os.Getenv("OPL_SYSTEM_COMPUTE_NODE_POOL_ID"),
		"OPL_SYSTEM_COMPUTE_MACHINE_ID":   os.Getenv("OPL_SYSTEM_COMPUTE_MACHINE_ID"),
		"OPL_SYSTEM_COMPUTE_NODE_NAME":    os.Getenv("OPL_SYSTEM_COMPUTE_NODE_NAME"),
		"OPL_SYSTEM_COMPUTE_CVM_ID":       os.Getenv("OPL_SYSTEM_COMPUTE_CVM_ID"),
		"OPL_BASIC_COMPUTE_NODE_POOL_ID":  os.Getenv("OPL_BASIC_COMPUTE_NODE_POOL_ID"),
		"OPL_PRO_COMPUTE_NODE_POOL_ID":    os.Getenv("OPL_PRO_COMPUTE_NODE_POOL_ID"),
	})
}

func FromMap(values map[string]string) Config {
	return Config{
		SystemNodePoolID: strings.TrimSpace(values["OPL_SYSTEM_COMPUTE_NODE_POOL_ID"]),
		SystemMachineID:  strings.TrimSpace(values["OPL_SYSTEM_COMPUTE_MACHINE_ID"]),
		SystemNodeName:   strings.TrimSpace(values["OPL_SYSTEM_COMPUTE_NODE_NAME"]),
		SystemCVMID:      strings.TrimSpace(values["OPL_SYSTEM_COMPUTE_CVM_ID"]),
		BasicNodePoolID:  strings.TrimSpace(values["OPL_BASIC_COMPUTE_NODE_POOL_ID"]),
		ProNodePoolID:    strings.TrimSpace(values["OPL_PRO_COMPUTE_NODE_POOL_ID"]),
	}
}

func (config Config) Validate() error {
	values := []string{
		config.SystemNodePoolID,
		config.SystemMachineID,
		config.SystemNodeName,
		config.SystemCVMID,
		config.BasicNodePoolID,
		config.ProNodePoolID,
	}
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return ErrInvalidConfiguration
		}
	}
	if config.BasicNodePoolID == config.ProNodePoolID || config.BasicNodePoolID == config.SystemNodePoolID || config.ProNodePoolID == config.SystemNodePoolID {
		return ErrInvalidConfiguration
	}
	return nil
}

func (config Config) Check(target Target) error {
	if err := config.Validate(); err != nil {
		return err
	}
	if target.NodePoolID == config.SystemNodePoolID || target.MachineID == config.SystemMachineID || target.NodeName == config.SystemNodeName || target.CVMID == config.SystemCVMID {
		return ErrProtectedResource
	}
	switch target.PackageID {
	case "basic":
		if target.NodePoolID != "" && target.NodePoolID != config.BasicNodePoolID {
			return ErrPackagePoolMismatch
		}
	case "pro":
		if target.NodePoolID != "" && target.NodePoolID != config.ProNodePoolID {
			return ErrPackagePoolMismatch
		}
	}
	return nil
}
