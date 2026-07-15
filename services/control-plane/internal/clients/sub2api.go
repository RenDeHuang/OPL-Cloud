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
	sub2APIKeyPageSize       = 1000
	maxSub2APIKeyPages       = 10
	maxSub2APIKeys           = sub2APIKeyPageSize * maxSub2APIKeyPages
)

var (
	ErrSub2APIUnsupportedVersion    = errors.New("sub2api unsupported version")
	ErrSub2APIChargeConflict        = errors.New("sub2api charge conflict")
	ErrSub2APIChargeUnknown         = errors.New("sub2api charge result unknown")
	ErrSub2APIResponseTooLarge      = errors.New("sub2api response too large")
	ErrSub2APIWorkspaceKeyMissing   = errors.New("sub2api workspace key missing")
	ErrSub2APIWorkspaceKeyAmbiguous = errors.New("sub2api workspace key ambiguous")
)

type Sub2APIClient interface {
	Version(context.Context) (string, error)
	Balance(context.Context, int64) (Sub2APIBalance, error)
	Charge(context.Context, Sub2APIChargeInput) (Sub2APICharge, error)
}

type Sub2APIWorkspaceKeyClient interface {
	WorkspaceKey(context.Context, int64) (Sub2APIWorkspaceKey, error)
}

type Sub2APIRefundClient interface {
	Refund(context.Context, Sub2APIRefundInput) (Sub2APIRefund, error)
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
	Status    string
}

type Sub2APIWorkspaceKey struct {
	ID                 int64
	UserID             int64
	Name               string
	Key                string
	Status             string
	QuotaUSDMicros     int64
	QuotaUsedUSDMicros int64
	Usage5hUSDMicros   int64
	Usage1dUSDMicros   int64
	Usage7dUSDMicros   int64
	LastUsedAt         *time.Time
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

type Sub2APIRefundInput struct {
	UserID          int64
	Code            string
	RefundUSDMicros int64
	Notes           string
}

type Sub2APIRefund struct {
	Code            string
	UserID          int64
	RefundUSDMicros int64
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
		Status  string      `json:"status"`
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
	return Sub2APIBalance{UserID: userID, USDMicros: micros, Status: data.Status}, nil
}

func (c *Sub2APIHTTPClient) WorkspaceKey(ctx context.Context, userID int64) (Sub2APIWorkspaceKey, error) {
	if userID <= 0 {
		return Sub2APIWorkspaceKey{}, errors.New("sub2api user ID must be positive")
	}
	matches := make([]Sub2APIWorkspaceKey, 0, 1)
	for page := 1; page <= maxSub2APIKeyPages; page++ {
		query := url.Values{"page": {strconv.Itoa(page)}, "page_size": {strconv.Itoa(sub2APIKeyPageSize)}}
		body, err := c.doAuthenticated(ctx, http.MethodGet, "/api/v1/admin/users/"+strconv.FormatInt(userID, 10)+"/api-keys?"+query.Encode(), nil, "")
		if err != nil {
			return Sub2APIWorkspaceKey{}, err
		}
		var data struct {
			Items []struct {
				ID         int64       `json:"id"`
				UserID     int64       `json:"user_id"`
				Name       string      `json:"name"`
				Key        string      `json:"key"`
				Status     string      `json:"status"`
				Quota      json.Number `json:"quota"`
				QuotaUsed  json.Number `json:"quota_used"`
				Usage5h    json.Number `json:"usage_5h"`
				Usage1d    json.Number `json:"usage_1d"`
				Usage7d    json.Number `json:"usage_7d"`
				LastUsedAt *time.Time  `json:"last_used_at"`
			} `json:"items"`
			Page     int `json:"page"`
			PageSize int `json:"page_size"`
			Pages    int `json:"pages"`
			Total    int `json:"total"`
		}
		if err := decodeSub2APIEnvelope(body, &data); err != nil {
			return Sub2APIWorkspaceKey{}, err
		}
		if data.Page != page || data.PageSize <= 0 || data.Pages < page || data.Pages > maxSub2APIKeyPages || data.Total < 0 || data.Total > maxSub2APIKeys {
			return Sub2APIWorkspaceKey{}, errors.New("invalid sub2api api key pagination")
		}
		for _, item := range data.Items {
			if item.UserID != userID {
				return Sub2APIWorkspaceKey{}, errors.New("sub2api user identity mismatch")
			}
			if item.Name != "opl-workspace" || item.Status != "active" {
				continue
			}
			if item.ID <= 0 || item.Key == "" {
				return Sub2APIWorkspaceKey{}, errors.New("invalid sub2api workspace key")
			}
			values := []*json.Number{&item.Quota, &item.QuotaUsed, &item.Usage5h, &item.Usage1d, &item.Usage7d}
			micros := make([]int64, len(values))
			for i, value := range values {
				if value.String() == "" {
					continue
				}
				micros[i], err = decimalUSDMicros(*value)
				if err != nil {
					return Sub2APIWorkspaceKey{}, errors.New("invalid sub2api workspace key usage")
				}
			}
			matches = append(matches, Sub2APIWorkspaceKey{
				ID: item.ID, UserID: item.UserID, Name: item.Name, Key: item.Key, Status: item.Status,
				QuotaUSDMicros: micros[0], QuotaUsedUSDMicros: micros[1], Usage5hUSDMicros: micros[2],
				Usage1dUSDMicros: micros[3], Usage7dUSDMicros: micros[4], LastUsedAt: item.LastUsedAt,
			})
		}
		if page == data.Pages {
			break
		}
	}
	if len(matches) == 0 {
		return Sub2APIWorkspaceKey{}, ErrSub2APIWorkspaceKeyMissing
	}
	if len(matches) != 1 {
		return Sub2APIWorkspaceKey{}, ErrSub2APIWorkspaceKeyAmbiguous
	}
	return matches[0], nil
}

func (c *Sub2APIHTTPClient) Charge(ctx context.Context, input Sub2APIChargeInput) (Sub2APICharge, error) {
	if input.UserID <= 0 || strings.TrimSpace(input.Code) == "" || input.ChargeUSDMicros <= 0 {
		return Sub2APICharge{}, errors.New("sub2api charge identity and positive amount are required")
	}
	status, err := c.redeemBalance(ctx, input.UserID, input.Code, -input.ChargeUSDMicros, input.Notes)
	if err != nil {
		return Sub2APICharge{}, err
	}
	return Sub2APICharge{Code: input.Code, UserID: input.UserID, ChargeUSDMicros: input.ChargeUSDMicros, Status: status}, nil
}

func (c *Sub2APIHTTPClient) Refund(ctx context.Context, input Sub2APIRefundInput) (Sub2APIRefund, error) {
	if input.UserID <= 0 || strings.TrimSpace(input.Code) == "" || input.RefundUSDMicros <= 0 {
		return Sub2APIRefund{}, errors.New("sub2api refund identity and positive amount are required")
	}
	status, err := c.redeemBalance(ctx, input.UserID, input.Code, input.RefundUSDMicros, input.Notes)
	if err != nil {
		return Sub2APIRefund{}, err
	}
	return Sub2APIRefund{Code: input.Code, UserID: input.UserID, RefundUSDMicros: input.RefundUSDMicros, Status: status}, nil
}

func (c *Sub2APIHTTPClient) redeemBalance(ctx context.Context, userID int64, code string, valueUSDMicros int64, notes string) (string, error) {
	version, err := c.Version(ctx)
	if err != nil {
		return "", err
	}
	if _, ok := c.supportedVersions[version]; !ok {
		return "", fmt.Errorf("%w: %s", ErrSub2APIUnsupportedVersion, version)
	}
	payload := struct {
		Code   string          `json:"code"`
		Type   string          `json:"type"`
		Value  json.RawMessage `json:"value"`
		UserID int64           `json:"user_id"`
		Notes  string          `json:"notes,omitempty"`
	}{
		Code: code, Type: "balance", Value: usdMicrosJSON(valueUSDMicros), UserID: userID, Notes: notes,
	}
	body, err := c.doAuthenticated(ctx, http.MethodPost, "/api/v1/admin/redeem-codes/create-and-redeem", payload, code)
	if err != nil {
		var httpErr *Sub2APIHTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusConflict {
			return "", fmt.Errorf("%w: redeem code rejected", ErrSub2APIChargeConflict)
		}
		if !errors.As(err, &httpErr) || httpErr.StatusCode >= http.StatusInternalServerError || errors.Is(err, ErrSub2APIResponseTooLarge) {
			return "", fmt.Errorf("%w: request did not produce a confirmed response", ErrSub2APIChargeUnknown)
		}
		return "", err
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
		return "", fmt.Errorf("%w: response could not be confirmed", ErrSub2APIChargeUnknown)
	}
	valueMicros, err := decimalUSDMicros(data.RedeemCode.Value)
	if err != nil {
		return "", fmt.Errorf("%w: response amount could not be confirmed", ErrSub2APIChargeUnknown)
	}
	if data.RedeemCode.Code != code || data.RedeemCode.Type != "balance" || data.RedeemCode.Status != "used" || data.RedeemCode.UsedBy == nil || *data.RedeemCode.UsedBy != userID || valueMicros != valueUSDMicros {
		return "", fmt.Errorf("%w: redeem record differs from requested balance adjustment", ErrSub2APIChargeConflict)
	}
	return data.RedeemCode.Status, nil
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

func usdMicrosJSON(micros int64) json.RawMessage {
	sign := ""
	if micros < 0 {
		sign, micros = "-", -micros
	}
	return json.RawMessage(fmt.Sprintf("%s%d.%06d", sign, micros/1_000_000, micros%1_000_000))
}
