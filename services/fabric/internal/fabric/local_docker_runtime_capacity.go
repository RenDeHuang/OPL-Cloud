package fabric

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const (
	localDockerRuntimeReservationSchemaVersion  = 1
	localDockerRuntimeReservationInventoryError = "local_docker_runtime_reservation_inventory_invalid"
)

type localDockerHostCapacity struct{ CPUs, MemoryBytes uint64 }
type localDockerRuntimeReservation struct {
	SchemaVersion int    `json:"schemaVersion"`
	WorkspaceID   string `json:"workspaceId"`
	ResourceID    string `json:"resourceId"`
	PackageID     string `json:"packageId"`
	NanoCPUs      uint64 `json:"nanoCpus"`
	MemoryBytes   uint64 `json:"memoryBytes"`
}
type localDockerInfoCapacity struct {
	NCPU     uint64 `json:"NCPU"`
	MemTotal uint64 `json:"MemTotal"`
}

func (p *LocalDockerProvider) readDockerHostCapacity(ctx context.Context) (localDockerHostCapacity, error) {
	body, err := p.runner.Run(ctx, nil, "info", "--format", "{{json .}}")
	if err != nil {
		return localDockerHostCapacity{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	var info localDockerInfoCapacity
	if err := decoder.Decode(&info); err != nil || decoder.Decode(&struct{}{}) != io.EOF || info.NCPU == 0 || info.MemTotal == 0 || info.NCPU > ^uint64(0)/uint64(localDockerNanoCPUsPerCPU) {
		return localDockerHostCapacity{}, fmt.Errorf("local_docker_host_capacity_readback_invalid")
	}
	return localDockerHostCapacity{CPUs: info.NCPU, MemoryBytes: info.MemTotal}, nil
}
func validLocalDockerRuntimeReservation(res localDockerRuntimeReservation) bool {
	return res.SchemaVersion == localDockerRuntimeReservationSchemaVersion && res.WorkspaceID != "" && res.ResourceID != "" && res.PackageID != "" && res.NanoCPUs > 0 && res.MemoryBytes > 0
}
func localDockerRuntimeReservationName(resourceID string) string {
	return localDockerRuntimeReservationDirectory + "/" + resourceID + ".json"
}

func writeLocalDockerRuntimeReservation(root *os.Root, reservation localDockerRuntimeReservation) error {
	if !validLocalDockerRuntimeReservation(reservation) {
		return fmt.Errorf("local_docker_runtime_reservation_invalid")
	}
	if err := ensureLocalDockerStorageDirectory(root, localDockerRuntimeReservationDirectory, 0700); err != nil {
		return err
	}
	target := localDockerRuntimeReservationName(reservation.ResourceID)
	if existing, err := readLocalDockerRuntimeReservation(root, target); err == nil {
		if existing != reservation {
			return fmt.Errorf("local_docker_runtime_reservation_conflict")
		}
		return nil
	} else if !errors.Is(err, ErrWorkspaceLaunchResourceAbsent) {
		return err
	}
	body, err := json.Marshal(reservation)
	if err != nil {
		return err
	}
	tmp := localDockerRuntimeReservationDirectory + "/.reserve-" + reservation.ResourceID
	file, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0400)
	if err != nil {
		return err
	}
	if err = file.Chmod(0400); err == nil {
		_, err = file.Write(body)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := root.Rename(tmp, target); err != nil {
		return err
	}
	return syncLocalDockerStorageDirectory(root, localDockerRuntimeReservationDirectory)
}
func readLocalDockerRuntimeReservation(root *os.Root, name string) (localDockerRuntimeReservation, error) {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return localDockerRuntimeReservation{}, ErrWorkspaceLaunchResourceAbsent
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0400 {
		return localDockerRuntimeReservation{}, fmt.Errorf("local_docker_runtime_reservation_invalid")
	}
	body, err := root.ReadFile(name)
	if err != nil {
		return localDockerRuntimeReservation{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var reservation localDockerRuntimeReservation
	if decoder.Decode(&reservation) != nil || decoder.Decode(&struct{}{}) != io.EOF || !validLocalDockerRuntimeReservation(reservation) {
		return localDockerRuntimeReservation{}, fmt.Errorf("local_docker_runtime_reservation_invalid")
	}
	return reservation, nil
}
func removeLocalDockerRuntimeReservation(root *os.Root, resourceID string) error {
	if err := root.Remove(localDockerRuntimeReservationName(resourceID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncLocalDockerStorageDirectory(root, localDockerRuntimeReservationDirectory)
}

func (p *LocalDockerProvider) localDockerRuntimeCapacityAdmission(ctx context.Context, root *os.Root, reservation localDockerRuntimeReservation) error {
	host, err := p.readDockerHostCapacity(ctx)
	if err != nil {
		return err
	}
	if err := p.validateRuntimeReservationInventory(ctx, root); err != nil {
		return err
	}
	directory, err := root.Open(localDockerRuntimeReservationDirectory)
	if errors.Is(err, os.ErrNotExist) {
		directory = nil
	} else if err != nil {
		return fmt.Errorf("local_docker_runtime_reservation_inventory_unavailable")
	}
	var cpu, memory uint64
	if directory != nil {
		entries, readErr := directory.ReadDir(-1)
		directory.Close()
		if readErr != nil {
			return fmt.Errorf("local_docker_runtime_reservation_inventory_unavailable")
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				return fmt.Errorf("local_docker_runtime_reservation_invalid")
			}
			existing, readErr := readLocalDockerRuntimeReservation(root, localDockerRuntimeReservationDirectory+"/"+entry.Name())
			if readErr != nil {
				return readErr
			}
			if existing.ResourceID == reservation.ResourceID {
				continue
			}
			if ^uint64(0)-cpu < existing.NanoCPUs || ^uint64(0)-memory < existing.MemoryBytes {
				return fmt.Errorf("local_docker_runtime_capacity_unavailable")
			}
			cpu += existing.NanoCPUs
			memory += existing.MemoryBytes
		}
	}
	if ^uint64(0)-cpu < reservation.NanoCPUs || ^uint64(0)-memory < reservation.MemoryBytes {
		return fmt.Errorf("local_docker_runtime_capacity_unavailable")
	}
	cpu += reservation.NanoCPUs
	memory += reservation.MemoryBytes
	if cpu > host.CPUs*uint64(localDockerNanoCPUsPerCPU) || memory > host.MemoryBytes {
		return fmt.Errorf("local_docker_runtime_capacity_insufficient")
	}
	return nil
}

func (p *LocalDockerProvider) validateRuntimeReservationInventory(ctx context.Context, root *os.Root) error {
	output, err := p.runner.Run(ctx, nil, "container", "ls", "-a", "--no-trunc", "--filter", "label=opl.fabric.kind=runtime", "--format", "{{json .}}")
	if err != nil {
		return err
	}
	seen := make(map[string]struct{})
	seenResources := make(map[string]struct{})
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var row dockerObjectInventoryRow
		if json.Unmarshal([]byte(line), &row) != nil || row.ID == "" || firstNonEmpty(row.Name, row.Names) == "" || row.Name != "" && row.Names != "" && row.Name != row.Names {
			return fmt.Errorf("local_docker_runtime_reservation_inventory_invalid")
		}
		name := firstNonEmpty(row.Name, row.Names)
		if _, exists := seen[name]; exists {
			return fmt.Errorf("local_docker_runtime_reservation_inventory_invalid")
		}
		seen[name] = struct{}{}
		container, exists, inspectErr := p.inspectContainer(ctx, name)
		if inspectErr != nil || !exists || container.ID != row.ID {
			return firstNonNil(inspectErr, fmt.Errorf(localDockerRuntimeReservationInventoryError))
		}
		reservation, reconcileErr := reconcileLocalDockerRuntimeReservation(root, container)
		if reconcileErr != nil {
			return reconcileErr
		}
		if _, duplicate := seenResources[reservation.ResourceID]; duplicate {
			return fmt.Errorf(localDockerRuntimeReservationInventoryError)
		}
		seenResources[reservation.ResourceID] = struct{}{}
	}
	directory, err := root.Open(localDockerRuntimeReservationDirectory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("local_docker_runtime_reservation_inventory_unavailable")
	}
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if readErr != nil || closeErr != nil {
		return fmt.Errorf("local_docker_runtime_reservation_inventory_unavailable")
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			return fmt.Errorf("local_docker_runtime_reservation_invalid")
		}
		reservation, readErr := readLocalDockerRuntimeReservation(root, localDockerRuntimeReservationDirectory+"/"+entry.Name())
		if readErr != nil || entry.Name() != reservation.ResourceID+".json" {
			return firstNonNil(readErr, fmt.Errorf(localDockerRuntimeReservationInventoryError))
		}
		name := localRuntimeName(reservation.WorkspaceID)
		if _, exists := seen[name]; exists {
			if _, resourceExists := seenResources[reservation.ResourceID]; !resourceExists {
				return fmt.Errorf(localDockerRuntimeReservationInventoryError)
			}
			continue
		}
		container, exists, inspectErr := p.inspectContainer(ctx, name)
		if inspectErr != nil {
			return inspectErr
		}
		if !exists {
			if err := removeLocalDockerRuntimeReservation(root, reservation.ResourceID); err != nil {
				return err
			}
			continue
		}
		reconciled, reconcileErr := reconcileLocalDockerRuntimeReservation(root, container)
		if reconcileErr != nil {
			return reconcileErr
		}
		if reconciled != reservation {
			return fmt.Errorf(localDockerRuntimeReservationInventoryError)
		}
	}
	return nil
}

func localDockerRuntimeReservationFromContainer(container dockerContainerInspect) (localDockerRuntimeReservation, bool) {
	labels := container.Config.Labels
	if labels["opl.fabric.provider"] != "local-docker" || labels["opl.fabric.kind"] != "runtime" ||
		labels["opl.workspace.id"] == "" || labels["opl.resource.id"] == "" || labels[localDockerComputePackageLabel] == "" ||
		container.Name != localRuntimeName(labels["opl.workspace.id"]) || labels["opl.resource.id"] != localRuntimeID(labels["opl.workspace.id"]) ||
		container.HostConfig.NanoCPUs <= 0 || container.HostConfig.Memory <= 0 || container.HostConfig.MemorySwap != container.HostConfig.Memory {
		return localDockerRuntimeReservation{}, false
	}
	return localDockerRuntimeReservation{
		SchemaVersion: localDockerRuntimeReservationSchemaVersion,
		WorkspaceID:   labels["opl.workspace.id"],
		ResourceID:    labels["opl.resource.id"],
		PackageID:     labels[localDockerComputePackageLabel],
		NanoCPUs:      uint64(container.HostConfig.NanoCPUs),
		MemoryBytes:   uint64(container.HostConfig.Memory),
	}, true
}

func reconcileLocalDockerRuntimeReservation(root *os.Root, container dockerContainerInspect) (localDockerRuntimeReservation, error) {
	expected, ok := localDockerRuntimeReservationFromContainer(container)
	if !ok {
		return localDockerRuntimeReservation{}, fmt.Errorf(localDockerRuntimeReservationInventoryError)
	}
	stored, err := readLocalDockerRuntimeReservation(root, localDockerRuntimeReservationName(expected.ResourceID))
	if errors.Is(err, ErrWorkspaceLaunchResourceAbsent) {
		if err := writeLocalDockerRuntimeReservation(root, expected); err != nil {
			return localDockerRuntimeReservation{}, err
		}
		return expected, nil
	}
	if err != nil {
		return localDockerRuntimeReservation{}, err
	}
	if stored != expected {
		return localDockerRuntimeReservation{}, fmt.Errorf(localDockerRuntimeReservationInventoryError)
	}
	return stored, nil
}

func localDockerRuntimeReservationFor(input WorkspaceRuntimeInput, limits localDockerRuntimeCgroupLimits) localDockerRuntimeReservation {
	return localDockerRuntimeReservation{SchemaVersion: localDockerRuntimeReservationSchemaVersion, WorkspaceID: input.WorkspaceID, ResourceID: localRuntimeID(input.WorkspaceID), PackageID: limits.PackageID, NanoCPUs: uint64(limits.NanoCPUs), MemoryBytes: uint64(limits.Memory)}
}

func ensureLocalDockerRuntimeReservation(root *os.Root, reservation localDockerRuntimeReservation) error {
	existing, err := readLocalDockerRuntimeReservation(root, localDockerRuntimeReservationName(reservation.ResourceID))
	if err == nil {
		if existing != reservation {
			return fmt.Errorf("local_docker_runtime_reservation_conflict")
		}
		return nil
	}
	if !errors.Is(err, ErrWorkspaceLaunchResourceAbsent) {
		return err
	}
	return writeLocalDockerRuntimeReservation(root, reservation)
}
