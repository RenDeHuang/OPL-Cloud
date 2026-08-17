package server

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type tencentRemoteCompanionConfig struct {
	SDKAppID        int64
	PushBusinessID  int64
	AdminIdentifier string
	Secret          string
	BaseURL         string
	Configured      bool
}

type tencentRemoteCompanionProvider struct {
	config tencentRemoteCompanionConfig
	client *http.Client
}

const maxTencentAPNSBusinessID = int64(1<<31 - 1)

func newTencentRemoteCompanionProviderFromEnv() remoteCompanionProvider {
	sdkAppID, parseErr := strconv.ParseInt(strings.TrimSpace(getenvNonSecret("OPL_TENCENT_IM_SDK_APP_ID")), 10, 64)
	pushBusinessID, pushParseErr := strconv.ParseInt(strings.TrimSpace(getenvNonSecret("OPL_TENCENT_IM_APNS_BUSINESS_ID")), 10, 64)
	if pushParseErr != nil || pushBusinessID <= 0 || pushBusinessID > maxTencentAPNSBusinessID {
		pushBusinessID = 0
	}
	baseURL := strings.TrimRight(strings.TrimSpace(getenvNonSecret("OPL_TENCENT_IM_BASE_URL")), "/")
	admin := strings.TrimSpace(getenvNonSecret("OPL_TENCENT_IM_ADMIN_IDENTIFIER"))
	secret := getenvNonSecret("OPL_TENCENT_IM_SECRET")
	configured := parseErr == nil && sdkAppID > 0 && admin != "" && secret != "" && baseURL != ""
	return &tencentRemoteCompanionProvider{
		config: tencentRemoteCompanionConfig{SDKAppID: sdkAppID, PushBusinessID: pushBusinessID, AdminIdentifier: admin, Secret: secret, BaseURL: baseURL, Configured: configured},
		client: &http.Client{Timeout: 5 * time.Second},
	}
}

// getenvNonSecret keeps provider configuration lookup in one place. The value
// is never included in an error or log message.
func getenvNonSecret(key string) string {
	return strings.TrimSpace(os.Getenv(key))
}

func (p *tencentRemoteCompanionProvider) configured() error {
	if !p.config.Configured {
		return errRemoteProviderNotConfig
	}
	return nil
}

func (p *tencentRemoteCompanionProvider) ProvisionPair(ctx context.Context, pairingID string) (remoteProviderPair, error) {
	users := remoteProviderPair{DesktopUserID: tencentPairUserID(pairingID, "desktop"), IOSUserID: tencentPairUserID(pairingID, "ios")}
	if err := p.configured(); err != nil {
		return users, err
	}
	if err := p.importUser(ctx, users.DesktopUserID); err != nil {
		return users, err
	}
	if err := p.importUser(ctx, users.IOSUserID); err != nil {
		return users, err
	}
	for _, userID := range []string{users.DesktopUserID, users.IOSUserID} {
		absent, err := p.UserAbsent(ctx, userID)
		if err != nil || absent {
			return users, errRemoteProviderUnavailable
		}
	}
	return users, nil
}

func (p *tencentRemoteCompanionProvider) KickUser(ctx context.Context, userID string) error {
	if err := p.configured(); err != nil {
		return err
	}
	_, err := p.post(ctx, "/v4/im_open_login_svc/kick", map[string]any{"UserID": userID})
	return err
}

func (p *tencentRemoteCompanionProvider) DeleteUser(ctx context.Context, userID string) error {
	if err := p.configured(); err != nil {
		return err
	}
	_, err := p.post(ctx, "/v4/im_open_login_svc/account_delete", map[string]any{"DeleteItem": []map[string]string{{"UserID": userID}}})
	return err
}

func (p *tencentRemoteCompanionProvider) UserAbsent(ctx context.Context, userID string) (bool, error) {
	if err := p.configured(); err != nil {
		return false, err
	}
	payload, err := p.post(ctx, "/v4/im_open_login_svc/account_check", map[string]any{"CheckItem": []map[string]string{{"UserID": userID}}})
	if err != nil {
		return false, err
	}
	items, ok := payload["CheckItem"].([]any)
	if !ok || len(items) != 1 {
		return false, errRemoteProviderUnavailable
	}
	item, ok := items[0].(map[string]any)
	if !ok {
		return false, errRemoteProviderUnavailable
	}
	status, _ := item["AccountStatus"].(string)
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "notimported":
		return true, nil
	case "imported":
		return false, nil
	default:
		return false, errRemoteProviderUnavailable
	}
}

func (p *tencentRemoteCompanionProvider) SignUserSig(ctx context.Context, userID string, now time.Time, ttl time.Duration) (string, time.Time, error) {
	if err := p.configured(); err != nil {
		return "", time.Time{}, err
	}
	if strings.TrimSpace(userID) == "" || ttl <= 0 {
		return "", time.Time{}, errRemoteProviderUnavailable
	}
	expires := now.UTC().Add(ttl)
	sig, err := tencentUserSig(p.config.Secret, p.config.SDKAppID, userID, now.UTC(), ttl)
	if err != nil {
		return "", time.Time{}, errRemoteProviderUnavailable
	}
	return sig, expires, nil
}

func (p *tencentRemoteCompanionProvider) importUser(ctx context.Context, userID string) error {
	_, err := p.post(ctx, "/v4/im_open_login_svc/account_import", map[string]any{"UserID": userID})
	return err
}

func (p *tencentRemoteCompanionProvider) post(ctx context.Context, path string, payload any) (map[string]any, error) {
	if err := p.configured(); err != nil {
		return nil, err
	}
	endpoint, err := url.Parse(p.config.BaseURL + path)
	if err != nil {
		return nil, errRemoteProviderUnavailable
	}
	now := time.Now().UTC()
	query := endpoint.Query()
	query.Set("sdkappid", strconv.FormatInt(p.config.SDKAppID, 10))
	query.Set("identifier", p.config.AdminIdentifier)
	userSig, err := tencentUserSig(p.config.Secret, p.config.SDKAppID, p.config.AdminIdentifier, now, 5*time.Minute)
	if err != nil {
		return nil, errRemoteProviderUnavailable
	}
	query.Set("usersig", userSig)
	query.Set("random", strconv.FormatInt(now.UnixNano()&0x7fffffff, 10))
	query.Set("contenttype", "json")
	endpoint.RawQuery = query.Encode()
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, errRemoteProviderUnavailable
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return nil, errRemoteProviderUnavailable
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := p.client.Do(request)
	if err != nil {
		return nil, errRemoteProviderUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1024))
		return nil, errRemoteProviderUnavailable
	}
	var result map[string]any
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil {
		return nil, errRemoteProviderUnavailable
	}
	if code, ok := result["ErrorCode"]; ok && !remoteProviderSuccessCode(code) {
		return nil, errRemoteProviderUnavailable
	}
	if code, ok := result["ActionStatus"]; ok {
		if status, _ := code.(string); status != "OK" && status != "" {
			return nil, errRemoteProviderUnavailable
		}
	}
	return result, nil
}

func remoteProviderSuccessCode(value any) bool {
	switch code := value.(type) {
	case float64:
		return code == 0
	case int:
		return code == 0
	case string:
		return code == "0"
	default:
		return false
	}
}

func tencentPairUserID(pairingID, role string) string {
	sum := sha256.Sum256([]byte("opl-link:" + pairingID + ":" + role))
	return "opl-link-" + role + "-" + hex.EncodeToString(sum[:12])
}

func tencentUserSig(secret string, sdkAppID int64, userID string, now time.Time, ttl time.Duration) (string, error) {
	expire := int64(ttl / time.Second)
	if strings.TrimSpace(secret) == "" || sdkAppID <= 0 || strings.TrimSpace(userID) == "" || expire <= 0 {
		return "", errRemoteProviderNotConfig
	}
	now = now.UTC()
	message := fmt.Sprintf("TLS.identifier:%s\nTLS.sdkappid:%d\nTLS.time:%d\nTLS.expire:%d\n", userID, sdkAppID, now.UTC().Unix(), expire)
	mac := hmac.New(sha256.New, []byte(secret))
	if _, err := mac.Write([]byte(message)); err != nil {
		return "", err
	}
	doc := map[string]any{
		"TLS.ver":        "2.0",
		"TLS.identifier": userID,
		"TLS.sdkappid":   sdkAppID,
		"TLS.expire":     expire,
		"TLS.time":       now.Unix(),
		"TLS.sig":        base64.StdEncoding.EncodeToString(mac.Sum(nil)),
	}
	plain, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	if _, err := writer.Write(plain); err != nil {
		_ = writer.Close()
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}
	encoded := base64.StdEncoding.EncodeToString(compressed.Bytes())
	encoded = strings.NewReplacer("+", "*", "/", "-", "=", "_").Replace(encoded)
	return encoded, nil
}

// fakeRemoteCompanionProvider is deterministic and is intentionally never
// selected by production configuration. Tests can inject it through the
// server constructor to exercise the full broker path without Tencent calls.
type fakeRemoteCompanionProvider struct {
	mu                        sync.Mutex
	users                     map[string]bool
	failProvision             bool
	failProvisionAfterDesktop bool
	failDeleteFor             map[string]bool
	secret                    string
}

func newFakeRemoteCompanionProvider() *fakeRemoteCompanionProvider {
	return &fakeRemoteCompanionProvider{users: map[string]bool{}, failDeleteFor: map[string]bool{}, secret: "opl-link-test-provider"}
}

func (p *fakeRemoteCompanionProvider) ProvisionPair(_ context.Context, pairingID string) (remoteProviderPair, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pair := remoteProviderPair{DesktopUserID: tencentPairUserID(pairingID, "desktop"), IOSUserID: tencentPairUserID(pairingID, "ios")}
	if p.failProvision {
		return pair, errRemoteProviderUnavailable
	}
	p.users[pair.DesktopUserID] = true
	if p.failProvisionAfterDesktop {
		return pair, errRemoteProviderUnavailable
	}
	p.users[pair.IOSUserID] = true
	return pair, nil
}

func (p *fakeRemoteCompanionProvider) KickUser(_ context.Context, _ string) error { return nil }

func (p *fakeRemoteCompanionProvider) DeleteUser(_ context.Context, userID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failDeleteFor[userID] {
		return errRemoteProviderUnavailable
	}
	delete(p.users, userID)
	return nil
}

func (p *fakeRemoteCompanionProvider) UserAbsent(_ context.Context, userID string) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.users[userID], nil
}

func (p *fakeRemoteCompanionProvider) SignUserSig(_ context.Context, userID string, now time.Time, ttl time.Duration) (string, time.Time, error) {
	if userID == "" || ttl <= 0 {
		return "", time.Time{}, errors.New("fake_provider_invalid_user")
	}
	sig, err := tencentUserSig(p.secret, 1, userID, now, ttl)
	if err != nil {
		return "", time.Time{}, err
	}
	return sig, now.UTC().Add(ttl), nil
}
