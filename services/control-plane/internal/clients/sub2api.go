package clients

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxSub2APIResponseBytes  = 1 << 20
	maxSub2APIRequestTimeout = 30 * time.Second
)

var (
	ErrSub2APIUnsupportedVersion = errors.New("sub2api unsupported version")
	ErrSub2APIChargeConflict     = errors.New("sub2api charge conflict")
	ErrSub2APIChargeUnknown      = errors.New("sub2api charge result unknown")
	ErrSub2APIResponseTooLarge   = errors.New("sub2api response too large")
)

type Sub2APIClient interface {
	Version(context.Context) (string, error)
	Balance(context.Context, int64) (Sub2APIBalance, error)
	Charge(context.Context, Sub2APIChargeInput) (Sub2APICharge, error)
}

type Sub2APIConfig struct {
	BaseURL           string
	AdminEmail        string
	AdminPassword     string
	SupportedVersions []string
	Timeout           time.Duration
}

type Sub2APIBalance struct {
	UserID    int64
	USDMicros int64
}

type Sub2APIChargeInput struct {
	UserID          int64
	Code            string
	ChargeUSDMicros int64
	Notes           string
}

type Sub2APICharge struct {
	Code            string
	UserID          int64
	ChargeUSDMicros int64
	Status          string
}

type Sub2APIHTTPError struct {
	StatusCode int
}

func (e *Sub2APIHTTPError) Error() string {
	return fmt.Sprintf("sub2api request failed with status %d", e.StatusCode)
}

type Sub2APIHTTPClient struct {
	baseURL           string
	adminEmail        string
	adminPassword     string
	supportedVersions map[string]struct{}
	timeout           time.Duration
	client            *http.Client

	authMu       sync.Mutex
	accessToken  string
	refreshToken string
}

func NewSub2APIHTTPClient(config Sub2APIConfig, client *http.Client) (*Sub2APIHTTPClient, error) {
	normalizedBaseURL := strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	parsed, err := url.Parse(normalizedBaseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("invalid Sub2API base URL")
	}
	if strings.TrimSpace(config.AdminEmail) == "" || config.AdminPassword == "" {
		return nil, errors.New("Sub2API admin credentials are required")
	}
	if config.Timeout <= 0 || config.Timeout > maxSub2APIRequestTimeout {
		return nil, fmt.Errorf("Sub2API timeout must be between 1ns and %s", maxSub2APIRequestTimeout)
	}
	supported := make(map[string]struct{}, len(config.SupportedVersions))
	for _, version := range config.SupportedVersions {
		if version = strings.TrimSpace(version); version != "" {
			supported[version] = struct{}{}
		}
	}
	if len(supported) == 0 {
		return nil, errors.New("at least one Sub2API version is required")
	}
	if client == nil {
		client = http.DefaultClient
	}
	return &Sub2APIHTTPClient{
		baseURL:           normalizedBaseURL,
		adminEmail:        config.AdminEmail,
		adminPassword:     config.AdminPassword,
		supportedVersions: supported,
		timeout:           config.Timeout,
		client:            client,
	}, nil
}

func (c *Sub2APIHTTPClient) Version(ctx context.Context) (string, error) {
	body, err := c.doAuthenticated(ctx, http.MethodGet, "/api/v1/admin/system/version", nil, "")
	if err != nil {
		return "", err
	}
	var data struct {
		Version string `json:"version"`
	}
	if err := decodeSub2APIEnvelope(body, &data); err != nil {
		return "", err
	}
	if strings.TrimSpace(data.Version) == "" {
		return "", errors.New("sub2api version missing")
	}
	return data.Version, nil
}

func (c *Sub2APIHTTPClient) Balance(ctx context.Context, userID int64) (Sub2APIBalance, error) {
	if userID <= 0 {
		return Sub2APIBalance{}, errors.New("sub2api user ID must be positive")
	}
	body, err := c.doAuthenticated(ctx, http.MethodGet, "/api/v1/admin/users/"+strconv.FormatInt(userID, 10), nil, "")
	if err != nil {
		return Sub2APIBalance{}, err
	}
	var data struct {
		ID      int64       `json:"id"`
		Balance json.Number `json:"balance"`
	}
	if err := decodeSub2APIEnvelope(body, &data); err != nil {
		return Sub2APIBalance{}, err
	}
	if data.ID != userID {
		return Sub2APIBalance{}, errors.New("sub2api user identity mismatch")
	}
	micros, err := decimalUSDMicros(data.Balance)
	if err != nil {
		return Sub2APIBalance{}, fmt.Errorf("invalid sub2api balance: %w", err)
	}
	return Sub2APIBalance{UserID: userID, USDMicros: micros}, nil
}

func (c *Sub2APIHTTPClient) Charge(ctx context.Context, input Sub2APIChargeInput) (Sub2APICharge, error) {
	if input.UserID <= 0 || strings.TrimSpace(input.Code) == "" || input.ChargeUSDMicros <= 0 {
		return Sub2APICharge{}, errors.New("sub2api charge identity and positive amount are required")
	}
	version, err := c.Version(ctx)
	if err != nil {
		return Sub2APICharge{}, err
	}
	if _, ok := c.supportedVersions[version]; !ok {
		return Sub2APICharge{}, fmt.Errorf("%w: %s", ErrSub2APIUnsupportedVersion, version)
	}
	payload := struct {
		Code   string          `json:"code"`
		Type   string          `json:"type"`
		Value  json.RawMessage `json:"value"`
		UserID int64           `json:"user_id"`
		Notes  string          `json:"notes,omitempty"`
	}{
		Code: input.Code, Type: "balance", Value: negativeUSDMicrosJSON(input.ChargeUSDMicros), UserID: input.UserID, Notes: input.Notes,
	}
	body, err := c.doAuthenticated(ctx, http.MethodPost, "/api/v1/admin/redeem-codes/create-and-redeem", payload, input.Code)
	if err != nil {
		var httpErr *Sub2APIHTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusConflict {
			return Sub2APICharge{}, fmt.Errorf("%w: redeem code rejected", ErrSub2APIChargeConflict)
		}
		if !errors.As(err, &httpErr) || httpErr.StatusCode >= http.StatusInternalServerError || errors.Is(err, ErrSub2APIResponseTooLarge) {
			return Sub2APICharge{}, fmt.Errorf("%w: request did not produce a confirmed response", ErrSub2APIChargeUnknown)
		}
		return Sub2APICharge{}, err
	}
	var data struct {
		RedeemCode struct {
			Code   string      `json:"code"`
			Type   string      `json:"type"`
			Value  json.Number `json:"value"`
			Status string      `json:"status"`
			UsedBy *int64      `json:"used_by"`
		} `json:"redeem_code"`
	}
	if err := decodeSub2APIEnvelope(body, &data); err != nil {
		return Sub2APICharge{}, fmt.Errorf("%w: response could not be confirmed", ErrSub2APIChargeUnknown)
	}
	valueMicros, err := decimalUSDMicros(data.RedeemCode.Value)
	if err != nil {
		return Sub2APICharge{}, fmt.Errorf("%w: response amount could not be confirmed", ErrSub2APIChargeUnknown)
	}
	if data.RedeemCode.Code != input.Code || data.RedeemCode.Type != "balance" || data.RedeemCode.Status != "used" || data.RedeemCode.UsedBy == nil || *data.RedeemCode.UsedBy != input.UserID || valueMicros != -input.ChargeUSDMicros {
		return Sub2APICharge{}, fmt.Errorf("%w: redeem record differs from requested charge", ErrSub2APIChargeConflict)
	}
	return Sub2APICharge{Code: input.Code, UserID: input.UserID, ChargeUSDMicros: input.ChargeUSDMicros, Status: data.RedeemCode.Status}, nil
}

func (c *Sub2APIHTTPClient) doAuthenticated(ctx context.Context, method, path string, input any, idempotencyKey string) ([]byte, error) {
	token, err := c.token(ctx)
	if err != nil {
		return nil, err
	}
	body, err := c.request(ctx, method, path, input, token, idempotencyKey)
	var httpErr *Sub2APIHTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusUnauthorized {
		return body, err
	}
	token, err = c.refreshAfterUnauthorized(ctx, token)
	if err != nil {
		return nil, err
	}
	return c.request(ctx, method, path, input, token, idempotencyKey)
}

func (c *Sub2APIHTTPClient) token(ctx context.Context) (string, error) {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	if c.accessToken != "" {
		return c.accessToken, nil
	}
	body, err := c.request(ctx, http.MethodPost, "/api/v1/auth/login", map[string]string{"email": c.adminEmail, "password": c.adminPassword}, "", "")
	if err != nil {
		return "", err
	}
	var data struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := decodeSub2APIEnvelope(body, &data); err != nil || data.AccessToken == "" || data.RefreshToken == "" {
		return "", errors.New("sub2api login response invalid")
	}
	c.accessToken, c.refreshToken = data.AccessToken, data.RefreshToken
	return c.accessToken, nil
}

func (c *Sub2APIHTTPClient) refreshAfterUnauthorized(ctx context.Context, rejectedToken string) (string, error) {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	if c.accessToken != "" && c.accessToken != rejectedToken {
		return c.accessToken, nil
	}
	body, err := c.request(ctx, http.MethodPost, "/api/v1/auth/refresh", map[string]string{"refresh_token": c.refreshToken}, "", "")
	if err != nil {
		return "", err
	}
	var data struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := decodeSub2APIEnvelope(body, &data); err != nil || data.AccessToken == "" || data.RefreshToken == "" {
		return "", errors.New("sub2api refresh response invalid")
	}
	c.accessToken, c.refreshToken = data.AccessToken, data.RefreshToken
	return c.accessToken, nil
}

func (c *Sub2APIHTTPClient) request(ctx context.Context, method, path string, input any, token, idempotencyKey string) ([]byte, error) {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return nil, errors.New("encode sub2api request")
		}
		body = bytes.NewReader(encoded)
	}
	requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, method, c.baseURL+path, body)
	if err != nil {
		return nil, errors.New("create sub2api request")
	}
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	res, err := c.client.Do(req)
	if err != nil {
		return nil, errors.New("sub2api transport failure")
	}
	defer res.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(res.Body, maxSub2APIResponseBytes+1))
	if err != nil {
		return nil, errors.New("read sub2api response")
	}
	if len(responseBody) > maxSub2APIResponseBytes {
		return nil, ErrSub2APIResponseTooLarge
	}
	if res.StatusCode < http.StatusOK || res.StatusCode >= http.StatusMultipleChoices {
		return nil, &Sub2APIHTTPError{StatusCode: res.StatusCode}
	}
	return responseBody, nil
}

func decodeSub2APIEnvelope(body []byte, output any) error {
	var envelope struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Code != 0 || len(envelope.Data) == 0 {
		return errors.New("invalid sub2api response envelope")
	}
	decoder := json.NewDecoder(bytes.NewReader(envelope.Data))
	decoder.UseNumber()
	if err := decoder.Decode(output); err != nil {
		return errors.New("invalid sub2api response data")
	}
	return nil
}

func decimalUSDMicros(value json.Number) (int64, error) {
	rational, ok := new(big.Rat).SetString(value.String())
	if !ok {
		return 0, errors.New("invalid decimal")
	}
	rational.Mul(rational, big.NewRat(1_000_000, 1))
	if rational.Denom().Cmp(big.NewInt(1)) != 0 || !rational.Num().IsInt64() {
		return 0, errors.New("decimal is not representable as USD micros")
	}
	return rational.Num().Int64(), nil
}

func negativeUSDMicrosJSON(micros int64) json.RawMessage {
	return json.RawMessage(fmt.Sprintf("-%d.%06d", micros/1_000_000, micros%1_000_000))
}
