//go:build linux

package fabric

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLocalDockerReadMountInfoPreservesEscapedMountIdentityAndQuotaOptions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mountinfo")
	if err := os.WriteFile(path, []byte("36 25 7:1 / /mnt/opl\\040quota rw,relatime - ext4 /dev/loop0 rw,prjquota\n"), 0600); err != nil {
		t.Fatal(err)
	}
	mounts, err := localDockerReadMountInfo(path)
	if err != nil || len(mounts) != 1 {
		t.Fatalf("mounts=%#v err=%v", mounts, err)
	}
	mount := mounts[0]
	if mount.device != "7:1" || mount.root != "/" || mount.mountPoint != "/mnt/opl quota" || mount.filesystem != "ext4" {
		t.Fatalf("mount=%#v", mount)
	}
	if _, ok := mount.mountOptions["prjquota"]; !ok {
		t.Fatalf("mount options=%#v", mount.mountOptions)
	}
}
