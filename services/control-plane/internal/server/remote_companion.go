package server

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	controlplaneent "opl-cloud/services/control-plane/ent"
	"opl-cloud/services/control-plane/ent/remotedevicecredential"
	"opl-cloud/services/control-plane/ent/remoteinvitation"
	"opl-cloud/services/control-plane/ent/remotepairing"
	"opl-cloud/services/control-plane/ent/remoteseatcapacity"
)

const (
	remoteCompanionBasePath = "/v1/remote-companion"
	remoteProtocolVersion   = "opl_remote_transport.v1"
	remoteCapacityID        = "remote-companion"
	remoteSeatLimit         = 40
	remoteSeatWarning       = 35
	remoteReservationTTL    = 5 * time.Minute
	remoteInvitationTTL     = 24 * time.Hour
	remoteUserSigTTL        = time.Hour
	remoteManualAttempts    = 5
	remoteTokenBytes        = 32
)

var (
	errRemoteInvalidInvite       = errors.New("invalid_invite")
	errRemoteExpiredInvite       = errors.New("expired_invite")
	errRemoteCapacityUnavailable = errors.New("capacity_unavailable")
	errRemoteClaimMismatch       = errors.New("claim_mismatch")
	errRemoteClaimExpired        = errors.New("claim_expired")
	errRemoteClaimAttempts       = errors.New("claim_attempt_limit")
	errRemoteNotConfirmed        = errors.New("not_confirmed")
	errRemoteAuthentication      = errors.New("authentication_string_mismatch")
	errRemoteRevoked             = errors.New("revoked")
	errRemoteCredentialDenied    = errors.New("credential_denied")
	errRemoteProviderUnavailable = errors.New("provider_unavailable")
	errRemoteProviderNotConfig   = errors.New("provider_not_configured")
	errRemoteInvalidPublicKey    = errors.New("invalid_public_key")
	errRemoteProtocolVersion     = errors.New("protocol_version_unsupported")
	errRemoteInvalidJSON         = errors.New("invalid_json_body")
	errRemoteInvalidRequest      = errors.New("invalid_request")
)

type remoteProviderPair struct {
	DesktopUserID string
	IOSUserID     string
}

// remoteCompanionProvider is the only provider mutation boundary for OPL Link.
// It deliberately carries no task, message, workspace, or pair-master-key data.
type remoteCompanionProvider interface {
	ProvisionPair(context.Context, string) (remoteProviderPair, error)
	KickUser(context.Context, string) error
	DeleteUser(context.Context, string) error
	UserAbsent(context.Context, string) (bool, error)
	SignUserSig(context.Context, string, time.Time, time.Duration) (string, time.Time, error)
}

type remoteCompanionBroker struct {
	store    *postgresEntStateStore
	provider remoteCompanionProvider
	now      func() time.Time
	hashKey  []byte
}

type remoteInviteResponse struct {
	ProtocolVersion string `json:"protocol_version"`
	InvitationID    string `json:"-"`
	Secret          string `json:"invitation_code"`
	ExpiresAt       string `json:"expires_at"`
	Label           string `json:"label,omitempty"`
}

type remoteSeatResponse struct {
	Count   int  `json:"count"`
	Limit   int  `json:"limit"`
	Warning bool `json:"warning"`
}

type remotePairingResponse struct {
	ProtocolVersion       string             `json:"protocol_version"`
	PairingID             string             `json:"pairing_id"`
	State                 string             `json:"state"`
	DesktopPublicKey      string             `json:"desktop_public_key"`
	IOSPublicKey          string             `json:"ios_public_key"`
	AuthenticationString  string             `json:"authentication_string"`
	ExpiresAt             string             `json:"expires_at"`
	ReservationExpiresAt  string             `json:"reservation_expires_at"`
	DesktopProviderUserID string             `json:"desktop_provider_user_id"`
	IOSProviderUserID     string             `json:"ios_provider_user_id"`
	Seat                  remoteSeatResponse `json:"seat"`
	SeatCount             int                `json:"-"`
	SeatLimit             int                `json:"-"`
	SeatWarning           bool               `json:"-"`
}

type remotePairingCreateResponse struct {
	remotePairingResponse
	ClaimSecret       string `json:"claim_secret"`
	ManualCode        string `json:"manual_code"`
	DesktopCredential string `json:"desktop_pair_token"`
	BrokerURL         string `json:"broker_url"`
}

type remoteClaimResponse struct {
	remotePairingResponse
	IOSCredential string `json:"ios_claim_token"`
}

type remoteCredentialResponse struct {
	ProtocolVersion    string `json:"protocol_version"`
	PairingID          string `json:"pairing_id"`
	DeviceID           string `json:"device_id"`
	Role               string `json:"role"`
	Provider           string `json:"provider"`
	SDKAppID           int64  `json:"sdk_app_id"`
	PushBusinessID     int64  `json:"push_business_id,omitempty"`
	ProviderUserID     string `json:"provider_user_id"`
	PeerProviderUserID string `json:"peer_provider_user_id"`
	UserSig            string `json:"usersig"`
	UserSigExpiresAt   string `json:"usersig_expires_at"`
	State              string `json:"state"`
}

const remoteCredentialOperationAction = "remote_companion.credentials"

// remoteCredentialIssue records the non-secret facts needed to reproduce a
// provider signature after a process restart. The signature itself remains
// provider-derived and is never written to the operation result.
type remoteCredentialIssue struct {
	PairingID          string `json:"pairing_id"`
	DeviceID           string `json:"device_id"`
	Role               string `json:"role"`
	RequestHash        string `json:"request_hash"`
	IdempotencyKeyHash string `json:"idempotency_key_hash"`
	ProviderUserID     string `json:"provider_user_id"`
	PeerProviderUserID string `json:"peer_provider_user_id"`
	SDKAppID           int64  `json:"sdk_app_id"`
	IssuedAt           string `json:"issued_at"`
	UserSigExpiresAt   string `json:"usersig_expires_at"`
}

// Internal pairing responses retain provider and seat details for broker
// operations. Route-specific responses below are the only HTTP wire shapes.
type remoteCreateHTTPResponse struct {
	ProtocolVersion  string `json:"protocol_version"`
	PairingID        string `json:"pairing_id"`
	DesktopPairToken string `json:"desktop_pair_token"`
	ClaimSecret      string `json:"claim_secret"`
	ManualCode       string `json:"manual_code"`
	ExpiresAt        string `json:"expires_at"`
	BrokerURL        string `json:"broker_url"`
}

type remoteInviteHTTPResponse struct {
	InvitationCode string `json:"invitation_code"`
	ExpiresAt      string `json:"expires_at"`
}

type remoteClaimHTTPResponse struct {
	ProtocolVersion      string `json:"protocol_version"`
	PairingID            string `json:"pairing_id"`
	IOSClaimToken        string `json:"ios_claim_token"`
	State                string `json:"state"`
	AuthenticationString string `json:"authentication_string"`
	ExpiresAt            string `json:"expires_at"`
}

type remoteConfirmHTTPResponse struct {
	ProtocolVersion string `json:"protocol_version"`
	PairingID       string `json:"pairing_id"`
	State           string `json:"state"`
}

type remoteCredentialHTTPResponse struct {
	ProtocolVersion    string `json:"protocol_version"`
	Provider           string `json:"provider"`
	SDKAppID           int64  `json:"sdk_app_id"`
	PushBusinessID     int64  `json:"push_business_id,omitempty"`
	ProviderUserID     string `json:"provider_user_id"`
	PeerProviderUserID string `json:"peer_provider_user_id"`
	UserSig            string `json:"usersig"`
	UserSigExpiresAt   string `json:"usersig_expires_at"`
}

type remoteRevokeHTTPResponse struct {
	ProtocolVersion        string `json:"protocol_version"`
	PairingID              string `json:"pairing_id"`
	State                  string `json:"state"`
	RevocationReceiptID    string `json:"revocation_receipt_id"`
	RevocationReceiptToken string `json:"revocation_receipt_token"`
}

type remoteDeviceActivation struct {
	DeviceID           string `json:"device_id"`
	DeviceLabel        string `json:"device_label"`
	PeerDeviceID       string `json:"peer_device_id"`
	PeerDeviceLabel    string `json:"peer_device_label"`
	ProviderUserID     string `json:"provider_user_id"`
	PeerProviderUserID string `json:"peer_provider_user_id"`
	PeerPublicKey      string `json:"peer_public_key"`
	SDKAppID           int64  `json:"sdk_app_id"`
	PushBusinessID     int64  `json:"push_business_id,omitempty"`
	UserSig            string `json:"usersig"`
	UserSigExpiresAt   string `json:"usersig_expires_at"`
}

type remoteReadPairingResponse struct {
	ProtocolVersion      string                 `json:"protocol_version"`
	PairingID            string                 `json:"pairing_id"`
	State                string                 `json:"state"`
	AuthenticationString string                 `json:"authentication_string"`
	ExpiresAt            string                 `json:"expires_at"`
	DeviceActivation     remoteDeviceActivation `json:"device_activation"`
}

type remoteProviderReclaimReceipt struct {
	ProviderUserID string `json:"provider_user_id"`
	Absent         bool   `json:"absent"`
	Error          string `json:"error,omitempty"`
}

type remoteRevokeReceipt struct {
	ProtocolVersion        string                       `json:"protocol_version"`
	PairingID              string                       `json:"pairing_id"`
	State                  string                       `json:"state"`
	RevocationReceiptID    string                       `json:"revocation_receipt_id"`
	RevocationReceiptToken string                       `json:"revocation_receipt_token,omitempty"`
	DesktopProviderAbsent  bool                         `json:"desktop_provider_absent"`
	IOSProviderAbsent      bool                         `json:"ios_provider_absent"`
	SeatReleased           bool                         `json:"seat_released"`
	RevokedAt              string                       `json:"revoked_at"`
	Desktop                remoteProviderReclaimReceipt `json:"-"`
	IOS                    remoteProviderReclaimReceipt `json:"-"`
}

type remoteRevocationReadResponse struct {
	ProtocolVersion               string `json:"protocol_version"`
	PairingID                     string `json:"pairing_id"`
	State                         string `json:"state"`
	DesktopProviderIdentityAbsent bool   `json:"desktop_provider_identity_absent"`
	IOSProviderIdentityAbsent     bool   `json:"ios_provider_identity_absent"`
	SeatReleased                  bool   `json:"seat_released"`
}

func newRemoteCompanionBroker(store StateStore, provider remoteCompanionProvider) (*remoteCompanionBroker, error) {
	postgresStore, ok := store.(*postgresEntStateStore)
	if !ok {
		return nil, nil
	}
	key := []byte(strings.TrimSpace(os.Getenv("OPL_LINK_TOKEN_HASH_KEY")))
	if len(key) < 32 {
		// A process-local random key would invalidate every invitation and claim
		// after restart. Keep the broker unavailable until its stable key exists.
		return nil, nil
	}
	if provider == nil {
		provider = newTencentRemoteCompanionProviderFromEnv()
	}
	broker := &remoteCompanionBroker{store: postgresStore, provider: provider, now: func() time.Time { return time.Now().UTC() }, hashKey: key}
	if err := broker.ensureCapacity(context.Background()); err != nil {
		return nil, err
	}
	return broker, nil
}

func (b *remoteCompanionBroker) ensureCapacity(ctx context.Context) error {
	_, err := b.store.client.RemoteSeatCapacity.Get(ctx, remoteCapacityID)
	if err == nil {
		return nil
	}
	if !controlplaneent.IsNotFound(err) {
		return err
	}
	now := b.now()
	_, err = b.store.client.RemoteSeatCapacity.Create().
		SetID(remoteCapacityID).
		SetCreatedAt(now).
		SetUpdatedAt(now).
		SetSeatCount(0).
		SetSeatLimit(remoteSeatLimit).
		SetWarningThreshold(remoteSeatWarning).
		Save(ctx)
	if controlplaneent.IsConstraintError(err) {
		_, err = b.store.client.RemoteSeatCapacity.Get(ctx, remoteCapacityID)
	}
	return err
}

func (b *remoteCompanionBroker) withTx(ctx context.Context, fn func(*controlplaneent.Tx) error) error {
	for attempt := 0; attempt < 8; attempt++ {
		tx, err := b.store.client.Tx(ctx)
		if err != nil {
			if remoteRetryableDBError(err) {
				if err := remoteRetryDelay(ctx, attempt); err != nil {
					return err
				}
				continue
			}
			return err
		}
		err = fn(tx)
		if err != nil {
			_ = tx.Rollback()
		} else {
			err = tx.Commit()
		}
		if remoteRetryableDBError(err) && attempt < 7 {
			if delayErr := remoteRetryDelay(ctx, attempt); delayErr != nil {
				return delayErr
			}
			continue
		}
		return err
	}
	return errors.New("remote_companion_transaction_retry_exhausted")
}

func remoteRetryableDBError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "database is locked") || strings.Contains(message, "database table is locked") || strings.Contains(message, "deadlock detected") || strings.Contains(message, "could not serialize")
}

func remoteRetryDelay(ctx context.Context, attempt int) error {
	timer := time.NewTimer(time.Duration(attempt+1) * 5 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *remoteCompanionBroker) lockCapacityTx(ctx context.Context, tx *controlplaneent.Tx, now time.Time) error {
	_, err := tx.RemoteSeatCapacity.UpdateOneID(remoteCapacityID).SetUpdatedAt(now).Save(ctx)
	return err
}

func (b *remoteCompanionBroker) reclaimExpiredReservationsTx(ctx context.Context, tx *controlplaneent.Tx, now time.Time) error {
	expired, err := tx.RemotePairing.Query().
		Where(remotepairing.StateIn("reserved", "awaiting_desktop_confirmation"), remotepairing.ReservationExpiresAtLT(now.Format(time.RFC3339Nano)), remotepairing.SeatReleasedEQ(false)).
		All(ctx)
	if err != nil {
		return err
	}
	for _, pairing := range expired {
		if _, err := tx.RemotePairing.UpdateOneID(pairing.ID).
			SetState("revoked").
			SetRevokedAt(now.Format(time.RFC3339Nano)).
			SetSeatReleased(true).
			Save(ctx); err != nil {
			return err
		}
	}
	if len(expired) == 0 {
		return nil
	}
	_, err = tx.RemoteSeatCapacity.UpdateOneID(remoteCapacityID).AddSeatCount(-len(expired)).SetUpdatedAt(now).Save(ctx)
	return err
}

func (b *remoteCompanionBroker) releaseSeatTx(ctx context.Context, tx *controlplaneent.Tx, pairing *controlplaneent.RemotePairing, now time.Time) error {
	if pairing.SeatReleased {
		return nil
	}
	if _, err := tx.RemotePairing.UpdateOneID(pairing.ID).
		SetState("revoked").
		SetRevokedAt(now.Format(time.RFC3339Nano)).
		SetSeatReleased(true).
		Save(ctx); err != nil {
		return err
	}
	_, err := tx.RemoteSeatCapacity.UpdateOneID(remoteCapacityID).AddSeatCount(-1).SetUpdatedAt(now).Save(ctx)
	return err
}

func (b *remoteCompanionBroker) createInvite(ctx context.Context, createdBy string) (remoteInviteResponse, error) {
	return b.createInviteWithOptions(ctx, createdBy, "", "", "")
}

func (b *remoteCompanionBroker) createInviteWithOptions(ctx context.Context, createdBy, label, requestedExpiry, idempotencyKey string) (remoteInviteResponse, error) {
	now := b.now()
	secret := ""
	id := ""
	var err error
	if idempotencyKey != "" {
		id = remoteStableID("inv", idempotencyKey)
		secret = b.stableToken("invitation", idempotencyKey)
		if existing, getErr := b.store.client.RemoteInvitation.Get(ctx, id); getErr == nil {
			if existing.CreatedByUserID != createdBy || existing.Label != label || existing.IdempotencyKey != idempotencyKey {
				return remoteInviteResponse{}, errRemoteInvalidRequest
			}
			return remoteInviteResponse{ProtocolVersion: remoteProtocolVersion, InvitationID: existing.ID, Secret: secret, ExpiresAt: existing.ExpiresAt, Label: existing.Label}, nil
		} else if !controlplaneent.IsNotFound(getErr) {
			return remoteInviteResponse{}, getErr
		}
	} else {
		secret, err = remoteSecret()
		if err != nil {
			return remoteInviteResponse{}, err
		}
		id, err = remoteID("inv")
		if err != nil {
			return remoteInviteResponse{}, err
		}
	}

	if strings.TrimSpace(requestedExpiry) == "" {
		requestedExpiry = now.Add(remoteInvitationTTL).Format(time.RFC3339Nano)
	} else {
		deadline, parseErr := time.Parse(time.RFC3339Nano, requestedExpiry)
		if parseErr != nil || !deadline.After(now) {
			return remoteInviteResponse{}, errRemoteInvalidRequest
		}
		requestedExpiry = deadline.UTC().Format(time.RFC3339Nano)
	}
	digest, err := b.tokenDigest(secret)
	if err != nil {
		return remoteInviteResponse{}, err
	}
	_, err = b.store.client.RemoteInvitation.Create().
		SetID(id).
		SetCreatedAt(now).
		SetUpdatedAt(now).
		SetLabel(label).
		SetSecretHash(digest.hash).
		SetSecretSalt(digest.salt).
		SetExpiresAt(requestedExpiry).
		SetStatus("active").
		SetConsumedAt("").
		SetCreatedByUserID(createdBy).
		SetIdempotencyKey(idempotencyKey).
		Save(ctx)
	if err != nil {
		return remoteInviteResponse{}, err
	}
	return remoteInviteResponse{ProtocolVersion: remoteProtocolVersion, InvitationID: id, Secret: secret, ExpiresAt: requestedExpiry, Label: label}, nil
}

type remoteTokenDigest struct {
	hash string
	salt string
}

func (b *remoteCompanionBroker) tokenDigest(token string) (remoteTokenDigest, error) {
	salt, err := remoteRandomBytes(16)
	if err != nil {
		return remoteTokenDigest{}, err
	}
	hash := remoteHMACDigest(b.hashKey, salt, []byte(token))
	return remoteTokenDigest{hash: hex.EncodeToString(hash), salt: hex.EncodeToString(salt)}, nil
}

func (b *remoteCompanionBroker) stableToken(label, key string) string {
	return base64.RawURLEncoding.EncodeToString(remoteHMACDigest(b.hashKey, []byte(label), []byte(key)))
}

func remoteStableID(prefix, key string) string {
	sum := sha256.Sum256([]byte(prefix + "\x00" + key))
	return prefix + "-" + hex.EncodeToString(sum[:16])
}

func remoteRequestHash(parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = hash.Write([]byte(fmt.Sprintf("%d:%s|", len([]byte(part)), part)))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func (b *remoteCompanionBroker) tokenMatches(hash, salt, token string) bool {
	saltBytes, saltErr := hex.DecodeString(salt)
	expected, hashErr := hex.DecodeString(hash)
	if saltErr != nil {
		saltBytes = nil
	}
	if hashErr != nil || len(expected) != sha256.Size {
		expected = make([]byte, sha256.Size)
	}
	actual := remoteHMACDigest(b.hashKey, saltBytes, []byte(token))
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

func remoteHMACDigest(key, salt, value []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(salt)
	_, _ = mac.Write(value)
	return mac.Sum(nil)
}

func remoteCredentialHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (b *remoteCompanionBroker) createPairing(ctx context.Context, inviteSecret, desktopPublicKey string) (remotePairingCreateResponse, error) {
	return b.createPairingWithOptions(ctx, inviteSecret, "", "", desktopPublicKey, "")
}

func (b *remoteCompanionBroker) createPairingWithOptions(ctx context.Context, inviteSecret, desktopDeviceID, desktopDeviceLabel, desktopPublicKey, idempotencyKey string) (remotePairingCreateResponse, error) {
	if strings.TrimSpace(inviteSecret) == "" {
		return remotePairingCreateResponse{}, errRemoteInvalidInvite
	}
	if err := validateRemotePublicKey(desktopPublicKey); err != nil {
		return remotePairingCreateResponse{}, err
	}
	requestHash := remoteRequestHash(inviteSecret, desktopDeviceID, desktopDeviceLabel, desktopPublicKey)
	pairingID := ""
	var err error
	if idempotencyKey != "" {
		pairingID = remoteStableID("pair", idempotencyKey)
		if existing, getErr := b.store.client.RemotePairing.Get(ctx, pairingID); getErr == nil {
			if existing.CreateIdempotencyKey != idempotencyKey || existing.CreateRequestHash != requestHash {
				return remotePairingCreateResponse{}, errRemoteInvalidRequest
			}
			return b.replayPairingCreate(ctx, existing, idempotencyKey)
		} else if !controlplaneent.IsNotFound(getErr) {
			return remotePairingCreateResponse{}, getErr
		}
	} else {
		pairingID, err = remoteID("pair")
		if err != nil {
			return remotePairingCreateResponse{}, err
		}
	}
	claimSecret := ""
	manualCode := ""
	desktopCredential := ""
	if idempotencyKey != "" {
		claimSecret = b.stableToken("claim:"+pairingID, idempotencyKey)
		manualCode = b.stableManualCode(pairingID, idempotencyKey)
		desktopCredential = b.stableToken("desktop_pair_token:"+pairingID, idempotencyKey)
	} else {
		claimSecret, err = remoteSecret()
		if err != nil {
			return remotePairingCreateResponse{}, err
		}
		manualCode, err = remoteManualCode()
		if err != nil {
			return remotePairingCreateResponse{}, err
		}
		desktopCredential, err = remoteSecret()
		if err != nil {
			return remotePairingCreateResponse{}, err
		}
	}
	claimDigest, err := b.tokenDigest(claimSecret)
	if err != nil {
		return remotePairingCreateResponse{}, err
	}
	manualDigest, err := b.tokenDigest(manualCode)
	if err != nil {
		return remotePairingCreateResponse{}, err
	}
	if strings.TrimSpace(desktopDeviceID) == "" {
		desktopDeviceID, err = remoteID("dev")
		if err != nil {
			return remotePairingCreateResponse{}, err
		}
	}
	desktopCredentialID, err := remoteID("cred")
	if err != nil {
		return remotePairingCreateResponse{}, err
	}
	now := b.now()
	expires := now.Add(remoteReservationTTL).Format(time.RFC3339Nano)
	var created *controlplaneent.RemotePairing
	err = b.withTx(ctx, func(tx *controlplaneent.Tx) error {
		if err := b.lockCapacityTx(ctx, tx, now); err != nil {
			return err
		}
		if err := b.reclaimExpiredReservationsTx(ctx, tx, now); err != nil {
			return err
		}
		invitations, err := tx.RemoteInvitation.Query().All(ctx)
		if err != nil {
			return err
		}
		var matched *controlplaneent.RemoteInvitation
		expired := false
		for _, invitation := range invitations {
			if !b.tokenMatches(invitation.SecretHash, invitation.SecretSalt, inviteSecret) {
				continue
			}
			if invitation.Status != "active" {
				continue
			}
			deadline, parseErr := time.Parse(time.RFC3339Nano, invitation.ExpiresAt)
			if parseErr != nil || !deadline.After(now) {
				expired = true
				continue
			}
			matched = invitation
		}
		if matched == nil {
			if expired {
				return errRemoteExpiredInvite
			}
			return errRemoteInvalidInvite
		}
		affected, err := tx.RemoteSeatCapacity.Update().
			Where(remoteseatcapacity.ID(remoteCapacityID), remoteseatcapacity.SeatCountLT(remoteSeatLimit)).
			AddSeatCount(1).
			SetUpdatedAt(now).
			Save(ctx)
		if err != nil {
			return err
		}
		if affected != 1 {
			return errRemoteCapacityUnavailable
		}
		consumed, err := tx.RemoteInvitation.Update().
			Where(
				remoteinvitation.ID(matched.ID),
				remoteinvitation.StatusEQ("active"),
				remoteinvitation.ExpiresAtGT(now.Format(time.RFC3339Nano)),
			).
			SetStatus("consumed").
			SetConsumedAt(now.Format(time.RFC3339Nano)).
			SetUpdatedAt(now).
			Save(ctx)
		if err != nil {
			return err
		}
		if consumed != 1 {
			return errRemoteInvalidInvite
		}
		created, err = tx.RemotePairing.Create().
			SetID(pairingID).
			SetCreatedAt(now).
			SetUpdatedAt(now).
			SetInvitationID(matched.ID).
			SetClaimSecretHash(claimDigest.hash).
			SetClaimSecretSalt(claimDigest.salt).
			SetManualCodeHash(manualDigest.hash).
			SetManualCodeSalt(manualDigest.salt).
			SetManualAttempts(0).
			SetState("reserved").
			SetExpiresAt(expires).
			SetReservationExpiresAt(expires).
			SetDesktopDeviceID(desktopDeviceID).
			SetDesktopDeviceLabel(desktopDeviceLabel).
			SetIosDeviceID("").
			SetIosDeviceLabel("").
			SetDesktopPublicKey(desktopPublicKey).
			SetIosPublicKey("").
			SetSas("").
			SetDesktopProviderUserID("").
			SetIosProviderUserID("").
			SetDesktopProviderAbsent(false).
			SetIosProviderAbsent(false).
			SetConfirmedAt("").
			SetRevokedAt("").
			SetSeatReleased(false).
			SetCreateIdempotencyKey(idempotencyKey).
			SetCreateRequestHash(requestHash).
			Save(ctx)
		if err != nil {
			return err
		}
		return tx.RemoteDeviceCredential.Create().
			SetID(desktopCredentialID).
			SetCreatedAt(now).
			SetUpdatedAt(now).
			SetPairingID(pairingID).
			SetDeviceID(desktopDeviceID).
			SetRole("desktop").
			SetCredentialHash(remoteCredentialHash(desktopCredential)).
			SetStatus("active").
			SetProviderUserID("").
			SetIssuedAt(now.Format(time.RFC3339Nano)).
			SetRevokedAt("").
			SetIssuedIdempotencyKey(idempotencyKey).
			Exec(ctx)
	})
	if err != nil {
		return remotePairingCreateResponse{}, err
	}
	capacity, err := b.capacity(ctx)
	if err != nil {
		return remotePairingCreateResponse{}, err
	}
	return remotePairingCreateResponse{
		remotePairingResponse: b.pairingResponse(created, capacity),
		ClaimSecret:           claimSecret,
		ManualCode:            manualCode,
		DesktopCredential:     desktopCredential,
		BrokerURL:             remoteBrokerURL(),
	}, nil
}

func (b *remoteCompanionBroker) replayPairingCreate(ctx context.Context, pairing *controlplaneent.RemotePairing, idempotencyKey string) (remotePairingCreateResponse, error) {
	capacity, err := b.capacity(ctx)
	if err != nil {
		return remotePairingCreateResponse{}, err
	}
	return remotePairingCreateResponse{
		remotePairingResponse: b.pairingResponse(pairing, capacity),
		ClaimSecret:           b.stableToken("claim:"+pairing.ID, idempotencyKey),
		ManualCode:            b.stableManualCode(pairing.ID, idempotencyKey),
		DesktopCredential:     b.stableToken("desktop_pair_token:"+pairing.ID, idempotencyKey),
		BrokerURL:             remoteBrokerURL(),
	}, nil
}

func (b *remoteCompanionBroker) claimPairing(ctx context.Context, pairingID, claimSecret, manualCode, iosPublicKey string) (remoteClaimResponse, error) {
	return b.claimPairingWithOptions(ctx, pairingID, claimSecret, manualCode, "", "", iosPublicKey, "")
}

func (b *remoteCompanionBroker) claimPairingWithOptions(ctx context.Context, pairingID, claimSecret, manualCode, iosDeviceID, iosDeviceLabel, iosPublicKey, idempotencyKey string) (remoteClaimResponse, error) {
	if !validRemoteID(pairingID, "pair-") {
		return remoteClaimResponse{}, errRemoteClaimMismatch
	}
	if (strings.TrimSpace(claimSecret) == "") == (strings.TrimSpace(manualCode) == "") {
		return remoteClaimResponse{}, errRemoteClaimMismatch
	}
	if err := validateRemotePublicKey(iosPublicKey); err != nil {
		return remoteClaimResponse{}, err
	}
	requestHash := remoteRequestHash(pairingID, claimSecret, manualCode, iosDeviceID, iosDeviceLabel, iosPublicKey)
	if idempotencyKey != "" {
		existing, getErr := b.store.client.RemotePairing.Get(ctx, pairingID)
		if getErr != nil && !controlplaneent.IsNotFound(getErr) {
			return remoteClaimResponse{}, getErr
		}
		if getErr == nil && existing.ClaimIdempotencyKey != "" {
			if existing.ClaimIdempotencyKey != idempotencyKey || existing.ClaimRequestHash != requestHash {
				return remoteClaimResponse{}, errRemoteInvalidRequest
			}
			if existing.IosPublicKey != iosPublicKey {
				return remoteClaimResponse{}, errRemoteInvalidRequest
			}
			capacity, capErr := b.capacity(ctx)
			if capErr != nil {
				return remoteClaimResponse{}, capErr
			}
			return remoteClaimResponse{
				remotePairingResponse: b.pairingResponse(existing, capacity),
				IOSCredential:         b.stableToken("ios_claim_token:"+pairingID, idempotencyKey),
			}, nil
		}
	}
	now := b.now()
	var pairing *controlplaneent.RemotePairing
	var iosCredential string
	var claimErr error
	var err error
	err = b.withTx(ctx, func(tx *controlplaneent.Tx) error {
		if err := b.lockCapacityTx(ctx, tx, now); err != nil {
			return err
		}
		pairing, err = tx.RemotePairing.Get(ctx, pairingID)
		if controlplaneent.IsNotFound(err) {
			claimErr = errRemoteClaimMismatch
			return nil
		}
		if err != nil {
			return err
		}
		if pairing.State == "revoked" || pairing.SeatReleased {
			claimErr = errRemoteRevoked
			return nil
		}
		deadline, parseErr := time.Parse(time.RFC3339Nano, pairing.ReservationExpiresAt)
		if parseErr != nil || !deadline.After(now) {
			if err := b.releaseSeatTx(ctx, tx, pairing, now); err != nil {
				return err
			}
			claimErr = errRemoteClaimExpired
			return nil
		}
		if manualCode != "" {
			if pairing.ManualAttempts >= remoteManualAttempts {
				if err := b.releaseSeatTx(ctx, tx, pairing, now); err != nil {
					return err
				}
				claimErr = errRemoteClaimAttempts
				return nil
			}
			if !b.tokenMatches(pairing.ManualCodeHash, pairing.ManualCodeSalt, manualCode) {
				attempts := pairing.ManualAttempts + 1
				if _, err := tx.RemotePairing.UpdateOneID(pairing.ID).SetManualAttempts(attempts).Save(ctx); err != nil {
					return err
				}
				pairing.ManualAttempts = attempts
				if attempts >= remoteManualAttempts {
					if err := b.releaseSeatTx(ctx, tx, pairing, now); err != nil {
						return err
					}
					claimErr = errRemoteClaimAttempts
					return nil
				}
				claimErr = errRemoteClaimMismatch
				return nil
			}
		} else if !b.tokenMatches(pairing.ClaimSecretHash, pairing.ClaimSecretSalt, claimSecret) {
			claimErr = errRemoteClaimMismatch
			return nil
		}
		if pairing.IosPublicKey != "" {
			claimErr = errRemoteClaimMismatch
			return nil
		}
		sas := remoteSAS(pairing.ID, pairing.DesktopPublicKey, iosPublicKey)
		iosCredential, err = remoteSecret()
		if idempotencyKey != "" {
			iosCredential = b.stableToken("ios_claim_token:"+pairingID, idempotencyKey)
		}
		if err != nil {
			return err
		}
		iosCredentialID, err := remoteID("cred")
		if err != nil {
			return err
		}
		credentialDeviceID := strings.TrimSpace(iosDeviceID)
		if credentialDeviceID == "" {
			if idempotencyKey != "" {
				credentialDeviceID = remoteStableID("dev", pairingID+":"+idempotencyKey)
			} else {
				credentialDeviceID, err = remoteID("dev")
			}
		}
		if err != nil {
			return err
		}
		if _, err := tx.RemotePairing.UpdateOneID(pairing.ID).
			SetState("awaiting_desktop_confirmation").
			SetIosDeviceID(credentialDeviceID).
			SetIosDeviceLabel(iosDeviceLabel).
			SetIosPublicKey(iosPublicKey).
			SetSas(sas).
			SetClaimIdempotencyKey(idempotencyKey).
			SetClaimRequestHash(requestHash).
			Save(ctx); err != nil {
			return err
		}
		if _, err := tx.RemoteDeviceCredential.Create().
			SetID(iosCredentialID).
			SetCreatedAt(now).
			SetUpdatedAt(now).
			SetPairingID(pairing.ID).
			SetDeviceID(credentialDeviceID).
			SetRole("ios").
			SetCredentialHash(remoteCredentialHash(iosCredential)).
			SetStatus("active").
			SetProviderUserID("").
			SetIssuedAt(now.Format(time.RFC3339Nano)).
			SetRevokedAt("").
			SetIssuedIdempotencyKey(idempotencyKey).
			Save(ctx); err != nil {
			return err
		}
		pairing.State = "awaiting_desktop_confirmation"
		pairing.IosDeviceID = credentialDeviceID
		pairing.IosDeviceLabel = iosDeviceLabel
		pairing.IosPublicKey = iosPublicKey
		pairing.Sas = sas
		return nil
	})
	if err != nil {
		return remoteClaimResponse{}, err
	}
	if claimErr != nil {
		return remoteClaimResponse{}, claimErr
	}
	capacity, err := b.capacity(ctx)
	if err != nil {
		return remoteClaimResponse{}, err
	}
	return remoteClaimResponse{remotePairingResponse: b.pairingResponse(pairing, capacity), IOSCredential: iosCredential}, nil
}

func (b *remoteCompanionBroker) authenticate(ctx context.Context, pairingID, credential string) (*controlplaneent.RemotePairing, *controlplaneent.RemoteDeviceCredential, error) {
	return b.authenticateDevice(ctx, pairingID, credential, "")
}

func (b *remoteCompanionBroker) authenticateDevice(ctx context.Context, pairingID, credential, deviceID string) (*controlplaneent.RemotePairing, *controlplaneent.RemoteDeviceCredential, error) {
	if !validRemoteID(pairingID, "pair-") || strings.TrimSpace(credential) == "" {
		return nil, nil, errRemoteCredentialDenied
	}
	pairing, err := b.store.client.RemotePairing.Get(ctx, pairingID)
	if controlplaneent.IsNotFound(err) {
		return nil, nil, errRemoteCredentialDenied
	}
	if err != nil {
		return nil, nil, err
	}
	if pairing.State == "revoked" || pairing.SeatReleased {
		return nil, nil, errRemoteRevoked
	}
	credentials, err := b.store.client.RemoteDeviceCredential.Query().Where(remotedevicecredential.PairingIDEQ(pairingID)).All(ctx)
	if err != nil {
		return nil, nil, err
	}
	want := remoteCredentialHash(credential)
	var matched *controlplaneent.RemoteDeviceCredential
	for _, candidate := range credentials {
		candidateHash := candidate.CredentialHash
		isMatch := subtle.ConstantTimeCompare([]byte(candidateHash), []byte(want)) == 1
		if candidate.Status == "active" && (deviceID == "" || candidate.DeviceID == deviceID) && isMatch && matched == nil {
			matched = candidate
		}
	}
	if matched == nil {
		return nil, nil, errRemoteCredentialDenied
	}
	return pairing, matched, nil
}

func (b *remoteCompanionBroker) readClaim(ctx context.Context, pairingID, credential string) (remoteReadPairingResponse, error) {
	pairing, device, err := b.authenticateDevice(ctx, pairingID, credential, "")
	if err != nil {
		return remoteReadPairingResponse{}, err
	}
	activation, err := b.deviceActivation(ctx, pairing, device)
	if err != nil {
		return remoteReadPairingResponse{}, err
	}
	return remoteReadPairingResponse{
		ProtocolVersion:      remoteProtocolVersion,
		PairingID:            pairing.ID,
		State:                pairing.State,
		AuthenticationString: pairing.Sas,
		ExpiresAt:            pairing.ExpiresAt,
		DeviceActivation:     activation,
	}, nil
}

func (b *remoteCompanionBroker) deviceActivation(ctx context.Context, pairing *controlplaneent.RemotePairing, device *controlplaneent.RemoteDeviceCredential) (remoteDeviceActivation, error) {
	providerUserID, peerProviderUserID, peerPublicKey, err := remoteProviderIdentity(pairing, device)
	if err != nil {
		return remoteDeviceActivation{}, err
	}
	activation := remoteDeviceActivation{
		DeviceID:           device.DeviceID,
		ProviderUserID:     providerUserID,
		PeerProviderUserID: peerProviderUserID,
		PeerPublicKey:      peerPublicKey,
		SDKAppID:           remoteProviderSDKAppID(b.provider),
		PushBusinessID:     remoteProviderPushBusinessID(b.provider),
	}
	switch device.Role {
	case "desktop":
		activation.DeviceLabel = pairing.DesktopDeviceLabel
		activation.PeerDeviceID = pairing.IosDeviceID
		activation.PeerDeviceLabel = pairing.IosDeviceLabel
	case "ios":
		activation.DeviceLabel = pairing.IosDeviceLabel
		activation.PeerDeviceID = pairing.DesktopDeviceID
		activation.PeerDeviceLabel = pairing.DesktopDeviceLabel
	}
	if pairing.State != "active" {
		return activation, nil
	}
	if providerUserID == "" {
		return remoteDeviceActivation{}, errRemoteNotConfirmed
	}
	sig, expires, err := b.provider.SignUserSig(ctx, providerUserID, b.now(), remoteUserSigTTL)
	if err != nil {
		return remoteDeviceActivation{}, err
	}
	activation.UserSig = sig
	activation.UserSigExpiresAt = expires.UTC().Format(time.RFC3339Nano)
	return activation, nil
}

func (b *remoteCompanionBroker) confirmPairing(ctx context.Context, pairingID, credential, authenticationString string) (remotePairingResponse, error) {
	pairing, device, err := b.authenticate(ctx, pairingID, credential)
	if err != nil {
		return remotePairingResponse{}, err
	}
	if device.Role != "desktop" {
		return remotePairingResponse{}, errRemoteCredentialDenied
	}
	authenticationString = strings.TrimSpace(authenticationString)
	if pairing.Sas == "" || len(pairing.Sas) != len(authenticationString) || subtle.ConstantTimeCompare([]byte(pairing.Sas), []byte(authenticationString)) != 1 {
		return remotePairingResponse{}, errRemoteAuthentication
	}
	if pairing.State == "active" {
		capacity, capErr := b.capacity(ctx)
		if capErr != nil {
			return remotePairingResponse{}, capErr
		}
		return b.pairingResponse(pairing, capacity), nil
	}
	if pairing.IosPublicKey == "" {
		return remotePairingResponse{}, errRemoteNotConfirmed
	}
	if pairing.State == "revoking" || pairing.State == "provider_reclaim_pending" {
		return remotePairingResponse{}, errRemoteRevoked
	}
	now := b.now()
	err = b.withTx(ctx, func(tx *controlplaneent.Tx) error {
		if err := b.lockCapacityTx(ctx, tx, now); err != nil {
			return err
		}
		current, err := tx.RemotePairing.Get(ctx, pairingID)
		if err != nil {
			return err
		}
		if current.State == "revoked" || current.SeatReleased {
			return errRemoteRevoked
		}
		if current.IosPublicKey == "" {
			return errRemoteNotConfirmed
		}
		if current.State == "reserved" || current.State == "awaiting_desktop_confirmation" {
			deadline, parseErr := time.Parse(time.RFC3339Nano, current.ReservationExpiresAt)
			if parseErr != nil || !deadline.After(now) {
				if err := b.releaseSeatTx(ctx, tx, current, now); err != nil {
					return err
				}
				return errRemoteClaimExpired
			}
		}
		_, err = tx.RemotePairing.UpdateOneID(pairingID).SetState("provisioning").Save(ctx)
		return err
	})
	if err != nil {
		return remotePairingResponse{}, err
	}
	providerPair, err := b.provider.ProvisionPair(ctx, pairingID)
	if err != nil {
		if persistErr := b.persistProviderReclaimPending(ctx, pairingID, providerPair); persistErr != nil {
			return remotePairingResponse{}, errors.Join(err, persistErr)
		}
		return remotePairingResponse{}, err
	}
	now = b.now()
	var active *controlplaneent.RemotePairing
	err = b.withTx(ctx, func(tx *controlplaneent.Tx) error {
		if err := b.lockCapacityTx(ctx, tx, now); err != nil {
			return err
		}
		current, err := tx.RemotePairing.Get(ctx, pairingID)
		if err != nil {
			return err
		}
		if current.State == "revoked" || current.SeatReleased {
			return errRemoteRevoked
		}
		active, err = tx.RemotePairing.UpdateOneID(pairingID).
			SetState("active").
			SetDesktopProviderUserID(providerPair.DesktopUserID).
			SetIosProviderUserID(providerPair.IOSUserID).
			SetDesktopProviderAbsent(false).
			SetIosProviderAbsent(false).
			SetConfirmedAt(now.Format(time.RFC3339Nano)).
			Save(ctx)
		if err != nil {
			return err
		}
		credentials, err := tx.RemoteDeviceCredential.Query().Where(remotedevicecredential.PairingIDEQ(pairingID)).All(ctx)
		if err != nil {
			return err
		}
		for _, device := range credentials {
			providerID := providerPair.DesktopUserID
			if device.Role == "ios" {
				providerID = providerPair.IOSUserID
			}
			if _, err := tx.RemoteDeviceCredential.UpdateOneID(device.ID).SetProviderUserID(providerID).Save(ctx); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return remotePairingResponse{}, err
	}
	capacity, err := b.capacity(ctx)
	if err != nil {
		return remotePairingResponse{}, err
	}
	return b.pairingResponse(active, capacity), nil
}

func (b *remoteCompanionBroker) persistProviderReclaimPending(ctx context.Context, pairingID string, providerPair remoteProviderPair) error {
	now := b.now()
	return b.withTx(ctx, func(tx *controlplaneent.Tx) error {
		if err := b.lockCapacityTx(ctx, tx, now); err != nil {
			return err
		}
		_, err := tx.RemotePairing.UpdateOneID(pairingID).
			SetState("provider_reclaim_pending").
			SetDesktopProviderUserID(providerPair.DesktopUserID).
			SetIosProviderUserID(providerPair.IOSUserID).
			SetDesktopProviderAbsent(false).
			SetIosProviderAbsent(false).
			Save(ctx)
		return err
	})
}

func remoteCredentialOperationID(pairingID, deviceID, idempotencyKey string) string {
	return remoteStableID("remote-credential", pairingID+"\x00"+deviceID+"\x00"+idempotencyKey)
}

func remoteCredentialRequestHash(pairingID, deviceID, role string) string {
	return remoteRequestHash(remoteProtocolVersion, pairingID, deviceID, role)
}

func decodeRemoteCredentialIssue(operation *controlplaneent.RuntimeOperation, operationID string) (remoteCredentialIssue, error) {
	if operation.OperationID != operationID || operation.Action != remoteCredentialOperationAction || operation.ResourceKind != "remote_device_credential" {
		return remoteCredentialIssue{}, errRemoteInvalidRequest
	}
	var issue remoteCredentialIssue
	if err := json.Unmarshal([]byte(operation.Result), &issue); err != nil {
		return remoteCredentialIssue{}, errRemoteInvalidRequest
	}
	issuedAt, issuedErr := time.Parse(time.RFC3339Nano, issue.IssuedAt)
	expiresAt, expiresErr := time.Parse(time.RFC3339Nano, issue.UserSigExpiresAt)
	if issuedErr != nil || expiresErr != nil || !expiresAt.Equal(issuedAt.Add(remoteUserSigTTL)) {
		return remoteCredentialIssue{}, errRemoteInvalidRequest
	}
	return issue, nil
}

func validateRemoteCredentialIssue(issue remoteCredentialIssue, pairing *controlplaneent.RemotePairing, device *controlplaneent.RemoteDeviceCredential, requestHash, idempotencyKey string, providerUserID, peerProviderUserID string, sdkAppID int64) error {
	if issue.PairingID != pairing.ID || issue.DeviceID != device.DeviceID || issue.Role != device.Role ||
		issue.RequestHash != requestHash || issue.IdempotencyKeyHash != remoteCredentialHash(idempotencyKey) ||
		issue.ProviderUserID != providerUserID || issue.PeerProviderUserID != peerProviderUserID || issue.SDKAppID != sdkAppID {
		return errRemoteInvalidRequest
	}
	return nil
}

func (b *remoteCompanionBroker) prepareRemoteCredentialIssue(ctx context.Context, pairing *controlplaneent.RemotePairing, device *controlplaneent.RemoteDeviceCredential, idempotencyKey string) (remoteCredentialIssue, error) {
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" {
		return remoteCredentialIssue{}, errRemoteInvalidRequest
	}
	providerUserID, peerProviderUserID, _, err := remoteProviderIdentity(pairing, device)
	if err != nil {
		return remoteCredentialIssue{}, err
	}
	if providerUserID == "" {
		return remoteCredentialIssue{}, errRemoteNotConfirmed
	}
	requestHash := remoteCredentialRequestHash(pairing.ID, device.DeviceID, device.Role)
	sdkAppID := remoteProviderSDKAppID(b.provider)
	operationID := remoteCredentialOperationID(pairing.ID, device.DeviceID, idempotencyKey)
	now := b.now().UTC().Truncate(time.Second)
	var issue remoteCredentialIssue
	err = b.withTx(ctx, func(tx *controlplaneent.Tx) error {
		currentPairing, pairingErr := tx.RemotePairing.Get(ctx, pairing.ID)
		if pairingErr != nil {
			return pairingErr
		}
		if currentPairing.State == "revoked" || currentPairing.SeatReleased {
			return errRemoteRevoked
		}
		if currentPairing.State != "active" {
			return errRemoteNotConfirmed
		}
		current, getErr := tx.RemoteDeviceCredential.Get(ctx, device.ID)
		if getErr != nil {
			return getErr
		}
		if current.PairingID != pairing.ID || current.DeviceID != device.DeviceID || current.Role != device.Role || current.Status != "active" {
			return errRemoteCredentialDenied
		}
		currentProviderUserID, currentPeerProviderUserID, _, identityErr := remoteProviderIdentity(currentPairing, current)
		if identityErr != nil || currentProviderUserID != providerUserID || currentPeerProviderUserID != peerProviderUserID {
			return errRemoteInvalidRequest
		}
		if existing, getErr := tx.RuntimeOperation.Get(ctx, operationID); getErr == nil {
			issue, err = decodeRemoteCredentialIssue(existing, operationID)
			if err != nil {
				return err
			}
			return validateRemoteCredentialIssue(issue, pairing, current, requestHash, idempotencyKey, providerUserID, peerProviderUserID, sdkAppID)
		} else if !controlplaneent.IsNotFound(getErr) {
			return getErr
		}

		issue = remoteCredentialIssue{
			PairingID:          pairing.ID,
			DeviceID:           device.DeviceID,
			Role:               device.Role,
			RequestHash:        requestHash,
			IdempotencyKeyHash: remoteCredentialHash(idempotencyKey),
			ProviderUserID:     providerUserID,
			PeerProviderUserID: peerProviderUserID,
			SDKAppID:           sdkAppID,
			IssuedAt:           now.Format(time.RFC3339Nano),
			UserSigExpiresAt:   now.Add(remoteUserSigTTL).Format(time.RFC3339Nano),
		}
		result, marshalErr := json.Marshal(issue)
		if marshalErr != nil {
			return marshalErr
		}
		_, err = tx.RuntimeOperation.Create().
			SetID(operationID).
			SetCreatedAt(now).
			SetUpdatedAt(now).
			SetOperationID(operationID).
			SetResourceID(pairing.ID).
			SetResourceKind("remote_device_credential").
			SetAction(remoteCredentialOperationAction).
			SetProvider("tencent_cloud_im").
			SetStatus("prepared").
			SetResult(string(result)).
			Save(ctx)
		return err
	})
	if err == nil {
		return issue, nil
	}
	// A concurrent request can win the stable operation ID between the query
	// and insert. Reload that winner and apply the same binding checks.
	if controlplaneent.IsConstraintError(err) {
		if existing, getErr := b.store.client.RuntimeOperation.Get(ctx, operationID); getErr == nil {
			issue, decodeErr := decodeRemoteCredentialIssue(existing, operationID)
			if decodeErr != nil {
				return remoteCredentialIssue{}, decodeErr
			}
			if validateErr := validateRemoteCredentialIssue(issue, pairing, device, requestHash, idempotencyKey, providerUserID, peerProviderUserID, sdkAppID); validateErr != nil {
				return remoteCredentialIssue{}, validateErr
			}
			return issue, nil
		}
	}
	return remoteCredentialIssue{}, err
}

func (b *remoteCompanionBroker) issueUserSigWithIdempotency(ctx context.Context, pairing *controlplaneent.RemotePairing, device *controlplaneent.RemoteDeviceCredential, idempotencyKey string) (remoteCredentialResponse, error) {
	issue, err := b.prepareRemoteCredentialIssue(ctx, pairing, device, idempotencyKey)
	if err != nil {
		return remoteCredentialResponse{}, err
	}
	issuedAt, err := time.Parse(time.RFC3339Nano, issue.IssuedAt)
	if err != nil {
		return remoteCredentialResponse{}, errRemoteInvalidRequest
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, issue.UserSigExpiresAt)
	if err != nil {
		return remoteCredentialResponse{}, errRemoteInvalidRequest
	}
	sig, providerExpires, err := b.provider.SignUserSig(ctx, issue.ProviderUserID, issuedAt, remoteUserSigTTL)
	if err != nil {
		return remoteCredentialResponse{}, err
	}
	if !providerExpires.UTC().Equal(expiresAt) {
		return remoteCredentialResponse{}, errRemoteProviderUnavailable
	}
	return remoteCredentialResponse{
		ProtocolVersion:    remoteProtocolVersion,
		PairingID:          issue.PairingID,
		DeviceID:           issue.DeviceID,
		Role:               issue.Role,
		Provider:           "tencent_cloud_im",
		SDKAppID:           issue.SDKAppID,
		PushBusinessID:     remoteProviderPushBusinessID(b.provider),
		ProviderUserID:     issue.ProviderUserID,
		PeerProviderUserID: issue.PeerProviderUserID,
		UserSig:            sig,
		UserSigExpiresAt:   issue.UserSigExpiresAt,
		State:              pairing.State,
	}, nil
}

func remoteProviderIdentity(pairing *controlplaneent.RemotePairing, device *controlplaneent.RemoteDeviceCredential) (providerUserID, peerProviderUserID, peerPublicKey string, err error) {
	switch device.Role {
	case "desktop":
		return pairing.DesktopProviderUserID, pairing.IosProviderUserID, pairing.IosPublicKey, nil
	case "ios":
		return pairing.IosProviderUserID, pairing.DesktopProviderUserID, pairing.DesktopPublicKey, nil
	default:
		return "", "", "", errRemoteCredentialDenied
	}
}

func (b *remoteCompanionBroker) revokePairing(ctx context.Context, pairingID, credential string) (remoteRevokeReceipt, error) {
	pairing, _, err := b.authenticateForRevoke(ctx, pairingID, credential)
	if err != nil {
		return remoteRevokeReceipt{}, err
	}
	if pairing.State == "revoked" || pairing.SeatReleased {
		return b.revokeReceipt(pairing, remoteProviderReclaimReceipt{
			ProviderUserID: pairing.DesktopProviderUserID,
			Absent:         pairing.DesktopProviderAbsent,
		}, remoteProviderReclaimReceipt{
			ProviderUserID: pairing.IosProviderUserID,
			Absent:         pairing.IosProviderAbsent,
		}), nil
	}
	now := b.now()
	err = b.withTx(ctx, func(tx *controlplaneent.Tx) error {
		if err := b.lockCapacityTx(ctx, tx, now); err != nil {
			return err
		}
		current, err := tx.RemotePairing.Get(ctx, pairingID)
		if err != nil {
			return err
		}
		if current.State == "revoked" || current.SeatReleased {
			return errRemoteRevoked
		}
		update := tx.RemotePairing.UpdateOneID(pairingID).SetState("revoking")
		if current.RevocationReceiptID == "" {
			receiptID := remoteStableID("rev", pairingID)
			receiptToken := b.stableToken("revocation-receipt:"+receiptID, pairingID)
			digest, digestErr := b.tokenDigest(receiptToken)
			if digestErr != nil {
				return digestErr
			}
			update.SetRevocationReceiptID(receiptID).
				SetRevocationReceiptHash(digest.hash).
				SetRevocationReceiptSalt(digest.salt)
		}
		_, err = update.Save(ctx)
		return err
	})
	if err != nil {
		return remoteRevokeReceipt{}, err
	}

	desktopAbsent := pairing.DesktopProviderAbsent || pairing.DesktopProviderUserID == ""
	iosAbsent := pairing.IosProviderAbsent || pairing.IosProviderUserID == ""
	var desktopErr error
	var iosErr error
	if !desktopAbsent {
		desktopAbsent, desktopErr = b.reclaimProviderUser(ctx, pairing.DesktopProviderUserID)
	}
	if !iosAbsent {
		iosAbsent, iosErr = b.reclaimProviderUser(ctx, pairing.IosProviderUserID)
	}
	now = b.now()
	var final *controlplaneent.RemotePairing
	err = b.withTx(ctx, func(tx *controlplaneent.Tx) error {
		if err := b.lockCapacityTx(ctx, tx, now); err != nil {
			return err
		}
		current, err := tx.RemotePairing.Get(ctx, pairingID)
		if err != nil {
			return err
		}
		desktopAbsent = desktopAbsent || current.DesktopProviderAbsent || current.DesktopProviderUserID == ""
		iosAbsent = iosAbsent || current.IosProviderAbsent || current.IosProviderUserID == ""
		update := tx.RemotePairing.UpdateOneID(pairingID).
			SetDesktopProviderAbsent(desktopAbsent).
			SetIosProviderAbsent(iosAbsent)
		if desktopAbsent && iosAbsent {
			update.SetState("revoked").SetRevokedAt(now.Format(time.RFC3339Nano))
			if !current.SeatReleased {
				update.SetSeatReleased(true)
			}
		} else {
			update.SetState("provider_reclaim_pending")
		}
		final, err = update.Save(ctx)
		if err != nil {
			return err
		}
		if desktopAbsent && iosAbsent {
			if !current.SeatReleased {
				if _, err := tx.RemoteSeatCapacity.UpdateOneID(remoteCapacityID).AddSeatCount(-1).SetUpdatedAt(now).Save(ctx); err != nil {
					return err
				}
			}
			devices, err := tx.RemoteDeviceCredential.Query().Where(remotedevicecredential.PairingIDEQ(pairingID)).All(ctx)
			if err != nil {
				return err
			}
			for _, device := range devices {
				if _, err := tx.RemoteDeviceCredential.UpdateOneID(device.ID).SetStatus("revoked").SetRevokedAt(now.Format(time.RFC3339Nano)).Save(ctx); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return remoteRevokeReceipt{}, err
	}
	return b.revokeReceipt(final, remoteProviderReclaimReceipt{
		ProviderUserID: pairing.DesktopProviderUserID,
		Absent:         final.DesktopProviderAbsent,
		Error:          remoteReclaimErrorCode(desktopErr, final.DesktopProviderAbsent),
	}, remoteProviderReclaimReceipt{
		ProviderUserID: pairing.IosProviderUserID,
		Absent:         final.IosProviderAbsent,
		Error:          remoteReclaimErrorCode(iosErr, final.IosProviderAbsent),
	}), nil
}

func (b *remoteCompanionBroker) authenticateForRevoke(ctx context.Context, pairingID, credential string) (*controlplaneent.RemotePairing, *controlplaneent.RemoteDeviceCredential, error) {
	pairing, device, err := b.authenticate(ctx, pairingID, credential)
	if err == nil {
		return pairing, device, nil
	}
	if !errors.Is(err, errRemoteRevoked) {
		return nil, nil, err
	}
	pairing, getErr := b.store.client.RemotePairing.Get(ctx, pairingID)
	if getErr != nil {
		if controlplaneent.IsNotFound(getErr) {
			return nil, nil, errRemoteCredentialDenied
		}
		return nil, nil, getErr
	}
	credentials, getErr := b.store.client.RemoteDeviceCredential.Query().Where(remotedevicecredential.PairingIDEQ(pairingID)).All(ctx)
	if getErr != nil {
		return nil, nil, getErr
	}
	want := remoteCredentialHash(credential)
	for _, candidate := range credentials {
		if subtle.ConstantTimeCompare([]byte(candidate.CredentialHash), []byte(want)) == 1 {
			return pairing, candidate, nil
		}
	}
	return nil, nil, errRemoteCredentialDenied
}

func remoteReclaimErrorCode(err error, absent bool) string {
	if err != nil {
		return remoteErrorCode(err)
	}
	if !absent {
		return errRemoteProviderUnavailable.Error()
	}
	return ""
}

func (b *remoteCompanionBroker) revokeReceipt(pairing *controlplaneent.RemotePairing, desktop, ios remoteProviderReclaimReceipt) remoteRevokeReceipt {
	return remoteRevokeReceipt{
		ProtocolVersion:        remoteProtocolVersion,
		PairingID:              pairing.ID,
		State:                  pairing.State,
		RevocationReceiptID:    pairing.RevocationReceiptID,
		RevocationReceiptToken: b.stableToken("revocation-receipt:"+pairing.RevocationReceiptID, pairing.ID),
		DesktopProviderAbsent:  pairing.DesktopProviderAbsent,
		IOSProviderAbsent:      pairing.IosProviderAbsent,
		SeatReleased:           pairing.SeatReleased,
		RevokedAt:              pairing.RevokedAt,
		Desktop:                desktop,
		IOS:                    ios,
	}
}

func (b *remoteCompanionBroker) readRevocation(ctx context.Context, receiptID, receiptToken string) (remoteRevocationReadResponse, error) {
	if !validRemoteID(receiptID, "rev-") || strings.TrimSpace(receiptToken) == "" {
		return remoteRevocationReadResponse{}, errRemoteCredentialDenied
	}
	pairing, err := b.store.client.RemotePairing.Query().Where(remotepairing.RevocationReceiptIDEQ(receiptID)).Only(ctx)
	if controlplaneent.IsNotFound(err) {
		return remoteRevocationReadResponse{}, errRemoteCredentialDenied
	}
	if err != nil {
		return remoteRevocationReadResponse{}, err
	}
	if !b.tokenMatches(pairing.RevocationReceiptHash, pairing.RevocationReceiptSalt, receiptToken) {
		return remoteRevocationReadResponse{}, errRemoteCredentialDenied
	}
	return remoteRevocationReadResponse{
		ProtocolVersion:               remoteProtocolVersion,
		PairingID:                     pairing.ID,
		State:                         pairing.State,
		DesktopProviderIdentityAbsent: pairing.DesktopProviderAbsent,
		IOSProviderIdentityAbsent:     pairing.IosProviderAbsent,
		SeatReleased:                  pairing.SeatReleased,
	}, nil
}

func (b *remoteCompanionBroker) reclaimProviderUser(ctx context.Context, providerID string) (bool, error) {
	if providerID == "" {
		return true, nil
	}
	var firstErr error
	if err := b.provider.KickUser(ctx, providerID); err != nil {
		firstErr = err
	}
	if err := b.provider.DeleteUser(ctx, providerID); err != nil && firstErr == nil {
		firstErr = err
	}
	absent, err := b.provider.UserAbsent(ctx, providerID)
	if err != nil && firstErr == nil {
		firstErr = err
	}
	return absent, firstErr
}

func (b *remoteCompanionBroker) capacity(ctx context.Context) (*controlplaneent.RemoteSeatCapacity, error) {
	return b.store.client.RemoteSeatCapacity.Get(ctx, remoteCapacityID)
}

func (b *remoteCompanionBroker) pairingResponse(pairing *controlplaneent.RemotePairing, capacity *controlplaneent.RemoteSeatCapacity) remotePairingResponse {
	return remotePairingResponse{
		ProtocolVersion:       remoteProtocolVersion,
		PairingID:             pairing.ID,
		State:                 pairing.State,
		DesktopPublicKey:      pairing.DesktopPublicKey,
		IOSPublicKey:          pairing.IosPublicKey,
		AuthenticationString:  pairing.Sas,
		ExpiresAt:             pairing.ExpiresAt,
		ReservationExpiresAt:  pairing.ReservationExpiresAt,
		DesktopProviderUserID: pairing.DesktopProviderUserID,
		IOSProviderUserID:     pairing.IosProviderUserID,
		Seat: remoteSeatResponse{
			Count:   capacity.SeatCount,
			Limit:   capacity.SeatLimit,
			Warning: capacity.SeatCount >= capacity.WarningThreshold,
		},
		SeatCount:   capacity.SeatCount,
		SeatLimit:   capacity.SeatLimit,
		SeatWarning: capacity.SeatCount >= capacity.WarningThreshold,
	}
}

func remoteSecret() (string, error) {
	raw, err := remoteRandomBytes(remoteTokenBytes)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func remoteRandomBytes(size int) ([]byte, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return nil, err
	}
	return value, nil
}

func remoteID(prefix string) (string, error) {
	secret, err := remoteSecret()
	if err != nil {
		return "", err
	}
	return prefix + "-" + secret, nil
}

func remoteManualCode() (string, error) {
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	raw, err := remoteRandomBytes(12)
	if err != nil {
		return "", err
	}
	var result strings.Builder
	result.Grow(12)
	for _, value := range raw {
		result.WriteByte(alphabet[int(value)&31])
	}
	return result.String(), nil
}

func (b *remoteCompanionBroker) stableManualCode(pairingID, idempotencyKey string) string {
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	raw := remoteHMACDigest(b.hashKey, []byte("manual-code:"+pairingID), []byte(idempotencyKey))
	var result strings.Builder
	result.Grow(12)
	for _, value := range raw[:12] {
		result.WriteByte(alphabet[int(value)&31])
	}
	return result.String()
}

func remoteSAS(pairingID, desktopPublicKey, iosPublicKey string) string {
	serialize := func(name, value string) string {
		return fmt.Sprintf("%s=%d:%s", name, len([]byte(value)), value)
	}
	input := strings.Join([]string{
		serialize("pair_id", pairingID),
		serialize("desktop_public_key", desktopPublicKey),
		serialize("ios_public_key", iosPublicKey),
	}, "|")
	sum := sha256.Sum256([]byte(input))
	value := binary.BigEndian.Uint32(sum[:4]) % 1_000_000
	return fmt.Sprintf("%03d %03d", value/1_000, value%1_000)
}

func remoteBrokerURL() string {
	return strings.TrimRight(strings.TrimSpace(os.Getenv("OPL_REMOTE_COMPANION_BROKER_URL")), "/")
}

func remoteProviderSDKAppID(provider remoteCompanionProvider) int64 {
	if tencent, ok := provider.(*tencentRemoteCompanionProvider); ok {
		return tencent.config.SDKAppID
	}
	return 0
}

func remoteProviderPushBusinessID(provider remoteCompanionProvider) int64 {
	if tencent, ok := provider.(*tencentRemoteCompanionProvider); ok {
		return tencent.config.PushBusinessID
	}
	return 0
}

func validateRemotePublicKey(value string) error {
	if value == "" || len(value) > 512 {
		return errRemoteInvalidPublicKey
	}
	for _, char := range value {
		if char < 0x21 || char > 0x7e {
			return errRemoteInvalidPublicKey
		}
	}
	return nil
}

func validRemoteID(value, prefix string) bool {
	if !strings.HasPrefix(value, prefix) || len(value) > 160 {
		return false
	}
	for _, char := range value {
		if !(char == '-' || char == '_' || char >= '0' && char <= '9' || char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z') {
			return false
		}
	}
	return true
}

func remoteErrorStatus(err error) int {
	switch {
	case errors.Is(err, errRemoteCredentialDenied):
		return http.StatusUnauthorized
	case errors.Is(err, errRemoteRevoked):
		return http.StatusGone
	case errors.Is(err, errRemoteExpiredInvite), errors.Is(err, errRemoteClaimExpired):
		return http.StatusGone
	case errors.Is(err, errRemoteCapacityUnavailable), errors.Is(err, errRemoteClaimMismatch), errors.Is(err, errRemoteClaimAttempts), errors.Is(err, errRemoteNotConfirmed), errors.Is(err, errRemoteAuthentication):
		return http.StatusConflict
	case errors.Is(err, errRemoteProviderUnavailable), errors.Is(err, errRemoteProviderNotConfig):
		return http.StatusServiceUnavailable
	case errors.Is(err, errRemoteInvalidInvite):
		return http.StatusBadRequest
	case errors.Is(err, errRemoteInvalidPublicKey):
		return http.StatusBadRequest
	case errors.Is(err, errRemoteProtocolVersion):
		return http.StatusBadRequest
	case errors.Is(err, errRemoteInvalidJSON):
		return http.StatusBadRequest
	case errors.Is(err, errRemoteInvalidRequest):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

func remoteErrorCode(err error) string {
	switch {
	case errors.Is(err, errRemoteProviderNotConfig):
		return "provider_unavailable"
	case errors.Is(err, errRemoteProviderUnavailable):
		return "provider_unavailable"
	case errors.Is(err, errRemoteCredentialDenied):
		return "credential_invalid"
	case errors.Is(err, errRemoteRevoked):
		return "pair_revoked"
	case errors.Is(err, errRemoteExpiredInvite):
		return "invitation_expired"
	case errors.Is(err, errRemoteInvalidInvite):
		return "invitation_invalid"
	case errors.Is(err, errRemoteClaimExpired):
		return "pairing_expired"
	case errors.Is(err, errRemoteClaimAttempts):
		return "claim_attempts_exhausted"
	case errors.Is(err, errRemoteClaimMismatch):
		return "claim_invalid"
	case errors.Is(err, errRemoteNotConfirmed):
		return "pairing_not_ready"
	case errors.Is(err, errRemoteCapacityUnavailable):
		return "capacity_unavailable"
	case errors.Is(err, errRemoteInvalidPublicKey):
		return "invalid_request"
	case errors.Is(err, errRemoteAuthentication):
		return "authentication_mismatch"
	case errors.Is(err, errRemoteProtocolVersion):
		return "unsupported_protocol"
	case errors.Is(err, errRemoteInvalidJSON):
		return "invalid_request"
	case errors.Is(err, errRemoteInvalidRequest):
		return "invalid_request"
	default:
		return "internal_error"
	}
}

func remoteErrorRetryable(err error) bool {
	return errors.Is(err, errRemoteProviderUnavailable) || errors.Is(err, errRemoteProviderNotConfig) || errors.Is(err, errRemoteCapacityUnavailable)
}

func writeRemoteError(w http.ResponseWriter, err error) {
	requestID := strings.TrimSpace(w.Header().Get("X-Request-ID"))
	if requestID == "" {
		requestID, _ = remoteID("req")
		w.Header().Set("X-Request-ID", requestID)
	}
	writeJSON(w, remoteErrorStatus(err), map[string]any{
		"protocol_version": remoteProtocolVersion,
		"error_code":       remoteErrorCode(err),
		"retryable":        remoteErrorRetryable(err),
		"request_id":       requestID,
	})
}

func requireRemoteMutationKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" || len(key) > 256 {
		writeRemoteError(w, errRemoteInvalidRequest)
		return "", false
	}
	return key, true
}

func requireRemoteProtocol(w http.ResponseWriter, version string) bool {
	if version != remoteProtocolVersion {
		writeRemoteError(w, errRemoteProtocolVersion)
		return false
	}
	return true
}

func remoteAuthCredential(r *http.Request) string {
	value := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(value) < 8 || !strings.EqualFold(value[:7], "Bearer ") {
		return ""
	}
	value = strings.TrimSpace(value[7:])
	if len(value) > 256 {
		return ""
	}
	return value
}

func decodeRemoteJSON(w http.ResponseWriter, r *http.Request, destination any) bool {
	if !limitJSONBody(w, r) {
		return false
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		writeRemoteError(w, errRemoteInvalidJSON)
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		writeRemoteError(w, errRemoteInvalidJSON)
		return false
	}
	return true
}

type remoteInviteRequest struct {
	ProtocolVersion string `json:"protocol_version"`
	Label           string `json:"label"`
	ExpiresAt       string `json:"expires_at"`
}
type remotePairingRequest struct {
	ProtocolVersion    string `json:"protocol_version"`
	InvitationCode     string `json:"invitation_code"`
	DesktopDeviceID    string `json:"desktop_device_id"`
	DesktopDeviceLabel string `json:"desktop_device_label"`
	DesktopPublicKey   string `json:"desktop_public_key"`
}
type remoteClaimRequest struct {
	ProtocolVersion         string `json:"protocol_version"`
	PairingID               string `json:"pairing_id"`
	ClaimSecretOrManualCode string `json:"claim_secret_or_manual_code"`
	IOSDeviceID             string `json:"ios_device_id"`
	IOSDeviceLabel          string `json:"ios_device_label"`
	IOSPublicKey            string `json:"ios_public_key"`
}
type remoteConfirmRequest struct {
	ProtocolVersion      string `json:"protocol_version"`
	AuthenticationString string `json:"authentication_string"`
}
type remoteCredentialRequest struct {
	ProtocolVersion string `json:"protocol_version"`
	DeviceID        string `json:"device_id"`
}
type remoteDeleteRequest struct {
	ProtocolVersion string `json:"protocol_version"`
}

func registerRemoteCompanionRoutes(mux *http.ServeMux, app *controlPlaneServer) {
	mux.HandleFunc("POST "+remoteCompanionBasePath+"/invitations", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		if app.remoteCompanion == nil {
			writeRemoteError(w, errRemoteProviderNotConfig)
			return
		}
		idempotencyKey, ok := requireRemoteMutationKey(w, r)
		if !ok {
			return
		}
		var input remoteInviteRequest
		if !decodeRemoteJSON(w, r, &input) {
			return
		}
		if !requireRemoteProtocol(w, input.ProtocolVersion) {
			return
		}
		user, _ := app.sessionUserContext(r)
		created, err := app.remoteCompanion.createInviteWithOptions(r.Context(), stringValue(user["id"]), input.Label, input.ExpiresAt, idempotencyKey)
		if err != nil {
			writeRemoteError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, remoteInviteHTTPResponse{
			InvitationCode: created.Secret,
			ExpiresAt:      created.ExpiresAt,
		})
	}))

	mux.HandleFunc("POST "+remoteCompanionBasePath+"/pairings", func(w http.ResponseWriter, r *http.Request) {
		if app.remoteCompanion == nil {
			writeRemoteError(w, errRemoteProviderNotConfig)
			return
		}
		idempotencyKey, ok := requireRemoteMutationKey(w, r)
		if !ok {
			return
		}
		var input remotePairingRequest
		if !decodeRemoteJSON(w, r, &input) {
			return
		}
		if !requireRemoteProtocol(w, input.ProtocolVersion) {
			return
		}
		created, err := app.remoteCompanion.createPairingWithOptions(r.Context(), input.InvitationCode, input.DesktopDeviceID, input.DesktopDeviceLabel, input.DesktopPublicKey, idempotencyKey)
		if err != nil {
			writeRemoteError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, remoteCreateHTTPResponse{
			ProtocolVersion:  created.ProtocolVersion,
			PairingID:        created.PairingID,
			DesktopPairToken: created.DesktopCredential,
			ClaimSecret:      created.ClaimSecret,
			ManualCode:       created.ManualCode,
			ExpiresAt:        created.ExpiresAt,
			BrokerURL:        created.BrokerURL,
		})
	})

	mux.HandleFunc("POST "+remoteCompanionBasePath+"/pairings/claim", func(w http.ResponseWriter, r *http.Request) {
		if app.remoteCompanion == nil {
			writeRemoteError(w, errRemoteProviderNotConfig)
			return
		}
		idempotencyKey, ok := requireRemoteMutationKey(w, r)
		if !ok {
			return
		}
		var input remoteClaimRequest
		if !decodeRemoteJSON(w, r, &input) {
			return
		}
		if !requireRemoteProtocol(w, input.ProtocolVersion) {
			return
		}
		claimValue := strings.TrimSpace(input.ClaimSecretOrManualCode)
		claimSecret, manualCode := claimValue, ""
		if len(claimValue) == 12 {
			claimSecret, manualCode = "", claimValue
		}
		claimed, err := app.remoteCompanion.claimPairingWithOptions(r.Context(), input.PairingID, claimSecret, manualCode, input.IOSDeviceID, input.IOSDeviceLabel, input.IOSPublicKey, idempotencyKey)
		if err != nil {
			writeRemoteError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, remoteClaimHTTPResponse{
			ProtocolVersion:      claimed.ProtocolVersion,
			PairingID:            claimed.PairingID,
			IOSClaimToken:        claimed.IOSCredential,
			State:                claimed.State,
			AuthenticationString: claimed.AuthenticationString,
			ExpiresAt:            claimed.ExpiresAt,
		})
	})

	mux.HandleFunc("GET "+remoteCompanionBasePath+"/pairings/{pairingId}", func(w http.ResponseWriter, r *http.Request) {
		if app.remoteCompanion == nil {
			writeRemoteError(w, errRemoteProviderNotConfig)
			return
		}
		claim, err := app.remoteCompanion.readClaim(r.Context(), r.PathValue("pairingId"), remoteAuthCredential(r))
		if err != nil {
			writeRemoteError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, claim)
	})

	mux.HandleFunc("POST "+remoteCompanionBasePath+"/pairings/{pairingId}/confirm", func(w http.ResponseWriter, r *http.Request) {
		if app.remoteCompanion == nil {
			writeRemoteError(w, errRemoteProviderNotConfig)
			return
		}
		if _, ok := requireRemoteMutationKey(w, r); !ok {
			return
		}
		var input remoteConfirmRequest
		if !decodeRemoteJSON(w, r, &input) {
			return
		}
		if !requireRemoteProtocol(w, input.ProtocolVersion) {
			return
		}
		confirmed, err := app.remoteCompanion.confirmPairing(r.Context(), r.PathValue("pairingId"), remoteAuthCredential(r), input.AuthenticationString)
		if err != nil {
			writeRemoteError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, remoteConfirmHTTPResponse{
			ProtocolVersion: confirmed.ProtocolVersion,
			PairingID:       confirmed.PairingID,
			State:           confirmed.State,
		})
	})

	mux.HandleFunc("POST "+remoteCompanionBasePath+"/pairings/{pairingId}/credentials", func(w http.ResponseWriter, r *http.Request) {
		if app.remoteCompanion == nil {
			writeRemoteError(w, errRemoteProviderNotConfig)
			return
		}
		idempotencyKey, ok := requireRemoteMutationKey(w, r)
		if !ok {
			return
		}
		var input remoteCredentialRequest
		if !decodeRemoteJSON(w, r, &input) {
			return
		}
		if !requireRemoteProtocol(w, input.ProtocolVersion) {
			return
		}
		pairing, device, err := app.remoteCompanion.authenticateDevice(r.Context(), r.PathValue("pairingId"), remoteAuthCredential(r), input.DeviceID)
		if err != nil {
			writeRemoteError(w, err)
			return
		}
		if pairing.State != "active" {
			writeRemoteError(w, errRemoteNotConfirmed)
			return
		}
		credentials, err := app.remoteCompanion.issueUserSigWithIdempotency(r.Context(), pairing, device, idempotencyKey)
		if err != nil {
			writeRemoteError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, remoteCredentialHTTPResponse{
			ProtocolVersion:    credentials.ProtocolVersion,
			Provider:           credentials.Provider,
			SDKAppID:           credentials.SDKAppID,
			PushBusinessID:     credentials.PushBusinessID,
			ProviderUserID:     credentials.ProviderUserID,
			PeerProviderUserID: credentials.PeerProviderUserID,
			UserSig:            credentials.UserSig,
			UserSigExpiresAt:   credentials.UserSigExpiresAt,
		})
	})

	mux.HandleFunc("DELETE "+remoteCompanionBasePath+"/pairings/{pairingId}", func(w http.ResponseWriter, r *http.Request) {
		if app.remoteCompanion == nil {
			writeRemoteError(w, errRemoteProviderNotConfig)
			return
		}
		if _, ok := requireRemoteMutationKey(w, r); !ok {
			return
		}
		revoked, err := app.remoteCompanion.revokePairing(r.Context(), r.PathValue("pairingId"), remoteAuthCredential(r))
		if err != nil {
			writeRemoteError(w, err)
			return
		}
		status := http.StatusOK
		if !revoked.SeatReleased {
			status = http.StatusAccepted
		}
		writeJSON(w, status, remoteRevokeHTTPResponse{
			ProtocolVersion:        revoked.ProtocolVersion,
			PairingID:              revoked.PairingID,
			State:                  revoked.State,
			RevocationReceiptID:    revoked.RevocationReceiptID,
			RevocationReceiptToken: revoked.RevocationReceiptToken,
		})
	})

	mux.HandleFunc("GET "+remoteCompanionBasePath+"/revocations/{receiptId}", func(w http.ResponseWriter, r *http.Request) {
		if app.remoteCompanion == nil {
			writeRemoteError(w, errRemoteProviderNotConfig)
			return
		}
		readback, err := app.remoteCompanion.readRevocation(r.Context(), r.PathValue("receiptId"), remoteAuthCredential(r))
		if err != nil {
			writeRemoteError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, readback)
	})
}
