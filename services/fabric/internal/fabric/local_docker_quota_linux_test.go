//go:build linux && opl_project_quota

package fabric

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestLinuxLocalDockerProjectQuotaEnforcesHardLimit(t *testing.T) {
	root := os.Getenv("OPL_TEST_PROJECT_QUOTA_ROOT")
	if root == "" {
		t.Skip("OPL_TEST_PROJECT_QUOTA_ROOT is not configured")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	quota := linuxLocalDockerProjectQuota{root: root}
	if err := quota.Preflight(root); err != nil {
		t.Fatalf("project quota preflight: %v", err)
	}
	directory, err := os.MkdirTemp(root, ".opl-quota-integration-")
	if err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(directory, "data")
	if err := os.Mkdir(data, 0700); err != nil {
		t.Fatal(err)
	}
	projectID := localDockerInitialProjectID(fmt.Sprintf("quota-integration-%d-%d", os.Getpid(), time.Now().UnixNano()))
	const hardLimitBytes = uint64(1024 * 1024)
	if err := quota.Apply(directory, projectID, hardLimitBytes); err != nil {
		_ = os.RemoveAll(directory)
		t.Fatalf("apply project quota: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(directory)
		_ = quota.Clear(root, projectID)
	})
	for _, path := range []string{directory, data} {
		state, err := quota.Read(path)
		if err != nil || state.ProjectID != projectID || state.HardLimitBytes != hardLimitBytes || !state.Inherits {
			t.Fatalf("quota readback path=%s state=%#v err=%v", path, state, err)
		}
	}

	file, err := os.OpenFile(filepath.Join(data, "limit.bin"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	chunk := make([]byte, 256*1024)
	var writeErr error
	for written := 0; written < 2*int(hardLimitBytes); written += len(chunk) {
		if _, writeErr = file.Write(chunk); writeErr != nil {
			break
		}
	}
	if !errors.Is(writeErr, syscall.EDQUOT) {
		t.Fatalf("write beyond project hard limit err=%v", writeErr)
	}
	if err := quota.Clear(root, projectID); err != nil {
		t.Fatalf("clear project quota: %v", err)
	}
	if err := quota.Clear(root, projectID); err != nil {
		t.Fatalf("repeat clear project quota: %v", err)
	}
	record, err := quota.ReadProject(root, projectID)
	if err != nil || record.HardLimitBytes != 0 || record.SoftLimitBytes != 0 {
		t.Fatalf("cleared project quota=%#v err=%v", record, err)
	}
}
