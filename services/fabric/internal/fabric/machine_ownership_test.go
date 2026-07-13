package fabric

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestMachineOwnershipClaimsOneMachinePerResourceConcurrently(t *testing.T) {
	store := NewMemoryOperationStore()
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 100)
	for i := 0; i < 100; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, created, err := store.ClaimMachine(ctx, MachineOwnership{ID: fmt.Sprintf("owner-%03d", i), ResourceID: fmt.Sprintf("resource-%03d", i), AccountID: "acct", PackageID: "basic", NodePoolID: "pool-basic", MachineID: fmt.Sprintf("machine-%03d", i), InstanceID: fmt.Sprintf("ins-%03d", i), Status: "claimed", ClaimedAt: time.Now().UTC()})
			if err != nil {
				errs <- err
			} else if !created {
				errs <- fmt.Errorf("claim %d replayed", i)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	ownerships, err := store.ListMachineOwnerships(ctx)
	if err != nil || len(ownerships) != 100 {
		t.Fatalf("ownerships = %d, %v", len(ownerships), err)
	}
}

func TestMachineOwnershipRejectsDuplicateMachineOrResource(t *testing.T) {
	store := NewMemoryOperationStore()
	ctx := context.Background()
	base := MachineOwnership{ID: "owner-a", ResourceID: "resource-a", AccountID: "acct", PackageID: "basic", NodePoolID: "pool-basic", MachineID: "machine-a", InstanceID: "ins-a", Status: "claimed", ClaimedAt: time.Now().UTC()}
	if _, _, err := store.ClaimMachine(ctx, base); err != nil {
		t.Fatal(err)
	}
	duplicateMachine := base
	duplicateMachine.ID = "owner-b"
	duplicateMachine.ResourceID = "resource-b"
	if _, _, err := store.ClaimMachine(ctx, duplicateMachine); err != ErrMachineOwnershipConflict {
		t.Fatalf("duplicate machine error = %v", err)
	}
	duplicateResource := base
	duplicateResource.MachineID = "machine-b"
	duplicateResource.InstanceID = "ins-b"
	if _, _, err := store.ClaimMachine(ctx, duplicateResource); err != ErrMachineOwnershipConflict {
		t.Fatalf("duplicate resource error = %v", err)
	}
}

func TestReleasedMachineOwnershipCanClaimReplacement(t *testing.T) {
	store := NewMemoryOperationStore()
	first := MachineOwnership{ID: "owner-first", ResourceID: "resource-alpha", AccountID: "acct-alpha", PackageID: "basic", NodePoolID: "np-basic", MachineID: "machine-first", InstanceID: "ins-first", Status: "claimed", ClaimedAt: time.Now().UTC()}
	claimed, _, err := store.ClaimMachine(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	claimed.Status = "released"
	if err := store.SaveMachineOwnership(context.Background(), claimed); err != nil {
		t.Fatal(err)
	}
	replacement := MachineOwnership{ID: "owner-second", ResourceID: first.ResourceID, AccountID: first.AccountID, PackageID: "basic", NodePoolID: "np-basic", MachineID: "machine-second", InstanceID: "ins-second", Status: "claimed", ClaimedAt: time.Now().UTC()}
	got, created, err := store.ClaimMachine(context.Background(), replacement)
	if err != nil || !created || got.MachineID != "machine-second" {
		t.Fatalf("replacement claim = %#v created=%v err=%v", got, created, err)
	}
}

func TestServiceReturnsExactMachineOwnership(t *testing.T) {
	store := NewMemoryOperationStore()
	releasedAt := time.Now().UTC().Truncate(time.Second)
	ownership := MachineOwnership{
		ID: "owner-alpha", ResourceID: "compute-alpha", AccountID: "acct-alpha", PackageID: "basic",
		NodePoolID: "np-basic", MachineID: "machine-alpha", InstanceID: "ins-alpha", NodeName: "node-alpha",
		Status: "released", ClaimedAt: releasedAt.Add(-time.Minute), ReleasedAt: &releasedAt,
	}
	if _, _, err := store.ClaimMachine(context.Background(), ownership); err != nil {
		t.Fatal(err)
	}
	service := NewServiceWithOperationStore(testProvider{}, store)
	got, err := service.MachineOwnership(context.Background(), ownership.ResourceID)
	if err != nil || got.ResourceID != ownership.ResourceID || got.AccountID != ownership.AccountID ||
		got.MachineID != ownership.MachineID || got.InstanceID != ownership.InstanceID || got.NodeName != ownership.NodeName ||
		got.Status != "released" || got.ReleasedAt == nil || !got.ReleasedAt.Equal(releasedAt) {
		t.Fatalf("ownership = %#v err=%v", got, err)
	}
	if _, err := service.MachineOwnership(context.Background(), "compute-missing"); err != ErrMachineOwnershipNotFound {
		t.Fatalf("missing ownership error = %v", err)
	}
}
