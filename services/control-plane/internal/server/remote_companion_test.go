package server

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const remoteTestHashKey = "01234567890123456789012345678901"

func newRemoteBrokerTest(t *testing.T) (*remoteCompanionBroker, *fakeRemoteCompanionProvider) {
	t.Helper()
	t.Setenv("OPL_LINK_TOKEN_HASH_KEY", remoteTestHashKey)
	store := NewTestEntStateStore(t, t.TempDir()+"/remote-companion.sqlite").(*postgresEntStateStore)
	provider := newFakeRemoteCompanionProvider()
	broker, err := newRemoteCompanionBroker(store, provider)
	if err != nil {
		t.Fatal(err)
	}
	if broker == nil {
		t.Fatal("remote broker is unexpectedly unavailable")
	}
	return broker, provider
}

func createRemoteInviteAndPairing(t *testing.T, broker *remoteCompanionBroker) remotePairingCreateResponse {
	t.Helper()
	invite, err := broker.createInvite(context.Background(), "test-user")
	if err != nil {
		t.Fatal(err)
	}
	created, err := broker.createPairing(context.Background(), invite.Secret, "desktop-key")
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func TestRemoteCompanionInvitationIsSingleUseAndSeatAtomic(t *testing.T) {
	broker, _ := newRemoteBrokerTest(t)
	invite, err := broker.createInvite(context.Background(), "test-user")
	if err != nil {
		t.Fatal(err)
	}

	const attempts = 12
	results := make(chan error, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := broker.createPairing(context.Background(), invite.Secret, "desktop-key")
			results <- err
		}()
	}
	wg.Wait()
	close(results)

	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("same invitation created %d pairings, want 1", successes)
	}
	capacity, err := broker.capacity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if capacity.SeatCount != 1 {
		t.Fatalf("seat count = %d, want 1", capacity.SeatCount)
	}
}

func TestRemoteCompanionSeatLimitIsAtomic(t *testing.T) {
	broker, _ := newRemoteBrokerTest(t)
	const attempts = 100
	invites := make([]string, attempts)
	for i := range invites {
		invite, err := broker.createInvite(context.Background(), "test-user")
		if err != nil {
			t.Fatal(err)
		}
		invites[i] = invite.Secret
	}

	results := make(chan error, attempts)
	var wg sync.WaitGroup
	workers := make(chan struct{}, 12)
	for _, inviteSecret := range invites {
		wg.Add(1)
		go func(inviteSecret string) {
			defer wg.Done()
			workers <- struct{}{}
			_, err := broker.createPairing(context.Background(), inviteSecret, "desktop-key")
			<-workers
			results <- err
		}(inviteSecret)
	}
	wg.Wait()
	close(results)

	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != remoteSeatLimit {
		t.Fatalf("successful pairings = %d, want %d", successes, remoteSeatLimit)
	}
	capacity, err := broker.capacity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if capacity.SeatCount != remoteSeatLimit {
		t.Fatalf("seat count = %d, want %d", capacity.SeatCount, remoteSeatLimit)
	}
	if capacity.SeatCount > capacity.SeatLimit {
		t.Fatalf("seat count = %d exceeds limit %d", capacity.SeatCount, capacity.SeatLimit)
	}
}

func TestRemoteCompanionManualCodeAttemptsReleaseSeat(t *testing.T) {
	broker, _ := newRemoteBrokerTest(t)
	created := createRemoteInviteAndPairing(t, broker)
	for attempt := 1; attempt <= remoteManualAttempts; attempt++ {
		_, err := broker.claimPairing(context.Background(), created.PairingID, "", "WRONG-CODE", "ios-key")
		if err == nil {
			t.Fatalf("attempt %d unexpectedly succeeded", attempt)
		}
		if attempt < remoteManualAttempts && !strings.Contains(err.Error(), errRemoteClaimMismatch.Error()) {
			t.Fatalf("attempt %d error = %v, want claim mismatch", attempt, err)
		}
		if attempt == remoteManualAttempts && !strings.Contains(err.Error(), errRemoteClaimAttempts.Error()) {
			t.Fatalf("attempt %d error = %v, want claim attempt limit", attempt, err)
		}
	}
	pairing, err := broker.store.client.RemotePairing.Get(context.Background(), created.PairingID)
	if err != nil {
		t.Fatal(err)
	}
	if pairing.State != "revoked" || !pairing.SeatReleased {
		t.Fatalf("pairing state = %q, seat_released = %v, want revoked/true", pairing.State, pairing.SeatReleased)
	}
	capacity, err := broker.capacity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if capacity.SeatCount != 0 {
		t.Fatalf("seat count = %d, want 0", capacity.SeatCount)
	}
}

func TestRemoteCompanionConfirmRequiresAuthenticationString(t *testing.T) {
	broker, _ := newRemoteBrokerTest(t)
	created := createRemoteInviteAndPairing(t, broker)
	claimed, err := broker.claimPairing(context.Background(), created.PairingID, created.ClaimSecret, "", "ios-key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := broker.confirmPairing(context.Background(), created.PairingID, created.DesktopCredential, "wrong"); !strings.Contains(err.Error(), errRemoteAuthentication.Error()) {
		t.Fatalf("wrong authentication string error = %v", err)
	}
	confirmed, err := broker.confirmPairing(context.Background(), created.PairingID, created.DesktopCredential, claimed.AuthenticationString)
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.State != "active" {
		t.Fatalf("confirmed state = %q, want active", confirmed.State)
	}
}

func TestRemoteCompanionRevocationReceiptSupportsPartialRetry(t *testing.T) {
	broker, provider := newRemoteBrokerTest(t)
	created := createRemoteInviteAndPairing(t, broker)
	claimed, err := broker.claimPairing(context.Background(), created.PairingID, created.ClaimSecret, "", "ios-key")
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := broker.confirmPairing(context.Background(), created.PairingID, created.DesktopCredential, claimed.AuthenticationString)
	if err != nil {
		t.Fatal(err)
	}
	provider.mu.Lock()
	provider.failDeleteFor[confirmed.IOSProviderUserID] = true
	provider.mu.Unlock()

	partial, err := broker.revokePairing(context.Background(), created.PairingID, created.DesktopCredential)
	if err != nil {
		t.Fatal(err)
	}
	if partial.State != "provider_reclaim_pending" || partial.SeatReleased || !partial.DesktopProviderAbsent || partial.IOSProviderAbsent {
		t.Fatalf("partial receipt = %#v", partial)
	}
	if partial.IOS.Error == "" {
		t.Fatal("partial receipt omitted provider error")
	}

	provider.mu.Lock()
	delete(provider.failDeleteFor, confirmed.IOSProviderUserID)
	provider.mu.Unlock()
	final, err := broker.revokePairing(context.Background(), created.PairingID, created.DesktopCredential)
	if err != nil {
		t.Fatal(err)
	}
	if final.State != "revoked" || !final.SeatReleased || !final.DesktopProviderAbsent || !final.IOSProviderAbsent || final.RevokedAt == "" {
		t.Fatalf("final receipt = %#v", final)
	}
	if _, _, err := broker.authenticate(context.Background(), created.PairingID, created.DesktopCredential); !strings.Contains(err.Error(), errRemoteRevoked.Error()) {
		t.Fatalf("revoked credential authentication error = %v", err)
	}
	capacity, err := broker.capacity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if capacity.SeatCount != 0 {
		t.Fatalf("seat count = %d, want 0", capacity.SeatCount)
	}
}

func TestRemoteCompanionTokenHashDoesNotPersistPlaintextAndSurvivesRestart(t *testing.T) {
	broker, _ := newRemoteBrokerTest(t)
	invite, err := broker.createInvite(context.Background(), "test-user")
	if err != nil {
		t.Fatal(err)
	}
	secondInvite, err := broker.createInvite(context.Background(), "test-user")
	if err != nil {
		t.Fatal(err)
	}
	pairing, err := broker.createPairing(context.Background(), invite.Secret, "desktop-key")
	if err != nil {
		t.Fatal(err)
	}
	row, err := broker.store.client.RemoteInvitation.Get(context.Background(), invite.InvitationID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(row.SecretHash, invite.Secret) || strings.Contains(row.SecretSalt, invite.Secret) {
		t.Fatal("invitation secret was persisted in plaintext")
	}
	pairRow, err := broker.store.client.RemotePairing.Get(context.Background(), pairing.PairingID)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{pairRow.ClaimSecretHash, pairRow.ClaimSecretSalt, pairRow.ManualCodeHash, pairRow.ManualCodeSalt} {
		if strings.Contains(value, pairing.ClaimSecret) || strings.Contains(value, pairing.ManualCode) {
			t.Fatal("pairing secret was persisted in plaintext")
		}
	}
	restarted, err := newRemoteCompanionBroker(broker.store, newFakeRemoteCompanionProvider())
	if err != nil {
		t.Fatal(err)
	}
	if restarted == nil {
		t.Fatal("broker did not restart with stable hash key")
	}
	if _, err := restarted.createPairing(context.Background(), secondInvite.Secret, "desktop-key-2"); err != nil {
		t.Fatalf("stable hash key did not preserve invite validation after restart: %v", err)
	}
	if _, err := restarted.createPairing(context.Background(), invite.Secret, "desktop-key-3"); err == nil {
		t.Fatal("consumed invitation was accepted after restart")
	}
}

func TestRemoteCompanionBrokerFailsClosedWithoutStableHashKey(t *testing.T) {
	t.Setenv("OPL_LINK_TOKEN_HASH_KEY", "")
	store := NewTestEntStateStore(t, t.TempDir()+"/remote-companion-no-key.sqlite")
	broker, err := newRemoteCompanionBroker(store, newFakeRemoteCompanionProvider())
	if err != nil {
		t.Fatal(err)
	}
	if broker != nil {
		t.Fatal("broker started without a stable hash key")
	}
}

func TestTencentUserSigUsesCompressedOfficialFormat(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	sig, err := tencentUserSig("test-secret", 1400000000, "opl-link-user", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	encoded := strings.NewReplacer("*", "+", "-", "/", "_", "=").Replace(sig)
	compressed, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := zlib.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(plain, &document); err != nil {
		t.Fatal(err)
	}
	if document["TLS.ver"] != "2.0" || document["TLS.identifier"] != "opl-link-user" {
		t.Fatalf("unexpected UserSig document: %#v", document)
	}
	expectedMessage := "TLS.identifier:opl-link-user\nTLS.sdkappid:1400000000\nTLS.time:1700000000\nTLS.expire:3600\n"
	mac := hmac.New(sha256.New, []byte("test-secret"))
	_, _ = mac.Write([]byte(expectedMessage))
	if document["TLS.sig"] != base64.StdEncoding.EncodeToString(mac.Sum(nil)) {
		t.Fatal("UserSig HMAC does not match official payload")
	}
}

func TestTencentUserAbsentRecognizesImportedStates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/v4/im_open_login_svc/account_check" {
			http.NotFound(w, r)
			return
		}
		status := "Imported"
		if r.URL.Query().Get("identifier") == "not-imported" {
			status = "NotImported"
		}
		_, _ = io.WriteString(w, `{"ActionStatus":"OK","ErrorCode":0,"CheckItem":[{"AccountStatus":"`+status+`"}]}`)
	}))
	defer server.Close()
	provider := &tencentRemoteCompanionProvider{
		config: tencentRemoteCompanionConfig{SDKAppID: 1400000000, AdminIdentifier: "admin", Secret: "test-secret", BaseURL: server.URL, Configured: true},
		client: server.Client(),
	}
	absent, err := provider.UserAbsent(context.Background(), "user")
	if err != nil || absent {
		t.Fatalf("Imported account absent = %v, err = %v", absent, err)
	}
	provider.config.AdminIdentifier = "not-imported"
	absent, err = provider.UserAbsent(context.Background(), "user")
	if err != nil || !absent {
		t.Fatalf("NotImported account absent = %v, err = %v", absent, err)
	}
}

func TestRemoteCompanionSASUsesCanonicalAppVector(t *testing.T) {
	got := remoteSAS(
		"pair-test-001",
		"3p7bfXt9wbTTW2HC7OQ1Nz-DQ8hbeGdNrfx-FG-IK08",
		"hSDwCYkwp1R0i33ctD73Wg2_Og0mOBr066SpjqqbTmo",
	)
	if got != "867 604" {
		t.Fatalf("SAS = %q, want 867 604", got)
	}
}

func TestRemoteCompanionRevocationReceiptReadback(t *testing.T) {
	broker, _ := newRemoteBrokerTest(t)
	created := createRemoteInviteAndPairing(t, broker)
	claimed, err := broker.claimPairing(context.Background(), created.PairingID, created.ClaimSecret, "", "ios-key")
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := broker.confirmPairing(context.Background(), created.PairingID, created.DesktopCredential, claimed.AuthenticationString)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := broker.revokePairing(context.Background(), created.PairingID, created.DesktopCredential)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.RevocationReceiptID == "" || receipt.RevocationReceiptToken == "" {
		t.Fatalf("receipt identity missing: %#v", receipt)
	}
	readback, err := broker.readRevocation(context.Background(), receipt.RevocationReceiptID, receipt.RevocationReceiptToken)
	if err != nil {
		t.Fatal(err)
	}
	if readback.State != "revoked" || !readback.SeatReleased || !readback.DesktopProviderIdentityAbsent || !readback.IOSProviderIdentityAbsent {
		t.Fatalf("revocation readback = %#v", readback)
	}
	if _, err := broker.revokePairing(context.Background(), confirmed.PairingID, created.DesktopCredential); err != nil {
		t.Fatalf("terminal revoke retry failed: %v", err)
	}
}

func TestRemoteCompanionPairingIdempotencyReplaysWithoutAnotherSeat(t *testing.T) {
	broker, _ := newRemoteBrokerTest(t)
	invite, err := broker.createInvite(context.Background(), "test-user")
	if err != nil {
		t.Fatal(err)
	}
	first, err := broker.createPairingWithOptions(context.Background(), invite.Secret, "desktop-id", "Desktop", "desktop-key", "pairing-idempotency-1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := broker.createPairingWithOptions(context.Background(), invite.Secret, "desktop-id", "Desktop", "desktop-key", "pairing-idempotency-1")
	if err != nil {
		t.Fatal(err)
	}
	if first.PairingID != second.PairingID || first.ClaimSecret != second.ClaimSecret || first.DesktopCredential != second.DesktopCredential {
		t.Fatalf("idempotent replay changed response: first=%#v second=%#v", first, second)
	}
	capacity, err := broker.capacity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if capacity.SeatCount != 1 {
		t.Fatalf("seat count = %d, want 1", capacity.SeatCount)
	}
}

func TestRemoteCompanionWireUsesCanonicalPathAndSnakeCase(t *testing.T) {
	broker, _ := newRemoteBrokerTest(t)
	invite, err := broker.createInvite(context.Background(), "test-user")
	if err != nil {
		t.Fatal(err)
	}
	app := &controlPlaneServer{remoteCompanion: broker}
	mux := http.NewServeMux()
	registerRemoteCompanionRoutes(mux, app)
	body := `{"protocol_version":"` + remoteProtocolVersion + `","invitation_code":"` + invite.Secret + `","desktop_device_id":"desktop-test","desktop_device_label":"Test Desktop","desktop_public_key":"desktop-key"}`
	request := httptest.NewRequest(http.MethodPost, remoteCompanionBasePath+"/pairings", strings.NewReader(body))
	request.Header.Set("Idempotency-Key", "wire-pairing-1")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("pairing status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	assertExactJSONKeys(t, recorder.Body.Bytes(), "protocol_version", "pairing_id", "desktop_pair_token", "claim_secret", "manual_code", "expires_at", "broker_url")
	if _, ok := response["pairing_id"]; !ok {
		t.Fatalf("wire response lacks pairing_id: %#v", response)
	}
	if _, ok := response["pairingId"]; ok {
		t.Fatalf("wire response contains camelCase field: %#v", response)
	}
	if _, ok := response["protocol_version"]; !ok {
		t.Fatalf("wire response lacks protocol_version: %#v", response)
	}
	legacy := httptest.NewRequest(http.MethodPost, "/api/remote-companion/v1/pairings", strings.NewReader(body))
	legacy.Header.Set("Idempotency-Key", "wire-pairing-legacy")
	legacyRecorder := httptest.NewRecorder()
	mux.ServeHTTP(legacyRecorder, legacy)
	if legacyRecorder.Code != http.StatusNotFound {
		t.Fatalf("legacy route status = %d, want 404", legacyRecorder.Code)
	}
	wrongVersion := strings.Replace(body, remoteProtocolVersion, "opl_remote_transport.v0", 1)
	wrongVersionRequest := httptest.NewRequest(http.MethodPost, remoteCompanionBasePath+"/pairings", strings.NewReader(wrongVersion))
	wrongVersionRequest.Header.Set("Idempotency-Key", "wire-pairing-wrong-version")
	wrongVersionRecorder := httptest.NewRecorder()
	mux.ServeHTTP(wrongVersionRecorder, wrongVersionRequest)
	if wrongVersionRecorder.Code != http.StatusBadRequest || !strings.Contains(wrongVersionRecorder.Body.String(), `"error_code":"unsupported_protocol"`) {
		t.Fatalf("wrong protocol response = %d %s", wrongVersionRecorder.Code, wrongVersionRecorder.Body.String())
	}
}

func TestRemoteCompanionCanonicalHTTPLifecycle(t *testing.T) {
	broker, _ := newRemoteBrokerTest(t)
	invite, err := broker.createInvite(context.Background(), "test-user")
	if err != nil {
		t.Fatal(err)
	}
	app := &controlPlaneServer{remoteCompanion: broker}
	mux := http.NewServeMux()
	registerRemoteCompanionRoutes(mux, app)

	createBody := `{"protocol_version":"` + remoteProtocolVersion + `","invitation_code":"` + invite.Secret + `","desktop_device_id":"desktop-device","desktop_device_label":"Desktop","desktop_public_key":"desktop-key"}`
	createRequest := httptest.NewRequest(http.MethodPost, remoteCompanionBasePath+"/pairings", strings.NewReader(createBody))
	createRequest.Header.Set("Idempotency-Key", "canonical-http-create")
	createRecorder := httptest.NewRecorder()
	mux.ServeHTTP(createRecorder, createRequest)
	if createRecorder.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", createRecorder.Code, createRecorder.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(createRecorder.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	assertExactJSONKeys(t, createRecorder.Body.Bytes(), "protocol_version", "pairing_id", "desktop_pair_token", "claim_secret", "manual_code", "expires_at", "broker_url")
	pairingID, _ := created["pairing_id"].(string)
	claimSecret, _ := created["claim_secret"].(string)
	desktopToken, _ := created["desktop_pair_token"].(string)
	if pairingID == "" || claimSecret == "" || desktopToken == "" {
		t.Fatalf("create response omitted canonical secrets: %#v", created)
	}

	claimBody := `{"protocol_version":"` + remoteProtocolVersion + `","pairing_id":"` + pairingID + `","claim_secret_or_manual_code":"` + claimSecret + `","ios_device_id":"ios-device","ios_device_label":"iPhone","ios_public_key":"ios-key"}`
	claimRequest := httptest.NewRequest(http.MethodPost, remoteCompanionBasePath+"/pairings/claim", strings.NewReader(claimBody))
	claimRequest.Header.Set("Idempotency-Key", "canonical-http-claim")
	claimRecorder := httptest.NewRecorder()
	mux.ServeHTTP(claimRecorder, claimRequest)
	if claimRecorder.Code != http.StatusOK {
		t.Fatalf("claim status = %d, body = %s", claimRecorder.Code, claimRecorder.Body.String())
	}
	var claimed map[string]any
	if err := json.Unmarshal(claimRecorder.Body.Bytes(), &claimed); err != nil {
		t.Fatal(err)
	}
	assertExactJSONKeys(t, claimRecorder.Body.Bytes(), "protocol_version", "pairing_id", "ios_claim_token", "state", "authentication_string", "expires_at")
	authenticationString, _ := claimed["authentication_string"].(string)
	iosToken, _ := claimed["ios_claim_token"].(string)
	if authenticationString == "" || iosToken == "" {
		t.Fatalf("claim response omitted canonical fields: %#v", claimed)
	}

	confirmBody := `{"protocol_version":"` + remoteProtocolVersion + `","authentication_string":"` + authenticationString + `"}`
	confirmRequest := httptest.NewRequest(http.MethodPost, remoteCompanionBasePath+"/pairings/"+pairingID+"/confirm", strings.NewReader(confirmBody))
	confirmRequest.Header.Set("Authorization", "Bearer "+desktopToken)
	confirmRequest.Header.Set("Idempotency-Key", "canonical-http-confirm")
	confirmRecorder := httptest.NewRecorder()
	mux.ServeHTTP(confirmRecorder, confirmRequest)
	if confirmRecorder.Code != http.StatusOK {
		t.Fatalf("confirm status = %d, body = %s", confirmRecorder.Code, confirmRecorder.Body.String())
	}
	assertExactJSONKeys(t, confirmRecorder.Body.Bytes(), "protocol_version", "pairing_id", "state")

	desktopReadRequest := httptest.NewRequest(http.MethodGet, remoteCompanionBasePath+"/pairings/"+pairingID, nil)
	desktopReadRequest.Header.Set("Authorization", "Bearer "+desktopToken)
	desktopReadRecorder := httptest.NewRecorder()
	mux.ServeHTTP(desktopReadRecorder, desktopReadRequest)
	if desktopReadRecorder.Code != http.StatusOK {
		t.Fatalf("desktop read status = %d, body = %s", desktopReadRecorder.Code, desktopReadRecorder.Body.String())
	}
	desktopRead := assertExactJSONKeys(t, desktopReadRecorder.Body.Bytes(), "protocol_version", "pairing_id", "state", "authentication_string", "expires_at", "device_activation")
	desktopActivation, ok := desktopRead["device_activation"].(map[string]any)
	if !ok {
		t.Fatalf("desktop device_activation = %#v", desktopRead["device_activation"])
	}
	assertExactJSONKeys(t, mustMarshalJSON(t, desktopActivation), "device_id", "device_label", "peer_device_id", "peer_device_label", "provider_user_id", "peer_provider_user_id", "peer_public_key", "sdk_app_id", "usersig", "usersig_expires_at")
	if desktopActivation["device_id"] != "desktop-device" || desktopActivation["device_label"] != "Desktop" || desktopActivation["peer_device_id"] != "ios-device" || desktopActivation["peer_device_label"] != "iPhone" || desktopActivation["peer_public_key"] != "ios-key" {
		t.Fatalf("desktop activation leaked or lost caller-scoped values: %#v", desktopActivation)
	}
	if desktopActivation["provider_user_id"] != tencentPairUserID(pairingID, "desktop") || desktopActivation["peer_provider_user_id"] != tencentPairUserID(pairingID, "ios") || desktopActivation["usersig"] == "" {
		t.Fatalf("desktop activation provider values = %#v", desktopActivation)
	}
	for _, forbidden := range []string{"desktop_provider_user_id", "ios_provider_user_id", "device_credential", "desktop_credential", "ios_credential", "seat", "desktop_public_key", "ios_public_key"} {
		if _, present := desktopRead[forbidden]; present {
			t.Fatalf("desktop read exposed forbidden field %q: %#v", forbidden, desktopRead)
		}
	}
	if strings.Contains(desktopReadRecorder.Body.String(), desktopToken) || strings.Contains(desktopReadRecorder.Body.String(), iosToken) {
		t.Fatalf("desktop read exposed a bearer credential: %s", desktopReadRecorder.Body.String())
	}

	desktopReadAgain := httptest.NewRecorder()
	mux.ServeHTTP(desktopReadAgain, desktopReadRequest)
	if desktopReadAgain.Code != http.StatusOK {
		t.Fatalf("desktop second read status = %d, body = %s", desktopReadAgain.Code, desktopReadAgain.Body.String())
	}
	var desktopReadAgainBody map[string]any
	if err := json.Unmarshal(desktopReadAgain.Body.Bytes(), &desktopReadAgainBody); err != nil {
		t.Fatal(err)
	}
	if activation, ok := desktopReadAgainBody["device_activation"].(map[string]any); !ok {
		t.Fatalf("desktop repeat read omitted device_activation: %#v", desktopReadAgainBody)
	} else {
		assertExactJSONKeys(t, mustMarshalJSON(t, activation), "device_id", "device_label", "peer_device_id", "peer_device_label", "provider_user_id", "peer_provider_user_id", "peer_public_key", "sdk_app_id", "usersig", "usersig_expires_at")
	}
	if strings.Contains(desktopReadAgain.Body.String(), desktopToken) || strings.Contains(desktopReadAgain.Body.String(), iosToken) {
		t.Fatalf("desktop repeat read exposed a bearer credential: %s", desktopReadAgain.Body.String())
	}

	iosReadRequest := httptest.NewRequest(http.MethodGet, remoteCompanionBasePath+"/pairings/"+pairingID, nil)
	iosReadRequest.Header.Set("Authorization", "Bearer "+iosToken)
	iosReadRecorder := httptest.NewRecorder()
	mux.ServeHTTP(iosReadRecorder, iosReadRequest)
	if iosReadRecorder.Code != http.StatusOK {
		t.Fatalf("iOS read status = %d, body = %s", iosReadRecorder.Code, iosReadRecorder.Body.String())
	}
	iosRead := assertExactJSONKeys(t, iosReadRecorder.Body.Bytes(), "protocol_version", "pairing_id", "state", "authentication_string", "expires_at", "device_activation")
	iosActivation, ok := iosRead["device_activation"].(map[string]any)
	if !ok {
		t.Fatalf("iOS device_activation = %#v", iosRead["device_activation"])
	}
	assertExactJSONKeys(t, mustMarshalJSON(t, iosActivation), "device_id", "device_label", "peer_device_id", "peer_device_label", "provider_user_id", "peer_provider_user_id", "peer_public_key", "sdk_app_id", "usersig", "usersig_expires_at")
	if iosActivation["device_id"] != "ios-device" || iosActivation["device_label"] != "iPhone" || iosActivation["peer_device_id"] != "desktop-device" || iosActivation["peer_device_label"] != "Desktop" || iosActivation["peer_public_key"] != "desktop-key" {
		t.Fatalf("iOS activation leaked or lost caller-scoped values: %#v", iosActivation)
	}
	if iosActivation["provider_user_id"] != tencentPairUserID(pairingID, "ios") || iosActivation["peer_provider_user_id"] != tencentPairUserID(pairingID, "desktop") || iosActivation["usersig"] == "" {
		t.Fatalf("iOS activation provider values = %#v", iosActivation)
	}
	for _, forbidden := range []string{"desktop_provider_user_id", "ios_provider_user_id", "device_credential", "desktop_credential", "ios_credential", "seat", "desktop_public_key", "ios_public_key"} {
		if _, present := iosRead[forbidden]; present {
			t.Fatalf("iOS read exposed forbidden field %q: %#v", forbidden, iosRead)
		}
	}
	if strings.Contains(iosReadRecorder.Body.String(), desktopToken) || strings.Contains(iosReadRecorder.Body.String(), iosToken) {
		t.Fatalf("iOS read exposed a bearer credential: %s", iosReadRecorder.Body.String())
	}

	iosReadAgain := httptest.NewRecorder()
	mux.ServeHTTP(iosReadAgain, iosReadRequest)
	if iosReadAgain.Code != http.StatusOK {
		t.Fatalf("iOS second read status = %d, body = %s", iosReadAgain.Code, iosReadAgain.Body.String())
	}
	var iosReadAgainBody map[string]any
	if err := json.Unmarshal(iosReadAgain.Body.Bytes(), &iosReadAgainBody); err != nil {
		t.Fatal(err)
	}
	if activation, ok := iosReadAgainBody["device_activation"].(map[string]any); !ok {
		t.Fatalf("iOS repeat read omitted device_activation: %#v", iosReadAgainBody)
	} else {
		assertExactJSONKeys(t, mustMarshalJSON(t, activation), "device_id", "device_label", "peer_device_id", "peer_device_label", "provider_user_id", "peer_provider_user_id", "peer_public_key", "sdk_app_id", "usersig", "usersig_expires_at")
	}
	if strings.Contains(iosReadAgain.Body.String(), desktopToken) || strings.Contains(iosReadAgain.Body.String(), iosToken) {
		t.Fatalf("iOS repeat read exposed a bearer credential: %s", iosReadAgain.Body.String())
	}

	credentialsBody := `{"protocol_version":"` + remoteProtocolVersion + `","device_id":"desktop-device"}`
	credentialsRequest := httptest.NewRequest(http.MethodPost, remoteCompanionBasePath+"/pairings/"+pairingID+"/credentials", strings.NewReader(credentialsBody))
	credentialsRequest.Header.Set("Authorization", "Bearer "+desktopToken)
	credentialsRequest.Header.Set("Idempotency-Key", "canonical-http-credentials")
	credentialsRecorder := httptest.NewRecorder()
	mux.ServeHTTP(credentialsRecorder, credentialsRequest)
	if credentialsRecorder.Code != http.StatusOK {
		t.Fatalf("credentials status = %d, body = %s", credentialsRecorder.Code, credentialsRecorder.Body.String())
	}
	var credentials map[string]any
	if err := json.Unmarshal(credentialsRecorder.Body.Bytes(), &credentials); err != nil {
		t.Fatal(err)
	}
	assertExactJSONKeys(t, credentialsRecorder.Body.Bytes(), "protocol_version", "provider", "sdk_app_id", "provider_user_id", "peer_provider_user_id", "usersig", "usersig_expires_at")
	if credentials["usersig"] == nil || credentials["provider"] != "tencent_cloud_im" || credentials["peer_provider_user_id"] != tencentPairUserID(pairingID, "ios") {
		t.Fatalf("credentials response = %#v", credentials)
	}

	revokeRequest := httptest.NewRequest(http.MethodDelete, remoteCompanionBasePath+"/pairings/"+pairingID, nil)
	revokeRequest.Header.Set("Authorization", "Bearer "+desktopToken)
	revokeRequest.Header.Set("Idempotency-Key", "canonical-http-revoke")
	revokeRecorder := httptest.NewRecorder()
	mux.ServeHTTP(revokeRecorder, revokeRequest)
	if revokeRecorder.Code != http.StatusOK {
		t.Fatalf("revoke status = %d, body = %s", revokeRecorder.Code, revokeRecorder.Body.String())
	}
	var revoked map[string]any
	if err := json.Unmarshal(revokeRecorder.Body.Bytes(), &revoked); err != nil {
		t.Fatal(err)
	}
	receiptID, _ := revoked["revocation_receipt_id"].(string)
	receiptToken, _ := revoked["revocation_receipt_token"].(string)
	if receiptID == "" || receiptToken == "" || revoked["seat_released"] != true {
		t.Fatalf("revoke response = %#v", revoked)
	}

	readRequest := httptest.NewRequest(http.MethodGet, remoteCompanionBasePath+"/revocations/"+receiptID, nil)
	readRequest.Header.Set("Authorization", "Bearer "+receiptToken)
	readRecorder := httptest.NewRecorder()
	mux.ServeHTTP(readRecorder, readRequest)
	if readRecorder.Code != http.StatusOK || !strings.Contains(readRecorder.Body.String(), `"seat_released":true`) {
		t.Fatalf("revocation readback status = %d, body = %s", readRecorder.Code, readRecorder.Body.String())
	}
}

func assertExactJSONKeys(t *testing.T, raw []byte, expected ...string) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("decode JSON for exact-key assertion: %v", err)
	}
	allowed := make(map[string]struct{}, len(expected))
	for _, key := range expected {
		allowed[key] = struct{}{}
		if _, ok := value[key]; !ok {
			t.Fatalf("JSON omitted required field %q: %#v", key, value)
		}
	}
	for key := range value {
		if _, ok := allowed[key]; !ok {
			t.Fatalf("JSON exposed undeclared field %q: %#v", key, value)
		}
	}
	return value
}

func mustMarshalJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
