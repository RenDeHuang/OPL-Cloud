package protectedresource

import "testing"

func TestGuardRejectsProtectedSystemIdentitiesAndPackagePoolMismatch(t *testing.T) {
	config := Config{
		SystemNodePoolID:  "np-system",
		SystemMachineID:   "machine-system",
		SystemNodeName:    "10.66.0.42",
		SystemMachineType: "NativeCVM",
		SystemCVMID:       "ins-system",
		PackageNodePools:  map[string]string{"basic": "np-basic", "pro": "np-pro"},
	}

	if err := config.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	for _, test := range []struct {
		name   string
		target Target
	}{
		{name: "system pool", target: Target{NodePoolID: "np-system"}},
		{name: "system machine", target: Target{MachineID: "machine-system"}},
		{name: "system node", target: Target{NodeName: "10.66.0.42"}},
		{name: "system CVM", target: Target{CVMID: "ins-system"}},
		{name: "Basic on Pro pool", target: Target{PackageID: "basic", NodePoolID: "np-pro"}},
		{name: "Pro on Basic pool", target: Target{PackageID: "pro", NodePoolID: "np-basic"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := config.Check(test.target); err == nil {
				t.Fatalf("target must be rejected: %#v", test.target)
			}
		})
	}

	for _, target := range []Target{
		{PackageID: "basic", NodePoolID: "np-basic", MachineID: "machine-basic", NodeName: "10.0.0.8", CVMID: "ins-basic"},
		{PackageID: "pro", NodePoolID: "np-pro", MachineID: "machine-pro", NodeName: "10.0.0.9", CVMID: "ins-pro"},
		{},
	} {
		if err := config.Check(target); err != nil {
			t.Fatalf("customer target rejected: target=%#v err=%v", target, err)
		}
	}
	if err := config.Check(Target{PackageID: "starter", NodePoolID: "np-basic"}); err != ErrPackagePoolMismatch {
		t.Fatalf("unknown package must fail closed: %v", err)
	}
}

func TestGuardConfigurationFailsClosed(t *testing.T) {
	valid := Config{
		SystemNodePoolID:  "np-system",
		SystemMachineID:   "machine-system",
		SystemNodeName:    "10.66.0.42",
		SystemMachineType: "NativeCVM",
		SystemCVMID:       "ins-system",
		PackageNodePools:  map[string]string{"basic": "np-basic", "pro": "np-pro"},
	}
	for _, test := range []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "missing system machine type", mutate: func(value *Config) { value.SystemMachineType = "" }},
		{name: "missing system CVM", mutate: func(value *Config) { value.SystemCVMID = "" }},
		{name: "non CVM system with CVM identity", mutate: func(value *Config) { value.SystemMachineType = "Native" }},
		{name: "shared customer pools", mutate: func(value *Config) { value.PackageNodePools["pro"] = value.PackageNodePools["basic"] }},
		{name: "Basic is system pool", mutate: func(value *Config) { value.PackageNodePools["basic"] = value.SystemNodePoolID }},
		{name: "Pro is system pool", mutate: func(value *Config) { value.PackageNodePools["pro"] = value.SystemNodePoolID }},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := valid
			test.mutate(&config)
			if err := config.Validate(); err == nil {
				t.Fatalf("invalid config accepted: %#v", config)
			}
		})
	}
}

func TestGuardAcceptsExplicitNonCVMSystemIdentityWithoutCVM(t *testing.T) {
	for _, machineType := range []string{"Native", "CXM"} {
		t.Run(machineType, func(t *testing.T) {
			config := Config{
				SystemNodePoolID:  "np-system",
				SystemMachineID:   "machine-system",
				SystemNodeName:    "10.66.0.42",
				SystemMachineType: machineType,
				PackageNodePools:  map[string]string{"basic": "np-basic", "pro": "np-pro"},
			}
			if err := config.Validate(); err != nil {
				t.Fatalf("explicit non-CVM system config rejected: %v", err)
			}
			if err := config.Check(Target{PackageID: "basic", NodePoolID: "np-basic", CVMID: "ins-basic"}); err != nil {
				t.Fatalf("customer CVM rejected by non-CVM system guard: %v", err)
			}
			for _, target := range []Target{{NodePoolID: "np-system"}, {MachineID: "machine-system"}, {NodeName: "10.66.0.42"}} {
				if err := config.Check(target); err != ErrProtectedResource {
					t.Fatalf("protected target %#v err=%v", target, err)
				}
			}
		})
	}
}

func TestGuardIgnoresUnavailableProviderPackages(t *testing.T) {
	config := FromMap(map[string]string{
		"OPL_SYSTEM_COMPUTE_NODE_POOL_ID":              "np-system",
		"OPL_SYSTEM_COMPUTE_MACHINE_ID":                "machine-system",
		"OPL_SYSTEM_COMPUTE_NODE_NAME":                 "10.66.0.42",
		"OPL_SYSTEM_COMPUTE_MACHINE_TYPE":              "NativeCVM",
		"OPL_SYSTEM_COMPUTE_CVM_ID":                    "ins-system",
		"OPL_FABRIC_TENCENT_TKE_PROVIDER_PROFILE_JSON": `{"schemaVersion":1,"packages":[{"id":"basic","available":false,"nodePoolId":"np-basic"},{"id":"pro","available":true,"nodePoolId":"np-pro"}]}`,
	})
	if err := config.Check(Target{PackageID: "basic", NodePoolID: "np-basic"}); err != ErrPackagePoolMismatch {
		t.Fatalf("unavailable package must fail closed: %v", err)
	}
	if err := config.Check(Target{PackageID: "pro", NodePoolID: "np-pro"}); err != nil {
		t.Fatalf("available package rejected: %v", err)
	}
}
