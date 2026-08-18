//go:build linux

package fabric

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	localDockerFSIOCFSGETXATTR    = 0x801c581f
	localDockerFSIOCFSSETXATTR    = 0x401c5820
	localDockerFSXFLAGPROJINHERIT = 0x00000200
	localDockerPRJQUOTA           = 2
	localDockerQSETQUOTA          = 0x800008
	localDockerQGETQUOTA          = 0x800007
	localDockerQIFBLIMITS         = 0x00000001
	localDockerExt4SuperMagic     = 0xef53
	localDockerXFSSuperMagic      = 0x58465342
	// quotactl_fd uses syscall 443 on Linux amd64 and arm64.
	localDockerSYSQUOTACTLFD = 443
)

func localDockerQCMD(command, quotaType uint32) uint32 { return command<<8 | quotaType }

type localDockerFsxattr struct {
	Flags      uint32
	ExtSize    uint32
	Nextents   uint32
	ProjectID  uint32
	CowExtSize uint32
	Pad        [8]byte
}

// Linux's dqblk has the same layout on amd64 and arm64. Keep this explicit so
// readback does not depend on a distro's quota package or command output.
type localDockerDqblk struct {
	BlockHard uint64
	BlockSoft uint64
	CurSpace  uint64
	InodeHard uint64
	InodeSoft uint64
	CurInodes uint64
	BlockTime uint64
	InodeTime uint64
	Valid     uint32
	Pad       uint32
}

type linuxLocalDockerProjectQuota struct {
	root string
}

type localDockerMountInfo struct {
	device       string
	root         string
	mountPoint   string
	filesystem   string
	mountOptions map[string]struct{}
}

func newLocalDockerProjectQuota(root string) localDockerProjectQuota {
	return linuxLocalDockerProjectQuota{root: root}
}

func (q linuxLocalDockerProjectQuota) Preflight(root string) error {
	if root == "" || filepath.Clean(root) != q.root {
		return ErrLocalDockerStorageQuotaUnavailable
	}
	file, err := q.openRoot()
	if err != nil {
		return err
	}
	defer file.Close()
	if err := localDockerValidateQuotaMount(root, file); err != nil {
		return err
	}
	var attrs localDockerFsxattr
	if err := localDockerFSXAttr(file, localDockerFSIOCFSGETXATTR, &attrs); err != nil {
		return fmt.Errorf("%w: fsxattr: %v", ErrLocalDockerStorageQuotaUnavailable, err)
	}
	if attrs.ProjectID != 0 || attrs.Flags&localDockerFSXFLAGPROJINHERIT != 0 {
		return fmt.Errorf("%w: storage root must not inherit a project", ErrLocalDockerStorageQuotaUnavailable)
	}
	if _, err := localDockerReadProjectQuota(file, 1); err != nil {
		return fmt.Errorf("%w: quotactl: %v", ErrLocalDockerStorageQuotaUnavailable, err)
	}
	return nil
}

func (q linuxLocalDockerProjectQuota) Apply(path string, projectID uint32, hardLimitBytes uint64) error {
	if projectID == 0 || hardLimitBytes == 0 || hardLimitBytes%1024 != 0 {
		return ErrLocalDockerStorageQuotaUnavailable
	}
	tree, err := q.openTree(path)
	if err != nil {
		return err
	}
	defer tree.Close()
	var rootStat unix.Stat_t
	if err := unix.Fstat(int(tree.Fd()), &rootStat); err != nil {
		return fmt.Errorf("%w: project root: %v", ErrLocalDockerStorageQuotaUnavailable, err)
	}
	if err := localDockerApplyProjectID(tree, uint64(rootStat.Dev), projectID); err != nil {
		return fmt.Errorf("%w: project id: %v", ErrLocalDockerStorageQuotaUnavailable, err)
	}
	quota := localDockerDqblk{BlockHard: hardLimitBytes / 1024, BlockSoft: hardLimitBytes / 1024, Valid: localDockerQIFBLIMITS}
	if err := localDockerQuotaCtlFile(localDockerQCMD(localDockerQSETQUOTA, localDockerPRJQUOTA), tree, projectID, &quota); err != nil {
		return fmt.Errorf("%w: set limit: %v", ErrLocalDockerStorageQuotaUnavailable, err)
	}
	return localDockerVerifyProjectQuotaFile(tree, projectID, hardLimitBytes)
}

func (q linuxLocalDockerProjectQuota) Read(path string) (localDockerProjectQuotaState, error) {
	file, err := q.openTree(path)
	if err != nil {
		return localDockerProjectQuotaState{}, fmt.Errorf("%w: project path: %v", ErrLocalDockerStorageQuotaReadbackMismatch, err)
	}
	defer file.Close()
	return localDockerReadProjectQuotaState(file)
}

func (q linuxLocalDockerProjectQuota) ReadProject(root string, projectID uint32) (localDockerProjectQuotaRecord, error) {
	if projectID == 0 || filepath.Clean(root) != q.root {
		return localDockerProjectQuotaRecord{}, ErrLocalDockerStorageQuotaReadbackMismatch
	}
	file, err := q.openRoot()
	if err != nil {
		return localDockerProjectQuotaRecord{}, err
	}
	defer file.Close()
	return localDockerReadProjectQuota(file, projectID)
}

func (q linuxLocalDockerProjectQuota) Clear(root string, projectID uint32) error {
	if projectID == 0 || filepath.Clean(root) != q.root {
		return ErrLocalDockerStorageQuotaReadbackMismatch
	}
	file, err := q.openRoot()
	if err != nil {
		return err
	}
	defer file.Close()
	quota := localDockerDqblk{Valid: localDockerQIFBLIMITS}
	if err := localDockerQuotaCtlFile(localDockerQCMD(localDockerQSETQUOTA, localDockerPRJQUOTA), file, projectID, &quota); err != nil {
		return fmt.Errorf("%w: clear limit: %v", ErrLocalDockerStorageQuotaUnavailable, err)
	}
	record, err := localDockerReadProjectQuota(file, projectID)
	if err != nil || record.HardLimitBytes != 0 || record.SoftLimitBytes != 0 {
		return fmt.Errorf("%w: clear readback hard=%d soft=%d", ErrLocalDockerStorageQuotaReadbackMismatch, record.HardLimitBytes, record.SoftLimitBytes)
	}
	return nil
}

func (q linuxLocalDockerProjectQuota) openRoot() (*os.File, error) {
	if q.root == "" || !filepath.IsAbs(q.root) || filepath.Clean(q.root) != q.root {
		return nil, ErrLocalDockerStorageQuotaUnavailable
	}
	how := &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW,
		Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	}
	fd, err := unix.Openat2(unix.AT_FDCWD, q.root, how)
	if err != nil {
		return nil, fmt.Errorf("%w: open root: %v", ErrLocalDockerStorageQuotaUnavailable, err)
	}
	return os.NewFile(uintptr(fd), q.root), nil
}

func (q linuxLocalDockerProjectQuota) openTree(path string) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, ErrLocalDockerStorageQuotaUnavailable
	}
	relative, err := filepath.Rel(q.root, path)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, ErrLocalDockerStorageQuotaUnavailable
	}
	root, err := q.openRoot()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	how := &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_XDEV,
	}
	fd, err := unix.Openat2(int(root.Fd()), relative, how)
	if err != nil {
		return nil, fmt.Errorf("%w: open project tree: %v", ErrLocalDockerStorageQuotaUnavailable, err)
	}
	return os.NewFile(uintptr(fd), path), nil
}

func localDockerOpenChild(parent *os.File, name string) (*os.File, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsRune(name, '/') {
		return nil, syscall.EINVAL
	}
	how := &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_NONBLOCK | unix.O_CLOEXEC | unix.O_NOFOLLOW,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_XDEV,
	}
	fd, err := unix.Openat2(int(parent.Fd()), name, how)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func localDockerApplyProjectID(file *os.File, rootDevice uint64, projectID uint32) error {
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return err
	}
	if uint64(stat.Dev) != rootDevice {
		return syscall.EXDEV
	}
	isDirectory := stat.Mode&unix.S_IFMT == unix.S_IFDIR
	isRegular := stat.Mode&unix.S_IFMT == unix.S_IFREG
	if !isDirectory && !isRegular {
		return nil
	}
	var attrs localDockerFsxattr
	if err := localDockerFSXAttr(file, localDockerFSIOCFSGETXATTR, &attrs); err != nil {
		return err
	}
	attrs.ProjectID = projectID
	if isDirectory {
		attrs.Flags |= localDockerFSXFLAGPROJINHERIT
	} else {
		attrs.Flags &^= localDockerFSXFLAGPROJINHERIT
	}
	if err := localDockerFSXAttr(file, localDockerFSIOCFSSETXATTR, &attrs); err != nil {
		return err
	}
	if !isDirectory {
		return nil
	}
	entries, err := file.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		child, openErr := localDockerOpenChild(file, entry.Name())
		if errors.Is(openErr, syscall.ELOOP) {
			continue
		}
		if openErr != nil {
			return openErr
		}
		applyErr := localDockerApplyProjectID(child, rootDevice, projectID)
		closeErr := child.Close()
		if applyErr != nil {
			return applyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func localDockerReadProjectQuotaState(file *os.File) (localDockerProjectQuotaState, error) {
	var attrs localDockerFsxattr
	if err := localDockerFSXAttr(file, localDockerFSIOCFSGETXATTR, &attrs); err != nil || attrs.ProjectID == 0 {
		return localDockerProjectQuotaState{}, fmt.Errorf("%w: project id: %v", ErrLocalDockerStorageQuotaReadbackMismatch, err)
	}
	record, err := localDockerReadProjectQuota(file, attrs.ProjectID)
	if err != nil {
		return localDockerProjectQuotaState{}, err
	}
	return localDockerProjectQuotaState{
		ProjectID: attrs.ProjectID, HardLimitBytes: record.HardLimitBytes,
		Inherits: attrs.Flags&localDockerFSXFLAGPROJINHERIT != 0,
	}, nil
}

func localDockerReadProjectQuota(file *os.File, projectID uint32) (localDockerProjectQuotaRecord, error) {
	var quota localDockerDqblk
	if err := localDockerQuotaCtlFile(localDockerQCMD(localDockerQGETQUOTA, localDockerPRJQUOTA), file, projectID, &quota); err != nil {
		return localDockerProjectQuotaRecord{}, fmt.Errorf("%w: get limit: %v", ErrLocalDockerStorageQuotaReadbackMismatch, err)
	}
	return localDockerProjectQuotaRecord{
		HardLimitBytes: quota.BlockHard * 1024,
		SoftLimitBytes: quota.BlockSoft * 1024,
		CurrentBytes:   quota.CurSpace,
		CurrentInodes:  quota.CurInodes,
	}, nil
}

func localDockerFSXAttr(file *os.File, command uintptr, attrs *localDockerFsxattr) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, file.Fd(), command, uintptr(unsafe.Pointer(attrs)))
	if errno != 0 {
		return errno
	}
	return nil
}

func localDockerQuotaCtlFile(command uint32, filesystem *os.File, projectID uint32, quota *localDockerDqblk) error {
	_, _, errno := syscall.Syscall6(localDockerSYSQUOTACTLFD, filesystem.Fd(), uintptr(command), uintptr(projectID), uintptr(unsafe.Pointer(quota)), 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func localDockerVerifyProjectQuotaFile(file *os.File, projectID uint32, hardLimitBytes uint64) error {
	state, err := localDockerReadProjectQuotaState(file)
	if err != nil || state.ProjectID != projectID || state.HardLimitBytes != hardLimitBytes || !state.Inherits {
		return fmt.Errorf("%w: project=%d/%d hard=%d/%d inherits=%t", ErrLocalDockerStorageQuotaReadbackMismatch, state.ProjectID, projectID, state.HardLimitBytes, hardLimitBytes, state.Inherits)
	}
	return nil
}

func localDockerValidateQuotaMount(root string, file *os.File) error {
	var stats unix.Statfs_t
	if err := unix.Fstatfs(int(file.Fd()), &stats); err != nil || stats.Type != localDockerExt4SuperMagic && stats.Type != localDockerXFSSuperMagic {
		return fmt.Errorf("%w: storage root must be ext4 or XFS", ErrLocalDockerStorageQuotaUnavailable)
	}
	mounts, err := localDockerReadMountInfo("/proc/self/mountinfo")
	if err != nil {
		return fmt.Errorf("%w: mountinfo: %v", ErrLocalDockerStorageQuotaUnavailable, err)
	}
	var selected *localDockerMountInfo
	for index := range mounts {
		if filepath.Clean(mounts[index].mountPoint) == root {
			if selected != nil {
				return fmt.Errorf("%w: ambiguous storage mount", ErrLocalDockerStorageQuotaUnavailable)
			}
			selected = &mounts[index]
		}
	}
	if selected == nil || selected.root != "/" || selected.filesystem != "ext4" && selected.filesystem != "xfs" {
		return fmt.Errorf("%w: storage root must be a dedicated filesystem mount", ErrLocalDockerStorageQuotaUnavailable)
	}
	if _, ok := selected.mountOptions["prjquota"]; !ok {
		if _, ok = selected.mountOptions["pquota"]; !ok {
			return fmt.Errorf("%w: project quota mount option is missing", ErrLocalDockerStorageQuotaUnavailable)
		}
	}
	for _, mount := range mounts {
		if mount.device == selected.device && mount.root == "/" && filepath.Clean(mount.mountPoint) != root {
			return fmt.Errorf("%w: storage filesystem has multiple visible roots", ErrLocalDockerStorageQuotaUnavailable)
		}
	}
	return nil
}

func localDockerReadMountInfo(path string) ([]localDockerMountInfo, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var mounts []localDockerMountInfo
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		separator := -1
		for index, field := range fields {
			if field == "-" {
				separator = index
				break
			}
		}
		if separator < 6 || separator+3 >= len(fields) {
			return nil, fmt.Errorf("invalid mountinfo record")
		}
		options := make(map[string]struct{})
		for _, value := range []string{fields[5], fields[separator+3]} {
			for _, option := range strings.Split(value, ",") {
				options[option] = struct{}{}
			}
		}
		mounts = append(mounts, localDockerMountInfo{
			device: fields[2], root: localDockerMountInfoUnescape(fields[3]),
			mountPoint: localDockerMountInfoUnescape(fields[4]), filesystem: fields[separator+1], mountOptions: options,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return mounts, nil
}

func localDockerMountInfoUnescape(value string) string {
	return strings.NewReplacer("\\040", " ", "\\011", "\t", "\\012", "\n", "\\134", "\\").Replace(value)
}
