package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/lib/pq"
)

func TestResourceAutoRenewIntentInterleavings(t *testing.T) {
	t.Run("memory", func(t *testing.T) {
		testResourceAutoRenewIntentInterleavings(t, newMemoryTableStore())
	})
	t.Run("postgres", func(t *testing.T) {
		testResourceAutoRenewIntentInterleavings(t, newPostgresResourceIntentStore(t, "control_plane_resource_intent"))
	})
}

func TestCanonicalMonthlyPriceSnapshotRoundTrips(t *testing.T) {
	t.Run("memory", func(t *testing.T) { testCanonicalMonthlyPriceSnapshotRoundTrip(t, "memory", newMemoryTableStore()) })
	t.Run("postgres", func(t *testing.T) {
		testCanonicalMonthlyPriceSnapshotRoundTrip(t, "postgres", newPostgresResourceIntentStore(t, "control_plane_price_snapshot"))
	})
}

func testCanonicalMonthlyPriceSnapshotRoundTrip(t *testing.T, name string, store controlPlaneTableStore) {
	t.Helper()
	for _, resourceType := range []string{"compute", "storage"} {
		t.Run(resourceType, func(t *testing.T) {
			ctx := context.Background()
			row := canonicalBillingOperation(resourceType, resourceType+"-canonical-"+name, "acct-canonical-"+name)
			claimed, fresh, err := store.ClaimResourceBillingOperation(ctx, resourceType, row)
			if err != nil || !fresh {
				t.Fatalf("canonical claim fresh=%v row=%#v err=%v", fresh, claimed, err)
			}
			claimed["billingStatus"] = "active"
			saveIntentResource(t, ctx, store, resourceType, claimed)
			persisted := loadIntentResource(t, ctx, store, resourceType, stringValue(row["id"]), stringValue(row["accountId"]))
			if !monthlyPriceSnapshotAvailable(persisted) || persisted["priceVersion"] != pilotPriceVersion || persisted["currency"] != "USD" {
				t.Fatalf("canonical persisted %s = %#v", resourceType, persisted)
			}
			if _, ok := persisted["pricingVersion"]; ok {
				t.Fatalf("canonical-only %s gained legacy alias: %#v", resourceType, persisted)
			}
			if replayed, fresh, err := store.ClaimResourceBillingOperation(ctx, resourceType, row); err != nil || fresh || !monthlyPriceSnapshotAvailable(replayed) {
				t.Fatalf("canonical replay fresh=%v row=%#v err=%v", fresh, replayed, err)
			}

			malformed := canonicalBillingOperation(resourceType, resourceType+"-malformed-"+name, stringValue(row["accountId"]))
			malformed["pricingVersion"] = pilotPriceVersion
			if resourceType == "storage" {
				malformed["monthlyPriceCnyCents"] = int64(1_800)
			} else {
				malformed["monthlyPriceCnyCents"] = int64(35_000)
			}
			malformedSnapshot := mapField(malformed, "priceSnapshot")
			malformedSnapshot["currency"] = "CNY"
			malformed["priceSnapshot"] = malformedSnapshot
			if _, fresh, err := store.ClaimResourceBillingOperation(ctx, resourceType, malformed); !errors.Is(err, errMonthlyPriceSnapshotUnavailable) || fresh {
				t.Fatalf("malformed canonical claim fresh=%v err=%v", fresh, err)
			}
			var rows []map[string]any
			if resourceType == "storage" {
				rows, err = store.ListStorages(ctx, stringValue(row["accountId"]))
			} else {
				rows, err = store.ListComputes(ctx, stringValue(row["accountId"]))
			}
			if err != nil || findRecord(rows, stringValue(malformed["id"])) != nil {
				t.Fatalf("malformed canonical claim persisted rows=%#v err=%v", rows, err)
			}
		})
	}
}

func TestMonthlyPriceSnapshotCanonicalAuthorityAndLegacyNormalization(t *testing.T) {
	canonical := canonicalBillingOperation("compute", "compute-price-authority", "acct-price-authority")
	if !monthlyPriceSnapshotAvailable(canonical) {
		t.Fatalf("canonical-only snapshot rejected: %#v", canonical)
	}
	requested := cloneMap(canonical)
	requested["pricingVersion"], requested["monthlyPriceCnyCents"] = "ignored-alias", int64(1)
	if !billingOperationIdentityMatches(canonical, requested) {
		t.Fatal("canonical identity depended on legacy aliases")
	}

	malformed := cloneMap(canonical)
	malformed["pricingVersion"], malformed["monthlyPriceCnyCents"] = pilotPriceVersion, int64(35_000)
	malformedSnapshot := mapField(malformed, "priceSnapshot")
	malformedSnapshot["currency"] = "CNY"
	malformed["priceSnapshot"] = malformedSnapshot
	if monthlyPriceSnapshotAvailable(malformed) || billingOperationIdentityMatches(malformed, canonical) {
		t.Fatalf("malformed canonical snapshot fell back to aliases: %#v", malformed)
	}

	legacy := cloneMap(canonical)
	delete(legacy, "priceVersion")
	delete(legacy, "currency")
	delete(legacy, "priceSnapshot")
	legacy["pricingVersion"], legacy["monthlyPriceCnyCents"] = pilotPriceVersion, int64(35_000)
	if !monthlyPriceSnapshotAvailable(legacy) || legacy["priceVersion"] != pilotPriceVersion || legacy["currency"] != "USD" || len(mapField(legacy, "priceSnapshot")) == 0 {
		t.Fatalf("legacy snapshot was not normalized: %#v", legacy)
	}
	if !billingOperationIdentityMatches(legacy, canonical) {
		t.Fatal("normalized legacy snapshot did not match canonical identity")
	}

	incompleteLegacy := canonicalBillingOperation("storage", "storage-incomplete-legacy", "acct-price-authority")
	for _, key := range []string{"priceVersion", "currency", "priceSnapshot", "sizeGb"} {
		delete(incompleteLegacy, key)
	}
	incompleteLegacy["pricingVersion"], incompleteLegacy["monthlyPriceCnyCents"] = pilotPriceVersion, int64(1_800)
	if monthlyPriceSnapshotAvailable(incompleteLegacy) {
		t.Fatalf("incomplete legacy storage snapshot accepted: %#v", incompleteLegacy)
	}
	for _, key := range []string{"priceVersion", "currency", "priceSnapshot"} {
		if _, exists := incompleteLegacy[key]; exists {
			t.Fatalf("failed legacy normalization wrote %s: %#v", key, incompleteLegacy)
		}
	}
}

func newPostgresResourceIntentStore(t *testing.T, prefix string) controlPlaneTableStore {
	t.Helper()
	databaseURL := os.Getenv("CONTROL_PLANE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("CONTROL_PLANE_TEST_DATABASE_URL is not set")
	}
	admin, err := sql.Open("postgres", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := admin.Ping(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	schema := fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	if _, err := admin.Exec(`CREATE SCHEMA ` + pq.QuoteIdentifier(schema)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(`DROP SCHEMA ` + pq.QuoteIdentifier(schema) + ` CASCADE`) })
	stateStore, err := NewPostgresEntStateStore(postgresInvitedAccountTestURL(databaseURL, schema))
	if err != nil {
		t.Fatal(err)
	}
	store := stateStore.(*postgresEntStateStore)
	t.Cleanup(func() { _ = store.client.Close() })
	return store
}

func canonicalBillingOperation(resourceType, id, accountID string) map[string]any {
	charge := int64(50_000_000)
	snapshot := map[string]any{
		"resourceType": resourceType, "priceVersion": pilotPriceVersion, "packageId": "basic",
		"currency": "USD", "billingUnit": "calendar_month", "chargeUsdMicros": charge,
	}
	row := map[string]any{
		"id": id, "resourceType": resourceType, "accountId": accountID, "packageId": "basic", "billingStatus": "preparing",
		"billingOperationId": "billing-" + id, "priceVersion": pilotPriceVersion, "currency": "USD", "chargeUsdMicros": charge,
		"priceSnapshot": snapshot, "periodStart": "2026-07-01T00:00:00Z", "paidThrough": "2026-08-01T00:00:00Z",
	}
	if resourceType == "storage" {
		charge = 2_580_000
		row["chargeUsdMicros"], row["sizeGb"], row["computeAllocationId"], row["zone"] = charge, 10, "compute-placement", "ap-shanghai-2"
		snapshot["chargeUsdMicros"], snapshot["sizeGb"] = charge, 10
	}
	return row
}

func TestMemoryResourceAutoRenewRequiresExplicitSetter(t *testing.T) {
	ctx := context.Background()
	store := newMemoryTableStore()
	for _, resourceType := range []string{"compute", "storage"} {
		accountID := "acct-explicit-intent-" + resourceType
		row := intentResource(resourceType, "resource-explicit-intent-"+resourceType, accountID)
		saveIntentResource(t, ctx, store, resourceType, row)
		stale := cloneMap(row)
		stale["autoRenew"] = false
		saveIntentResource(t, ctx, store, resourceType, stale)
		if current := loadIntentResource(t, ctx, store, resourceType, stringValue(row["id"]), accountID); current["autoRenew"] != true {
			t.Fatalf("ordinary %s save changed intent: %#v", resourceType, current)
		}
		if err := store.SetResourceAutoRenew(ctx, resourceType, stringValue(row["id"]), accountID, false); err != nil {
			t.Fatal(err)
		}
		if current := loadIntentResource(t, ctx, store, resourceType, stringValue(row["id"]), accountID); current["autoRenew"] != false {
			t.Fatalf("explicit %s setter did not change intent: %#v", resourceType, current)
		}
	}
}

func testResourceAutoRenewIntentInterleavings(t *testing.T, store controlPlaneTableStore) {
	t.Helper()
	for index, resourceType := range []string{"compute", "storage"} {
		t.Run(resourceType+"/claim_then_disable", func(t *testing.T) {
			ctx := context.Background()
			accountID := "acct-intent-a-" + resourceType
			user := seedIntentOwner(t, ctx, store, accountID, "usr-intent-a-"+resourceType, int64(201+index*2))
			resource := intentResource(resourceType, "resource-intent-a-"+resourceType, accountID)
			saveIntentResource(t, ctx, store, resourceType, resource)

			claim := cloneMap(resource)
			claim["billingStatus"], claim["billingOperationId"] = "renewal_pending", "renewal-intent-a-"+resourceType
			claimed, fresh, err := store.ClaimResourceBillingOperation(ctx, resourceType, claim)
			if err != nil || !fresh {
				t.Fatalf("initial renewal claim fresh=%v row=%#v err=%v", fresh, claimed, err)
			}
			user["status"] = "disabled"
			if err := store.ApplyUserLifecycle(ctx, user); err != nil {
				t.Fatal(err)
			}

			staleProgress := cloneMap(claimed)
			staleProgress["billingStatus"], staleProgress["paidThrough"], staleProgress["autoRenew"] = "active", "2026-09-01T00:00:00Z", true
			saveIntentResource(t, ctx, store, resourceType, staleProgress)
			current := loadIntentResource(t, ctx, store, resourceType, stringValue(resource["id"]), accountID)
			if current["autoRenew"] != false || current["billingStatus"] != "active" || current["paidThrough"] != "2026-09-01T00:00:00Z" {
				t.Fatalf("stale progress restored intent or lost progress: %#v", current)
			}

			next := cloneMap(staleProgress)
			next["billingStatus"], next["billingOperationId"] = "renewal_pending", "renewal-intent-next-"+resourceType
			replayed, fresh, err := store.ClaimResourceBillingOperation(ctx, resourceType, next)
			if err != nil || fresh || replayed["billingOperationId"] != claimed["billingOperationId"] || replayed["paidThrough"] != current["paidThrough"] {
				t.Fatalf("disabled next renewal fresh=%v row=%#v err=%v", fresh, replayed, err)
			}
		})

		t.Run(resourceType+"/disable_then_claim", func(t *testing.T) {
			ctx := context.Background()
			accountID := "acct-intent-b-" + resourceType
			user := seedIntentOwner(t, ctx, store, accountID, "usr-intent-b-"+resourceType, int64(202+index*2))
			resource := intentResource(resourceType, "resource-intent-b-"+resourceType, accountID)
			saveIntentResource(t, ctx, store, resourceType, resource)
			staleClaim := cloneMap(resource)
			staleClaim["billingStatus"], staleClaim["billingOperationId"] = "renewal_pending", "renewal-intent-b-"+resourceType

			user["status"] = "disabled"
			if err := store.ApplyUserLifecycle(ctx, user); err != nil {
				t.Fatal(err)
			}
			claimed, fresh, err := store.ClaimResourceBillingOperation(ctx, resourceType, staleClaim)
			if err != nil || fresh || claimed["billingOperationId"] != resource["billingOperationId"] || claimed["paidThrough"] != resource["paidThrough"] || claimed["autoRenew"] != false {
				t.Fatalf("claim after disable fresh=%v row=%#v err=%v", fresh, claimed, err)
			}
		})
	}
}

func seedIntentOwner(t *testing.T, ctx context.Context, store controlPlaneTableStore, accountID, userID string, sub2APIUserID int64) map[string]any {
	t.Helper()
	mustStore(t, store.SaveAccount(ctx, map[string]any{"id": accountID, "status": "active", "sub2apiUserId": sub2APIUserID}))
	user := map[string]any{"id": userID, "email": userID + "@example.com", "accountId": accountID, "role": "owner", "status": "active"}
	mustStore(t, store.SaveUser(ctx, user))
	return user
}

func intentResource(resourceType, id, accountID string) map[string]any {
	row := map[string]any{
		"id": id, "resourceType": resourceType, "accountId": accountID, "autoRenew": true,
		"billingStatus": "active", "billingOperationId": "purchase-" + id,
		"pricingVersion": "2026-07-14", "packageId": "basic", "periodStart": "2026-07-01T00:00:00Z", "paidThrough": "2026-08-01T00:00:00Z",
		"monthlyPriceCnyCents": int64(35000), "chargeUsdMicros": int64(50_000_000),
	}
	if resourceType == "storage" {
		row["sizeGb"], row["computeAllocationId"], row["zone"] = 10, "compute-intent", "ap-shanghai-2"
		row["monthlyPriceCnyCents"], row["chargeUsdMicros"] = int64(1_800), int64(2_580_000)
	}
	return row
}

func saveIntentResource(t *testing.T, ctx context.Context, store controlPlaneTableStore, resourceType string, row map[string]any) {
	t.Helper()
	var err error
	if resourceType == "storage" {
		err = store.SaveStorage(ctx, row)
	} else {
		err = store.SaveCompute(ctx, row)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func loadIntentResource(t *testing.T, ctx context.Context, store controlPlaneTableStore, resourceType, id, accountID string) map[string]any {
	t.Helper()
	var rows []map[string]any
	var err error
	if resourceType == "storage" {
		rows, err = store.ListStorages(ctx, accountID)
	} else {
		rows, err = store.ListComputes(ctx, accountID)
	}
	if err != nil {
		t.Fatal(err)
	}
	row := findRecord(rows, id)
	if row == nil {
		t.Fatalf("%s %s not found in %#v", resourceType, id, rows)
	}
	return row
}
