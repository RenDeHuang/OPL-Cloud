package protectedresource

import "testing"

func TestGuardRejectsProtectedSystemIdentitiesAndPackagePoolMismatch(t *testing.T) {
	config := Config{
		SystemNodePoolID: "np-system",
		SystemMachineID:  "machine-system",
		SystemNodeName:   "10.66.0.42",
		SystemCVMID:      "ins-system",
		BasicNodePoolID:  "np-basic",
		ProNodePoolID:    "np-pro",
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
}

func TestGuardConfigurationFailsClosed(t *testing.T) {
	valid := Config{
		SystemNodePoolID: "np-system",
		SystemMachineID:  "machine-system",
		SystemNodeName:   "10.66.0.42",
		SystemCVMID:      "ins-system",
		BasicNodePoolID:  "np-basic",
		ProNodePoolID:    "np-pro",
	}
	for _, test := range []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "missing system CVM", mutate: func(value *Config) { value.SystemCVMID = "" }},
		{name: "shared customer pools", mutate: func(value *Config) { value.ProNodePoolID = value.BasicNodePoolID }},
		{name: "Basic is system pool", mutate: func(value *Config) { value.BasicNodePoolID = value.SystemNodePoolID }},
		{name: "Pro is system pool", mutate: func(value *Config) { value.ProNodePoolID = value.SystemNodePoolID }},
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
