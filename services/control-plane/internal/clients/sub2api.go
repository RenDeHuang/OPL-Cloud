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
	maxSub2APIIdentityPages  = 10
	maxSub2APIIdentities     = sub2APIKeyPageSize * maxSub2APIIdentityPages
	maxSub2APIUsagePage      = 1_000_000
)

var (
	ErrSub2APIChargeConflict        = errors.New("sub2api charge conflict")
	ErrSub2APIChargeUnknown         = errors.New("sub2api charge result unknown")
	ErrSub2APIResponseTooLarge      = errors.New("sub2api response too large")
	ErrSub2APIWorkspaceKeyMissing   = errors.New("sub2api workspace key missing")
	ErrSub2APIWorkspaceKeyAmbiguous = errors.New("sub2api workspace key ambiguous")
	ErrSub2APIIdentityConflict      = errors.New("sub2api identity conflict")
	ErrSub2APIIdentityUnknown       = errors.New("sub2api identity result unknown")
	ErrSub2APIInvalidCredentials    = errors.New("sub2api invalid credentials")
	ErrSub2APIAuthRateLimited       = errors.New("sub2api authentication rate limited")
	ErrSub2APIAuthUnavailable       = errors.New("sub2api authentication unavailable")
)

type Sub2APIClient interface {
	Version(context.Context) (string, error)
	Balance(context.Context, int64) (Sub2APIBalance, error)
	Charge(context.Context, Sub2APIChargeInput) (Sub2APICharge, error)
}

type Sub2APIWorkspaceKeyClient interface {
	WorkspaceKey(context.Context, int64) (Sub2APIWorkspaceKey, error)
}

type Sub2APIKeyListClient interface {
	Keys(context.Context, int64) ([]Sub2APIWorkspaceKey, error)
}

type Sub2APIRefundClient interface {
	Refund(context.Context, Sub2APIRefundInput) (Sub2APIRefund, error)
}

type Sub2APIUsageClient interface {
	Usage(context.Context, Sub2APIUsageQuery) (Sub2APIUsagePage, error)
	UsageStats(context.Context, Sub2APIUsageStatsQuery) (Sub2APIUsageStats, error)
	BalanceHistory(context.Context, int64) ([]Sub2APIBalanceHistoryEntry, error)
}

type Sub2APIIdentityClient interface {
	ResolveOrCreateUser(context.Context, string, string) (Sub2APIIdentity, error)
	AuthenticateUser(context.Context, string, string) (Sub2APIIdentity, error)
	UserIdentity(context.Context, int64, string) (Sub2APIIdentity, error)
}

type Sub2APIUserReadClient interface {
	User(context.Context, int64) (Sub2APIIdentity, error)
}

type Sub2APIAdminIdentityClient interface {
	AdminIdentity(context.Context) (Sub2APIIdentity, error)
}

type Sub2APIConfig struct {
	BaseURL       string
	AdminEmail    string
	AdminPassword string
	Timeout       time.Duration
}

type Sub2APIBalance struct {
	UserID    int64
	USDMicros int64
	Status    string
}

type Sub2APIIdentity struct {
	ID     int64  `json:"id"`
	Email  string `json:"email"`
	Status string `json:"status"`
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

type Sub2APIUsageQuery struct {
	UserID   int64
	APIKeyID int64
	Page     int
	PageSize int
}

type Sub2APIUsageRecord struct {
	UserID              int64     `json:"user_id"`
	APIKeyID            int64     `json:"api_key_id"`
	RequestID           string    `json:"request_id"`
	CreatedAt           time.Time `json:"created_at"`
	Model               string    `json:"model"`
	InboundEndpoint     string    `json:"inbound_endpoint"`
	RequestType         string    `json:"request_type"`
	InputTokens         int64     `json:"input_tokens"`
	OutputTokens        int64     `json:"output_tokens"`
	CacheCreationTokens int64     `json:"cache_creation_tokens"`
	CacheReadTokens     int64     `json:"cache_read_tokens"`
	ActualCostUSDMicros int64     `json:"actual_cost_usd_micros"`
}

type Sub2APIUsagePage struct {
	Items    []Sub2APIUsageRecord
	Total    int64
	Page     int
	PageSize int
	Pages    int
}

type Sub2APIUsageStatsQuery struct {
	UserID   int64
	APIKeyID int64
	Period   string
}

type Sub2APIUsageStats struct {
	TotalRequests            int64 `json:"total_requests"`
	TotalInputTokens         int64 `json:"total_input_tokens"`
	TotalOutputTokens        int64 `json:"total_output_tokens"`
	TotalTokens              int64 `json:"total_tokens"`
	TotalActualCostUSDMicros int64 `json:"total_actual_cost_usd_micros"`
}

type Sub2APIBalanceHistoryEntry struct {
	Code           string     `json:"code"`
	Type           string     `json:"type"`
	ValueUSDMicros int64      `json:"value_usd_micros"`
	Status         string     `json:"status"`
	UsedBy         *int64     `json:"used_by"`
	UsedAt         *time.Time `json:"used_at"`
	CreatedAt      time.Time  `json:"created_at"`
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
	baseURL       string
	adminEmail    string
	adminPassword string
	timeout       time.Duration
	client        *http.Client

	authMu       sync.Mutex
	accessToken  string
	refreshToken string

	// ponytail: Pilot serializes identity convergence globally; use per-email locks if throughput matters.
	identityGate chan struct{}
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
	if client == nil {
		client = http.DefaultClient
	}
	return &Sub2APIHTTPClient{
		baseURL:       normalizedBaseURL,
		adminEmail:    config.AdminEmail,
		adminPassword: config.AdminPassword,
		timeout:       config.Timeout,
		client:        client,
		identityGate:  make(chan struct{}, 1),
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
	if data.Status != "active" && data.Status != "disabled" {
		return Sub2APIBalance{}, errors.New("invalid sub2api user status")
	}
	micros, err := decimalUSDMicros(data.Balance)
	if err != nil {
		return Sub2APIBalance{}, fmt.Errorf("invalid sub2api balance: %w", err)
	}
	return Sub2APIBalance{UserID: userID, USDMicros: micros, Status: data.Status}, nil
}

func (c *Sub2APIHTTPClient) ResolveOrCreateUser(ctx context.Context, email, password string) (Sub2APIIdentity, error) {
	email = normalizeSub2APIEmail(email)
	if email == "" || password == "" {
		return Sub2APIIdentity{}, ErrSub2APIIdentityUnknown
	}
	select {
	case c.identityGate <- struct{}{}:
	case <-ctx.Done():
		return Sub2APIIdentity{}, ctx.Err()
	}
	defer func() { <-c.identityGate }()
	matches, err := c.usersByEmail(ctx, email)
	if err != nil {
		return Sub2APIIdentity{}, err
	}
	switch len(matches) {
	case 1:
		return c.authenticatedUserIdentity(ctx, matches[0].ID, email, password)
	case 0:
		// User creation has no proven idempotency-key support. Sub2API's normalized-email
		// uniqueness and the mandatory lookup/readback below provide convergence.
		_, _ = c.doAuthenticated(ctx, http.MethodPost, "/api/v1/admin/users", map[string]string{
			"email": email, "password": password, "role": "user",
		}, "")
	default:
		return Sub2APIIdentity{}, ErrSub2APIIdentityConflict
	}
	matches, err = c.usersByEmail(ctx, email)
	if err != nil {
		return Sub2APIIdentity{}, fmt.Errorf("%w: %v", ErrSub2APIIdentityUnknown, err)
	}
	if len(matches) > 1 {
		return Sub2APIIdentity{}, ErrSub2APIIdentityConflict
	}
	if len(matches) != 1 {
		return Sub2APIIdentity{}, ErrSub2APIIdentityUnknown
	}
	return c.authenticatedUserIdentity(ctx, matches[0].ID, email, password)
}

func (c *Sub2APIHTTPClient) authenticatedUserIdentity(ctx context.Context, userID int64, email, password string) (Sub2APIIdentity, error) {
	identity, err := c.AuthenticateUser(ctx, email, password)
	if err != nil {
		return Sub2APIIdentity{}, err
	}
	if identity.ID != userID {
		return Sub2APIIdentity{}, ErrSub2APIIdentityConflict
	}
	return c.UserIdentity(ctx, userID, email)
}

func (c *Sub2APIHTTPClient) AuthenticateUser(ctx context.Context, email, password string) (Sub2APIIdentity, error) {
	email = normalizeSub2APIEmail(email)
	if email == "" || password == "" {
		return Sub2APIIdentity{}, ErrSub2APIInvalidCredentials
	}
	body, err := c.request(ctx, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"email": email, "password": password, "turnstile_token": "",
	}, "", "")
	if err != nil {
		var httpErr *Sub2APIHTTPError
		switch {
		case errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusUnauthorized:
			return Sub2APIIdentity{}, ErrSub2APIInvalidCredentials
		case errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusTooManyRequests:
			return Sub2APIIdentity{}, ErrSub2APIAuthRateLimited
		default:
			return Sub2APIIdentity{}, ErrSub2APIAuthUnavailable
		}
	}
	var data struct {
		AccessToken string           `json:"access_token"`
		User        *Sub2APIIdentity `json:"user"`
	}
	if err := decodeSub2APIEnvelope(body, &data); err != nil || data.AccessToken == "" || data.User == nil {
		return Sub2APIIdentity{}, ErrSub2APIAuthUnavailable
	}
	identity := *data.User
	identity.Email = normalizeSub2APIEmail(identity.Email)
	if identity.ID <= 0 || identity.Email != email || identity.Status != "active" {
		return Sub2APIIdentity{}, ErrSub2APIAuthUnavailable
	}
	return identity, nil
}

func (c *Sub2APIHTTPClient) UserIdentity(ctx context.Context, userID int64, email string) (Sub2APIIdentity, error) {
	email = normalizeSub2APIEmail(email)
	if userID <= 0 || email == "" {
		return Sub2APIIdentity{}, ErrSub2APIIdentityUnknown
	}
	identity, err := c.User(ctx, userID)
	if err != nil {
		return Sub2APIIdentity{}, err
	}
	if identity.Email != email || identity.Status != "active" {
		return Sub2APIIdentity{}, ErrSub2APIIdentityConflict
	}
	return identity, nil
}

func (c *Sub2APIHTTPClient) User(ctx context.Context, userID int64) (Sub2APIIdentity, error) {
	if userID <= 0 {
		return Sub2APIIdentity{}, ErrSub2APIIdentityUnknown
	}
	body, err := c.doAuthenticated(ctx, http.MethodGet, "/api/v1/admin/users/"+strconv.FormatInt(userID, 10), nil, "")
	if err != nil {
		return Sub2APIIdentity{}, err
	}
	var identity Sub2APIIdentity
	if err := decodeSub2APIEnvelope(body, &identity); err != nil {
		return Sub2APIIdentity{}, err
	}
	identity.Email = normalizeSub2APIEmail(identity.Email)
	if identity.ID != userID || identity.ID <= 0 || identity.Email == "" || (identity.Status != "active" && identity.Status != "disabled") {
		return Sub2APIIdentity{}, ErrSub2APIIdentityConflict
	}
	return identity, nil
}

func (c *Sub2APIHTTPClient) AdminIdentity(ctx context.Context) (Sub2APIIdentity, error) {
	identity, err := c.AuthenticateUser(ctx, c.adminEmail, c.adminPassword)
	if err != nil {
		return Sub2APIIdentity{}, err
	}
	return c.UserIdentity(ctx, identity.ID, c.adminEmail)
}

func (c *Sub2APIHTTPClient) usersByEmail(ctx context.Context, email string) ([]Sub2APIIdentity, error) {
	matches := make([]Sub2APIIdentity, 0, 1)
	seenIDs := make(map[int64]struct{})
	total, pages, collected := int64(-1), -1, int64(0)
	for page := 1; page <= maxSub2APIIdentityPages; page++ {
		query := url.Values{
			"page": {strconv.Itoa(page)}, "page_size": {strconv.Itoa(sub2APIKeyPageSize)},
			"search": {email}, "sort_by": {"id"}, "sort_order": {"asc"},
		}
		body, err := c.doAuthenticated(ctx, http.MethodGet, "/api/v1/admin/users?"+query.Encode(), nil, "")
		if err != nil {
			return nil, err
		}
		var data struct {
			Items    []Sub2APIIdentity `json:"items"`
			Total    int64             `json:"total"`
			Page     int               `json:"page"`
			PageSize int               `json:"page_size"`
			Pages    int               `json:"pages"`
		}
		if err := decodeSub2APIEnvelope(body, &data); err != nil {
			return nil, err
		}
		expectedPages := int((data.Total + int64(sub2APIKeyPageSize) - 1) / int64(sub2APIKeyPageSize))
		if expectedPages < 1 {
			expectedPages = 1
		}
		expectedItems := sub2APIKeyPageSize
		if page == expectedPages {
			expectedItems = int(data.Total) - (page-1)*sub2APIKeyPageSize
		}
		if data.Total < 0 || data.Total > int64(maxSub2APIIdentities) || data.Page != page || data.PageSize != sub2APIKeyPageSize || data.Pages != expectedPages || data.Pages > maxSub2APIIdentityPages || len(data.Items) != expectedItems {
			return nil, ErrSub2APIIdentityConflict
		}
		if page == 1 {
			total, pages = data.Total, data.Pages
		} else if data.Total != total || data.Pages != pages {
			return nil, ErrSub2APIIdentityConflict
		}
		for _, item := range data.Items {
			item.Email = normalizeSub2APIEmail(item.Email)
			if item.ID <= 0 || item.Email == "" || item.Status == "" {
				return nil, ErrSub2APIIdentityConflict
			}
			if _, exists := seenIDs[item.ID]; exists {
				return nil, ErrSub2APIIdentityConflict
			}
			seenIDs[item.ID] = struct{}{}
			if item.Email == email {
				matches = append(matches, item)
			}
		}
		collected += int64(len(data.Items))
		if collected > total || (len(data.Items) == 0 && collected < total) {
			return nil, ErrSub2APIIdentityConflict
		}
		if page == pages {
			if collected != total {
				return nil, ErrSub2APIIdentityConflict
			}
			return matches, nil
		}
	}
	return nil, ErrSub2APIIdentityConflict
}

func normalizeSub2APIEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func (c *Sub2APIHTTPClient) Keys(ctx context.Context, userID int64) ([]Sub2APIWorkspaceKey, error) {
	if userID <= 0 {
		return nil, errors.New("sub2api user ID must be positive")
	}
	keys := make([]Sub2APIWorkspaceKey, 0)
	seenIDs := make(map[int64]struct{})
	total, pages, collected := -1, -1, 0
	for page := 1; page <= maxSub2APIKeyPages; page++ {
		query := url.Values{"page": {strconv.Itoa(page)}, "page_size": {strconv.Itoa(sub2APIKeyPageSize)}}
		body, err := c.doAuthenticated(ctx, http.MethodGet, "/api/v1/admin/users/"+strconv.FormatInt(userID, 10)+"/api-keys?"+query.Encode(), nil, "")
		if err != nil {
			return nil, err
		}
		var data struct {
			Items []struct {
				ID         int64        `json:"id"`
				UserID     int64        `json:"user_id"`
				Name       string       `json:"name"`
				Key        string       `json:"key"`
				Status     string       `json:"status"`
				Quota      *json.Number `json:"quota"`
				QuotaUsed  *json.Number `json:"quota_used"`
				Usage5h    *json.Number `json:"usage_5h"`
				Usage1d    *json.Number `json:"usage_1d"`
				Usage7d    *json.Number `json:"usage_7d"`
				LastUsedAt *time.Time   `json:"last_used_at"`
			} `json:"items"`
			Page     int `json:"page"`
			PageSize int `json:"page_size"`
			Pages    int `json:"pages"`
			Total    int `json:"total"`
		}
		if err := decodeSub2APIEnvelope(body, &data); err != nil {
			return nil, err
		}
		if data.Total < 0 || data.Total > maxSub2APIKeys {
			return nil, errors.New("invalid sub2api api key pagination")
		}
		expectedPages := (data.Total + sub2APIKeyPageSize - 1) / sub2APIKeyPageSize
		if data.Page != page || data.PageSize != sub2APIKeyPageSize || data.Pages != expectedPages || data.Pages > maxSub2APIKeyPages || len(data.Items) > sub2APIKeyPageSize {
			return nil, errors.New("invalid sub2api api key pagination")
		}
		if page == 1 {
			total, pages = data.Total, data.Pages
		} else if data.Total != total || data.Pages != pages || data.PageSize != sub2APIKeyPageSize {
			return nil, errors.New("invalid sub2api api key pagination")
		}
		for _, item := range data.Items {
			if item.UserID != userID {
				return nil, errors.New("sub2api user identity mismatch")
			}
			if item.ID <= 0 {
				return nil, errors.New("invalid sub2api api key pagination")
			}
			if strings.TrimSpace(item.Name) == "" || (item.Status != "active" && item.Status != "disabled") {
				return nil, errors.New("invalid sub2api api key")
			}
			if _, exists := seenIDs[item.ID]; exists {
				return nil, errors.New("invalid sub2api api key pagination")
			}
			seenIDs[item.ID] = struct{}{}
			values := []*json.Number{item.Quota, item.QuotaUsed, item.Usage5h, item.Usage1d, item.Usage7d}
			micros := make([]int64, len(values))
			for i, value := range values {
				if value == nil {
					return nil, errors.New("invalid sub2api workspace key usage")
				}
				micros[i], err = decimalUSDMicros(*value)
				if err != nil || micros[i] < 0 {
					return nil, errors.New("invalid sub2api workspace key usage")
				}
			}
			keys = append(keys, Sub2APIWorkspaceKey{
				ID: item.ID, UserID: item.UserID, Name: item.Name, Key: item.Key, Status: item.Status,
				QuotaUSDMicros: micros[0], QuotaUsedUSDMicros: micros[1], Usage5hUSDMicros: micros[2],
				Usage1dUSDMicros: micros[3], Usage7dUSDMicros: micros[4], LastUsedAt: item.LastUsedAt,
			})
		}
		collected += len(data.Items)
		if collected > total || (len(data.Items) == 0 && collected < total) {
			return nil, errors.New("invalid sub2api api key pagination")
		}
		if pages == 0 {
			break
		}
		if page == pages {
			if collected != total {
				return nil, errors.New("invalid sub2api api key pagination")
			}
			break
		}
	}
	return keys, nil
}

func (c *Sub2APIHTTPClient) WorkspaceKey(ctx context.Context, userID int64) (Sub2APIWorkspaceKey, error) {
	keys, err := c.Keys(ctx, userID)
	if err != nil {
		return Sub2APIWorkspaceKey{}, err
	}
	matches := make([]Sub2APIWorkspaceKey, 0, 1)
	for _, key := range keys {
		if key.Name == "opl-workspace" && key.Status == "active" {
			matches = append(matches, key)
		}
	}
	if len(matches) == 0 {
		return Sub2APIWorkspaceKey{}, ErrSub2APIWorkspaceKeyMissing
	}
	if len(matches) != 1 {
		return Sub2APIWorkspaceKey{}, ErrSub2APIWorkspaceKeyAmbiguous
	}
	if matches[0].Key == "" {
		return Sub2APIWorkspaceKey{}, errors.New("invalid sub2api workspace key")
	}
	return matches[0], nil
}

func (c *Sub2APIHTTPClient) Usage(ctx context.Context, query Sub2APIUsageQuery) (Sub2APIUsagePage, error) {
	if query.UserID <= 0 || query.APIKeyID <= 0 || query.Page <= 0 || query.Page > maxSub2APIUsagePage || query.PageSize <= 0 || query.PageSize > 100 {
		return Sub2APIUsagePage{}, errors.New("invalid sub2api usage query")
	}
	values := url.Values{
		"api_key_id": {strconv.FormatInt(query.APIKeyID, 10)},
		"page":       {strconv.Itoa(query.Page)},
		"page_size":  {strconv.Itoa(query.PageSize)},
		"sort_by":    {"created_at"},
		"sort_order": {"desc"},
		"user_id":    {strconv.FormatInt(query.UserID, 10)},
	}
	body, err := c.doAuthenticated(ctx, http.MethodGet, "/api/v1/admin/usage?"+values.Encode(), nil, "")
	if err != nil {
		return Sub2APIUsagePage{}, err
	}
	type usageRow struct {
		UserID              int64        `json:"user_id"`
		APIKeyID            int64        `json:"api_key_id"`
		RequestID           string       `json:"request_id"`
		CreatedAt           *time.Time   `json:"created_at"`
		Model               string       `json:"model"`
		InboundEndpoint     *string      `json:"inbound_endpoint"`
		RequestType         string       `json:"request_type"`
		InputTokens         *int64       `json:"input_tokens"`
		OutputTokens        *int64       `json:"output_tokens"`
		CacheCreationTokens *int64       `json:"cache_creation_tokens"`
		CacheReadTokens     *int64       `json:"cache_read_tokens"`
		ActualCost          *json.Number `json:"actual_cost"`
	}
	var data struct {
		Items    []usageRow `json:"items"`
		Total    int64      `json:"total"`
		Page     int        `json:"page"`
		PageSize int        `json:"page_size"`
		Pages    int        `json:"pages"`
	}
	if err := decodeSub2APIEnvelope(body, &data); err != nil {
		return Sub2APIUsagePage{}, err
	}
	var expectedPages int64
	expectedItems := 0
	if data.Total > 0 {
		expectedPages = (data.Total-1)/int64(query.PageSize) + 1
		if int64(query.Page) <= expectedPages {
			remaining := data.Total - int64(query.Page-1)*int64(query.PageSize)
			expectedItems = query.PageSize
			if remaining < int64(expectedItems) {
				expectedItems = int(remaining)
			}
		}
	}
	if data.Total < 0 || expectedPages > maxSub2APIUsagePage || data.Page != query.Page || data.PageSize != query.PageSize || int64(data.Pages) != expectedPages || data.Total == 0 && query.Page != 1 || data.Total > 0 && int64(query.Page) > expectedPages || len(data.Items) != expectedItems {
		return Sub2APIUsagePage{}, errors.New("invalid sub2api usage pagination")
	}
	items := make([]Sub2APIUsageRecord, 0, len(data.Items))
	for _, item := range data.Items {
		if item.UserID != query.UserID || item.APIKeyID != query.APIKeyID {
			return Sub2APIUsagePage{}, errors.New("sub2api usage identity mismatch")
		}
		if item.CreatedAt == nil || item.CreatedAt.IsZero() || item.RequestID == "" || item.Model == "" || item.RequestType == "" || !validUsageCounts(item.InputTokens, item.OutputTokens, item.CacheCreationTokens, item.CacheReadTokens) || item.ActualCost == nil {
			return Sub2APIUsagePage{}, errors.New("invalid sub2api usage record")
		}
		actualCost, err := decimalUSDMicros(*item.ActualCost)
		if err != nil || actualCost < 0 {
			return Sub2APIUsagePage{}, errors.New("invalid sub2api usage actual cost")
		}
		inboundEndpoint := ""
		if item.InboundEndpoint != nil {
			inboundEndpoint = *item.InboundEndpoint
		}
		items = append(items, Sub2APIUsageRecord{
			UserID: item.UserID, APIKeyID: item.APIKeyID, RequestID: item.RequestID, CreatedAt: *item.CreatedAt,
			Model: item.Model, InboundEndpoint: inboundEndpoint, RequestType: item.RequestType,
			InputTokens: *item.InputTokens, OutputTokens: *item.OutputTokens, CacheCreationTokens: *item.CacheCreationTokens,
			CacheReadTokens: *item.CacheReadTokens, ActualCostUSDMicros: actualCost,
		})
	}
	return Sub2APIUsagePage{Items: items, Total: data.Total, Page: data.Page, PageSize: data.PageSize, Pages: data.Pages}, nil
}

func (c *Sub2APIHTTPClient) UsageStats(ctx context.Context, query Sub2APIUsageStatsQuery) (Sub2APIUsageStats, error) {
	period := strings.TrimSpace(query.Period)
	if period == "" {
		period = "month"
	}
	if query.UserID <= 0 || query.APIKeyID <= 0 || (period != "today" && period != "week" && period != "month") {
		return Sub2APIUsageStats{}, errors.New("invalid sub2api usage stats query")
	}
	values := url.Values{
		"api_key_id": {strconv.FormatInt(query.APIKeyID, 10)},
		"period":     {period},
		"user_id":    {strconv.FormatInt(query.UserID, 10)},
	}
	body, err := c.doAuthenticated(ctx, http.MethodGet, "/api/v1/admin/usage/stats?"+values.Encode(), nil, "")
	if err != nil {
		return Sub2APIUsageStats{}, err
	}
	var data struct {
		TotalRequests     *int64       `json:"total_requests"`
		TotalInputTokens  *int64       `json:"total_input_tokens"`
		TotalOutputTokens *int64       `json:"total_output_tokens"`
		TotalTokens       *int64       `json:"total_tokens"`
		TotalActualCost   *json.Number `json:"total_actual_cost"`
	}
	if err := decodeSub2APIEnvelope(body, &data); err != nil {
		return Sub2APIUsageStats{}, err
	}
	if !validUsageCounts(data.TotalRequests, data.TotalInputTokens, data.TotalOutputTokens, data.TotalTokens) || data.TotalActualCost == nil {
		return Sub2APIUsageStats{}, errors.New("invalid sub2api usage stats")
	}
	actualCost, err := decimalUSDMicros(*data.TotalActualCost)
	if err != nil || actualCost < 0 {
		return Sub2APIUsageStats{}, errors.New("invalid sub2api usage stats actual cost")
	}
	return Sub2APIUsageStats{
		TotalRequests: *data.TotalRequests, TotalInputTokens: *data.TotalInputTokens, TotalOutputTokens: *data.TotalOutputTokens,
		TotalTokens: *data.TotalTokens, TotalActualCostUSDMicros: actualCost,
	}, nil
}

func (c *Sub2APIHTTPClient) BalanceHistory(ctx context.Context, userID int64) ([]Sub2APIBalanceHistoryEntry, error) {
	if userID <= 0 {
		return nil, errors.New("sub2api user ID must be positive")
	}
	entries := make([]Sub2APIBalanceHistoryEntry, 0)
	var total int64 = -1
	pages := -1
	var collected int64
	for page := 1; page <= maxSub2APIKeyPages; page++ {
		values := url.Values{"page": {strconv.Itoa(page)}, "page_size": {strconv.Itoa(sub2APIKeyPageSize)}, "type": {"balance"}}
		body, err := c.doAuthenticated(ctx, http.MethodGet, "/api/v1/admin/users/"+strconv.FormatInt(userID, 10)+"/balance-history?"+values.Encode(), nil, "")
		if err != nil {
			return nil, err
		}
		var data struct {
			Items []struct {
				Code      string       `json:"code"`
				Type      string       `json:"type"`
				Value     *json.Number `json:"value"`
				Status    string       `json:"status"`
				UsedBy    *int64       `json:"used_by"`
				UsedAt    *time.Time   `json:"used_at"`
				CreatedAt *time.Time   `json:"created_at"`
			} `json:"items"`
			Total    int64 `json:"total"`
			Page     int   `json:"page"`
			PageSize int   `json:"page_size"`
			Pages    int   `json:"pages"`
		}
		if err := decodeSub2APIEnvelope(body, &data); err != nil {
			return nil, err
		}
		if data.Total < 0 || data.Total > int64(maxSub2APIKeys) {
			return nil, errors.New("invalid sub2api balance history pagination")
		}
		expectedPages := int((data.Total + int64(sub2APIKeyPageSize) - 1) / int64(sub2APIKeyPageSize))
		if data.Page != page || data.PageSize != sub2APIKeyPageSize || data.Pages != expectedPages || data.Pages > maxSub2APIKeyPages || len(data.Items) > sub2APIKeyPageSize {
			return nil, errors.New("invalid sub2api balance history pagination")
		}
		if page == 1 {
			total, pages = data.Total, data.Pages
		} else if data.Total != total || data.Pages != pages {
			return nil, errors.New("invalid sub2api balance history pagination")
		}
		for _, item := range data.Items {
			if item.Code == "" || item.Type != "balance" || item.Status == "" || item.Value == nil || item.CreatedAt == nil || item.CreatedAt.IsZero() {
				return nil, errors.New("invalid sub2api balance history entry")
			}
			if item.Status == "used" && (item.UsedBy == nil || *item.UsedBy != userID || item.UsedAt == nil) {
				return nil, errors.New("sub2api balance history identity mismatch")
			}
			value, err := decimalUSDMicros(*item.Value)
			if err != nil {
				return nil, errors.New("invalid sub2api balance history amount")
			}
			entries = append(entries, Sub2APIBalanceHistoryEntry{Code: item.Code, Type: item.Type, ValueUSDMicros: value, Status: item.Status, UsedBy: item.UsedBy, UsedAt: item.UsedAt, CreatedAt: *item.CreatedAt})
		}
		if len(entries) > maxSub2APIKeys {
			return nil, errors.New("invalid sub2api balance history size")
		}
		collected += int64(len(data.Items))
		if collected > total || (len(data.Items) == 0 && collected < total) {
			return nil, errors.New("invalid sub2api balance history pagination")
		}
		if pages == 0 {
			return entries, nil
		}
		if page == pages {
			if collected != total {
				return nil, errors.New("invalid sub2api balance history pagination")
			}
			return entries, nil
		}
	}
	return nil, errors.New("invalid sub2api balance history pagination")
}

func validUsageCounts(values ...*int64) bool {
	for _, value := range values {
		if value == nil || *value < 0 {
			return false
		}
	}
	return true
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
			return c.confirmAdjustmentReplay(ctx, userID, code, valueUSDMicros)
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

func (c *Sub2APIHTTPClient) confirmAdjustmentReplay(ctx context.Context, userID int64, code string, valueUSDMicros int64) (string, error) {
	history, err := c.BalanceHistory(ctx, userID)
	if err != nil {
		return "", fmt.Errorf("%w: balance history unavailable", ErrSub2APIChargeUnknown)
	}
	var match *Sub2APIBalanceHistoryEntry
	for i := range history {
		if history[i].Code != code {
			continue
		}
		if match != nil {
			return "", fmt.Errorf("%w: duplicate balance history evidence", ErrSub2APIChargeConflict)
		}
		match = &history[i]
	}
	if match == nil {
		return "", fmt.Errorf("%w: balance history evidence missing", ErrSub2APIChargeUnknown)
	}
	if match.Type != "balance" || match.Status != "used" || match.UsedBy == nil || *match.UsedBy != userID || match.UsedAt == nil || match.ValueUSDMicros != valueUSDMicros {
		return "", fmt.Errorf("%w: balance history evidence differs", ErrSub2APIChargeConflict)
	}
	return "used", nil
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
	return c.loginLocked(ctx)
}

func (c *Sub2APIHTTPClient) loginLocked(ctx context.Context) (string, error) {
	body, err := c.request(ctx, http.MethodPost, "/api/v1/auth/login", map[string]string{"email": c.adminEmail, "password": c.adminPassword}, "", "")
	if err != nil {
		return "", err
	}
	var data struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := decodeSub2APIEnvelope(body, &data); err != nil || data.AccessToken == "" {
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
	if c.refreshToken == "" {
		return c.loginLocked(ctx)
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
