package server

import (
	"regexp"
	"strings"
	"testing"
)

func TestWorkspaceLaunchDescriptorRequestHashUsesCanonicalSHA256(t *testing.T) {
	t.Setenv("OPL_WORKSPACE_IMAGE", "repo.example/workspace@sha256:"+strings.Repeat("b", 64))

	descriptor, err := newWorkspaceLaunchDescriptor(
		"acct-alpha",
		"usr-alpha",
		"Tencent Basic CPU Workspace",
		"tencent-basic-cpu",
		50,
		true,
		"2026-08-12",
		"launch-key-alpha",
	)
	if err != nil {
		t.Fatal(err)
	}

	const want = "7c48b5a1f3fa0a51f510e16c46212ec4b5e9d4b608c51e32c97a3331898b6c1b"
	if descriptor.RequestHash != want {
		t.Fatalf("request hash=%q want %q", descriptor.RequestHash, want)
	}
	if !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(descriptor.RequestHash) {
		t.Fatalf("request hash format=%q", descriptor.RequestHash)
	}
}

func TestWorkspaceLaunchDescriptorRequestHashDriftsOnCanonicalFieldChanges(t *testing.T) {
	t.Setenv("OPL_WORKSPACE_IMAGE", "repo.example/workspace@sha256:"+strings.Repeat("b", 64))

	base, err := newWorkspaceLaunchDescriptor("acct-alpha", "usr-alpha", "Tencent Basic CPU Workspace", "tencent-basic-cpu", 50, true, "2026-08-12", "launch-key-alpha")
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		got  func() (workspaceLaunchDescriptor, error)
	}{
		{name: "accountId", got: func() (workspaceLaunchDescriptor, error) {
			return newWorkspaceLaunchDescriptor("acct-beta", "usr-alpha", "Tencent Basic CPU Workspace", "tencent-basic-cpu", 50, true, "2026-08-12", "launch-key-alpha")
		}},
		{name: "ownerUserId", got: func() (workspaceLaunchDescriptor, error) {
			return newWorkspaceLaunchDescriptor("acct-alpha", "usr-beta", "Tencent Basic CPU Workspace", "tencent-basic-cpu", 50, true, "2026-08-12", "launch-key-alpha")
		}},
		{name: "name", got: func() (workspaceLaunchDescriptor, error) {
			return newWorkspaceLaunchDescriptor("acct-alpha", "usr-alpha", "Tencent Basic CPU Workspace 2", "tencent-basic-cpu", 50, true, "2026-08-12", "launch-key-alpha")
		}},
		{name: "packageId", got: func() (workspaceLaunchDescriptor, error) {
			return newWorkspaceLaunchDescriptor("acct-alpha", "usr-alpha", "Tencent Basic CPU Workspace", "tencent-basic-cpu-v2", 50, true, "2026-08-12", "launch-key-alpha")
		}},
		{name: "sizeGb", got: func() (workspaceLaunchDescriptor, error) {
			return newWorkspaceLaunchDescriptor("acct-alpha", "usr-alpha", "Tencent Basic CPU Workspace", "tencent-basic-cpu", 51, true, "2026-08-12", "launch-key-alpha")
		}},
		{name: "autoRenew", got: func() (workspaceLaunchDescriptor, error) {
			return newWorkspaceLaunchDescriptor("acct-alpha", "usr-alpha", "Tencent Basic CPU Workspace", "tencent-basic-cpu", 50, false, "2026-08-12", "launch-key-alpha")
		}},
		{name: "priceVersion", got: func() (workspaceLaunchDescriptor, error) {
			return newWorkspaceLaunchDescriptor("acct-alpha", "usr-alpha", "Tencent Basic CPU Workspace", "tencent-basic-cpu", 50, true, "2026-08-13", "launch-key-alpha")
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			changed, err := tc.got()
			if err != nil {
				t.Fatal(err)
			}
			if changed.RequestHash == base.RequestHash {
				t.Fatalf("%s did not change request hash", tc.name)
			}
		})
	}
}

func TestWorkspaceLaunchDescriptorRequestHashExcludesImageDigestAndIdempotencyKey(t *testing.T) {
	t.Setenv("OPL_WORKSPACE_IMAGE", "repo.example/workspace@sha256:"+strings.Repeat("b", 64))

	base, err := newWorkspaceLaunchDescriptor("acct-alpha", "usr-alpha", "Tencent Basic CPU Workspace", "tencent-basic-cpu", 50, true, "2026-08-12", "launch-key-alpha")
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv("OPL_WORKSPACE_IMAGE", "repo.example/workspace@sha256:"+strings.Repeat("c", 64))
	changedImage, err := newWorkspaceLaunchDescriptor("acct-alpha", "usr-alpha", "Tencent Basic CPU Workspace", "tencent-basic-cpu", 50, true, "2026-08-12", "launch-key-alpha")
	if err != nil {
		t.Fatal(err)
	}
	if changedImage.RequestHash != base.RequestHash {
		t.Fatalf("image digest changed request hash: %q vs %q", changedImage.RequestHash, base.RequestHash)
	}

	changedKey, err := newWorkspaceLaunchDescriptor("acct-alpha", "usr-alpha", "Tencent Basic CPU Workspace", "tencent-basic-cpu", 50, true, "2026-08-12", "launch-key-beta")
	if err != nil {
		t.Fatal(err)
	}
	if changedKey.RequestHash != base.RequestHash {
		t.Fatalf("idempotency key changed request hash: %q vs %q", changedKey.RequestHash, base.RequestHash)
	}
	if changedKey.OperationID == base.OperationID {
		t.Fatalf("idempotency key did not change operation id: %q", changedKey.OperationID)
	}
}

func TestWorkspaceLaunchDescriptorExactReplayIsStable(t *testing.T) {
	t.Setenv("OPL_WORKSPACE_IMAGE", "repo.example/workspace@sha256:"+strings.Repeat("b", 64))

	first, err := newWorkspaceLaunchDescriptor("acct-alpha", "usr-alpha", "Tencent Basic CPU Workspace", "tencent-basic-cpu", 50, true, "2026-08-12", "launch-key-alpha")
	if err != nil {
		t.Fatal(err)
	}
	second, err := newWorkspaceLaunchDescriptor("acct-alpha", "usr-alpha", "Tencent Basic CPU Workspace", "tencent-basic-cpu", 50, true, "2026-08-12", "launch-key-alpha")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("descriptor replay unstable: first=%+v second=%+v", first, second)
	}
}
