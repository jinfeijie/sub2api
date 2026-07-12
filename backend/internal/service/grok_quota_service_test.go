//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/stretchr/testify/require"
)

type grokQuotaAccountRepo struct {
	*mockAccountRepoForPlatform
	updates               map[int64]map[string]any
	updateCalls           int
	rateLimitedCalls      int
	lastRateLimitedID     int64
	lastRateLimitResetAt  time.Time
	tempUnschedCalls      int
	lastTempUnschedID     int64
	lastTempUnschedUntil  time.Time
	lastTempUnschedReason string
	updateExtraErr        error
}

func (r *grokQuotaAccountRepo) UpdateExtra(_ context.Context, id int64, updates map[string]any) error {
	r.updateCalls++
	if r.updateExtraErr != nil {
		return r.updateExtraErr
	}
	if r.updates == nil {
		r.updates = make(map[int64]map[string]any)
	}
	r.updates[id] = updates
	return nil
}

func (r *grokQuotaAccountRepo) SetRateLimited(_ context.Context, id int64, resetAt time.Time) error {
	r.rateLimitedCalls++
	r.lastRateLimitedID = id
	r.lastRateLimitResetAt = resetAt
	return nil
}

func (r *grokQuotaAccountRepo) SetRateLimitedIfLater(ctx context.Context, id int64, resetAt time.Time) error {
	return r.SetRateLimited(ctx, id, resetAt)
}

func TestGrokQuotaServiceProbeUsageReportsPersistenceWarning(t *testing.T) {
	t.Parallel()
	account := &Account{
		ID: 45, Platform: PlatformGrok, Type: AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token": "access-token",
			"expires_at":   time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		},
	}
	repo := &grokQuotaAccountRepo{
		mockAccountRepoForPlatform: &mockAccountRepoForPlatform{accountsByID: map[int64]*Account{45: account}},
		updateExtraErr:             errors.New("database unavailable"),
	}
	upstream := &grokBillingTestUpstream{
		weekly:  grokBillingTestResponse{status: http.StatusOK, body: `{"config":{"currentPeriod":{"type":"WEEKLY","end":"2026-07-16T03:25:00Z"},"creditUsagePercent":10}}`},
		monthly: grokBillingTestResponse{status: http.StatusOK, body: `{"config":{"monthlyLimit":{"val":15000},"used":{"val":10},"billingPeriodEnd":"2026-08-01T00:00:00Z"}}`},
	}

	result, err := NewGrokQuotaService(repo, nil, NewGrokTokenProvider(repo, nil), upstream).ProbeUsage(context.Background(), 45)
	require.NoError(t, err)
	require.False(t, result.Persisted)
	require.NotNil(t, result.Billing)
}

func (r *grokQuotaAccountRepo) SetTempUnschedulable(_ context.Context, id int64, until time.Time, reason string) error {
	r.tempUnschedCalls++
	r.lastTempUnschedID = id
	r.lastTempUnschedUntil = until
	r.lastTempUnschedReason = reason
	return nil
}

type grokQuotaProxyRepo struct {
	proxyRepoStub
	proxies map[int64]*Proxy
	calls   int
}

type grokBillingTestResponse struct {
	status int
	body   string
}

type grokBillingTestUpstream struct {
	mu       sync.Mutex
	weekly   grokBillingTestResponse
	monthly  grokBillingTestResponse
	requests []*http.Request
	proxyURL string
}

type grokUsageLogRepo struct {
	UsageLogRepository
	stats  *usagestats.AccountStats
	starts []time.Time
}

func (r *grokUsageLogRepo) GetAccountWindowStats(_ context.Context, _ int64, start time.Time) (*usagestats.AccountStats, error) {
	r.starts = append(r.starts, start)
	return r.stats, nil
}

func (u *grokBillingTestUpstream) Do(req *http.Request, proxyURL string, _ int64, _ int) (*http.Response, error) {
	u.mu.Lock()
	u.requests = append(u.requests, req)
	u.proxyURL = proxyURL
	u.mu.Unlock()

	response := u.monthly
	if strings.Contains(req.URL.RawQuery, "format=credits") {
		response = u.weekly
	}
	return &http.Response{
		StatusCode: response.status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(response.body)),
	}, nil
}

func (u *grokBillingTestUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, accountConcurrency)
}

func (r *grokQuotaProxyRepo) GetByID(_ context.Context, id int64) (*Proxy, error) {
	r.calls++
	return r.proxies[id], nil
}

func TestGrokQuotaServiceProbeUsageStoresBilling(t *testing.T) {
	t.Parallel()

	account := &Account{
		ID:          42,
		Platform:    PlatformGrok,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token": "access-token",
			"expires_at":   time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		},
	}
	repo := &grokQuotaAccountRepo{
		mockAccountRepoForPlatform: &mockAccountRepoForPlatform{
			accountsByID: map[int64]*Account{42: account},
		},
	}
	weeklyBody := `{"config":{"currentPeriod":{"type":"WEEKLY","start":"2026-07-09T03:25:00Z","end":"2026-07-16T03:25:00Z"},"creditUsagePercent":2,"productUsage":[{"product":"Api","usagePercent":2}]}}`
	monthlyBody := `{"config":{"monthlyLimit":{"val":15000},"used":{"val":78},"billingPeriodStart":"2026-07-01T00:00:00Z","billingPeriodEnd":"2026-08-01T00:00:00Z"}}`
	upstream := &grokBillingTestUpstream{
		weekly:  grokBillingTestResponse{status: http.StatusOK, body: weeklyBody},
		monthly: grokBillingTestResponse{status: http.StatusOK, body: monthlyBody},
	}
	svc := NewGrokQuotaService(repo, nil, NewGrokTokenProvider(repo, nil), upstream)

	result, err := svc.ProbeUsage(context.Background(), 42)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, result.StatusCode)
	require.NotNil(t, result.Billing)
	require.Equal(t, "weekly", result.Billing.PeriodType)
	require.InDelta(t, 2.0, *result.Billing.UsagePercent, 1e-9)
	require.Equal(t, "SuperGrok", result.Billing.Plan)
	require.InDelta(t, 15000, *result.Billing.MonthlyLimitCents, 1e-9)
	require.Len(t, upstream.requests, 2)
	requestByURL := make(map[string]*http.Request, len(upstream.requests))
	for _, request := range upstream.requests {
		requestByURL[request.URL.String()] = request
	}
	weeklyRequest := requestByURL["https://cli-chat-proxy.grok.com/v1/billing?format=credits"]
	require.NotNil(t, weeklyRequest)
	require.NotNil(t, requestByURL["https://cli-chat-proxy.grok.com/v1/billing"])
	require.Equal(t, "Bearer access-token", weeklyRequest.Header.Get("Authorization"))
	require.Equal(t, xai.CLITokenAuthValue, weeklyRequest.Header.Get(xai.CLITokenAuthHeader))
	require.Equal(t, xai.CLIClientVersion, weeklyRequest.Header.Get(xai.CLIClientVersionHeader))
	require.NotNil(t, repo.updates[42][grokBillingExtraKey])
}

func TestGrokQuotaServiceProbeUsagePrefersSuccessfulStatus(t *testing.T) {
	t.Parallel()

	account := &Account{
		ID:          43,
		Platform:    PlatformGrok,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token": "access-token",
			"expires_at":   time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		},
	}
	repo := &grokQuotaAccountRepo{
		mockAccountRepoForPlatform: &mockAccountRepoForPlatform{
			accountsByID: map[int64]*Account{43: account},
		},
	}
	// Weekly fails with 429; monthly succeeds — status must stay 200.
	monthlyBody := `{"config":{"monthlyLimit":{"val":15000},"used":{"val":78},"billingPeriodStart":"2026-07-01T00:00:00Z","billingPeriodEnd":"2026-08-01T00:00:00Z"}}`
	upstream := &grokBillingTestUpstream{
		weekly:  grokBillingTestResponse{status: http.StatusTooManyRequests, body: `rate limited`},
		monthly: grokBillingTestResponse{status: http.StatusOK, body: monthlyBody},
	}
	svc := NewGrokQuotaService(repo, nil, NewGrokTokenProvider(repo, nil), upstream)

	result, err := svc.ProbeUsage(context.Background(), 43)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, result.StatusCode)
	require.NotNil(t, result.Billing)
	require.InDelta(t, 15000, *result.Billing.MonthlyLimitCents, 1e-9)
}

func TestGrokQuotaServiceProbeUsageDual429MapsRateLimit(t *testing.T) {
	t.Parallel()

	account := &Account{
		ID:          44,
		Platform:    PlatformGrok,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token": "access-token",
			"expires_at":   time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		},
	}
	repo := &grokQuotaAccountRepo{
		mockAccountRepoForPlatform: &mockAccountRepoForPlatform{
			accountsByID: map[int64]*Account{44: account},
		},
	}
	upstream := &grokBillingTestUpstream{
		weekly:  grokBillingTestResponse{status: http.StatusTooManyRequests, body: `rate limited`},
		monthly: grokBillingTestResponse{status: http.StatusTooManyRequests, body: `rate limited`},
	}
	svc := NewGrokQuotaService(repo, nil, NewGrokTokenProvider(repo, nil), upstream)

	_, err := svc.ProbeUsage(context.Background(), 44)
	require.Error(t, err)
	var apiErr *infraerrors.ApplicationError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, http.StatusTooManyRequests, int(apiErr.Code))
	require.Equal(t, "GROK_QUOTA_PROBE_UPSTREAM_ERROR", apiErr.Reason)
}

func TestGrokQuotaServiceProbeUsageMixedFailuresReturnPartsFailed(t *testing.T) {
	t.Parallel()

	account := &Account{
		ID: 47, Platform: PlatformGrok, Type: AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token": "access-token",
			"expires_at":   time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		},
	}
	repo := &grokQuotaAccountRepo{
		mockAccountRepoForPlatform: &mockAccountRepoForPlatform{accountsByID: map[int64]*Account{47: account}},
	}
	upstream := &grokBillingTestUpstream{
		weekly:  grokBillingTestResponse{status: http.StatusTooManyRequests, body: `rate limited`},
		monthly: grokBillingTestResponse{status: http.StatusUnauthorized, body: `{"error":"unauthorized"}`},
	}

	_, err := NewGrokQuotaService(repo, nil, NewGrokTokenProvider(repo, nil), upstream).ProbeUsage(context.Background(), 47)
	require.Error(t, err)
	var apiErr *infraerrors.ApplicationError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, http.StatusBadGateway, int(apiErr.Code))
	require.Equal(t, "GROK_QUOTA_PROBE_PARTS_FAILED", apiErr.Reason)
	require.Equal(t, "429", apiErr.Metadata["weekly_status"])
	require.Equal(t, "401", apiErr.Metadata["monthly_status"])
}

func TestGrokQuotaServiceProbeUsageUpstreamError(t *testing.T) {
	t.Parallel()

	account := &Account{
		ID:          48,
		Platform:    PlatformGrok,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token": "access-token",
			"expires_at":   time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		},
	}
	repo := &grokQuotaAccountRepo{
		mockAccountRepoForPlatform: &mockAccountRepoForPlatform{
			accountsByID: map[int64]*Account{48: account},
		},
	}
	upstream := &grokBillingTestUpstream{
		weekly:  grokBillingTestResponse{status: http.StatusUnauthorized, body: `{"error":"unauthorized"}`},
		monthly: grokBillingTestResponse{status: http.StatusUnauthorized, body: `{"error":"unauthorized"}`},
	}
	svc := NewGrokQuotaService(repo, nil, NewGrokTokenProvider(repo, nil), upstream)

	_, err := svc.ProbeUsage(context.Background(), 48)
	require.Error(t, err)
	var apiErr *infraerrors.ApplicationError
	require.ErrorAs(t, err, &apiErr)
}

func TestGrokQuotaServiceResetQuotaUnsupported(t *testing.T) {
	t.Parallel()

	account := &Account{
		ID:       49,
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token": "access-token",
			"expires_at":   time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		},
	}
	repo := &grokQuotaAccountRepo{
		mockAccountRepoForPlatform: &mockAccountRepoForPlatform{
			accountsByID: map[int64]*Account{49: account},
		},
	}
	svc := NewGrokQuotaService(repo, nil, NewGrokTokenProvider(repo, nil), &httpUpstreamRecorder{})
	_, err := svc.ResetQuota(context.Background(), 49)
	require.Error(t, err)
	var apiErr *infraerrors.ApplicationError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, "GROK_QUOTA_RESET_UNSUPPORTED", apiErr.Reason)
}

func TestPreferSuccessfulBillingStatus(t *testing.T) {
	t.Parallel()
	require.Equal(t, 200, preferSuccessfulBillingStatus(429, 200, false, true))
	require.Equal(t, 200, preferSuccessfulBillingStatus(200, 429, true, false))
	require.Equal(t, 429, preferSuccessfulBillingStatus(429, 0, false, false))
	require.Equal(t, 401, preferSuccessfulBillingStatus(401, 500, false, false))
}

func TestGrokBillingDoesNotAutoPauseWithoutUpstreamRejection(t *testing.T) {
	t.Parallel()

	pct := 100.0
	account := &Account{
		ID:       50,
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			grokBillingExtraKey: map[string]any{
				"usage_percent": pct,
				"period_end":    time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
				"updated_at":    time.Now().UTC().Format(time.RFC3339),
			},
		},
	}
	paused, _ := shouldAutoPauseGrokAccountByQuota(account)
	require.False(t, paused)
}

func TestGrokQuotaServiceProbeUsageLoadsProxyWhenAccountEdgeMissing(t *testing.T) {
	t.Parallel()

	proxyID := int64(7)
	account := &Account{
		ID:          46,
		Platform:    PlatformGrok,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		ProxyID:     &proxyID,
		Credentials: map[string]any{
			"access_token": "access-token",
			"expires_at":   time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		},
	}
	repo := &grokQuotaAccountRepo{
		mockAccountRepoForPlatform: &mockAccountRepoForPlatform{
			accountsByID: map[int64]*Account{46: account},
		},
	}
	proxyRepo := &grokQuotaProxyRepo{
		proxies: map[int64]*Proxy{
			proxyID: {
				ID:       proxyID,
				Protocol: "http",
				Host:     "proxy.test",
				Port:     3128,
			},
		},
	}
	weeklyBody := `{"config":{"currentPeriod":{"type":"WEEKLY","start":"2026-07-09T03:25:00Z","end":"2026-07-16T03:25:00Z"},"creditUsagePercent":1}}`
	monthlyBody := `{"config":{"monthlyLimit":{"val":15000},"used":{"val":10},"billingPeriodStart":"2026-07-01T00:00:00Z","billingPeriodEnd":"2026-08-01T00:00:00Z"}}`
	upstream := &grokBillingTestUpstream{
		weekly:  grokBillingTestResponse{status: http.StatusOK, body: weeklyBody},
		monthly: grokBillingTestResponse{status: http.StatusOK, body: monthlyBody},
	}
	svc := NewGrokQuotaService(repo, proxyRepo, NewGrokTokenProvider(repo, nil), upstream)

	_, err := svc.ProbeUsage(context.Background(), 46)
	require.NoError(t, err)
	require.Equal(t, 1, proxyRepo.calls)
	require.Equal(t, "http://proxy.test:3128", upstream.proxyURL)
}

func TestShouldAutoPauseGrokAccountByQuotaHeaders(t *testing.T) {
	t.Parallel()

	zero := int64(0)
	limit := int64(10)
	resetFuture := time.Now().Add(time.Minute).Unix()
	retryAfter := 30
	tests := []struct {
		name     string
		snapshot xai.QuotaSnapshot
		want     bool
	}{
		{
			name: "remaining requests exhausted",
			snapshot: xai.QuotaSnapshot{
				Requests:  &xai.QuotaWindow{Limit: &limit, Remaining: &zero, ResetUnix: &resetFuture},
				UpdatedAt: time.Now().UTC().Format(time.RFC3339),
			},
			want: true,
		},
		{
			name: "retry after active",
			snapshot: xai.QuotaSnapshot{
				RetryAfterSeconds: &retryAfter,
				UpdatedAt:         time.Now().UTC().Format(time.RFC3339),
			},
			want: true,
		},
		{
			name: "retry after expired",
			snapshot: xai.QuotaSnapshot{
				RetryAfterSeconds: &retryAfter,
				UpdatedAt:         time.Now().Add(-time.Duration(retryAfter+1) * time.Second).UTC().Format(time.RFC3339),
			},
			want: false,
		},
		{
			name: "stale snapshot ignored",
			snapshot: xai.QuotaSnapshot{
				Requests:  &xai.QuotaWindow{Limit: &limit, Remaining: &zero, ResetUnix: &resetFuture},
				UpdatedAt: time.Now().Add(-3 * time.Hour).UTC().Format(time.RFC3339),
			},
			want: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			account := &Account{
				Platform: PlatformGrok,
				Type:     AccountTypeOAuth,
				Extra: map[string]any{
					grokQuotaSnapshotExtraKey: tt.snapshot,
				},
			}
			got, _ := shouldAutoPauseGrokAccountByQuota(account)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestGrokBillingSnapshotNeedsRefresh(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	account := &Account{Extra: map[string]any{}}
	require.True(t, grokBillingSnapshotNeedsRefresh(account, now))

	account.Extra[grokBillingExtraKey] = map[string]any{
		"updated_at": now.Add(-5 * time.Minute).Format(time.RFC3339),
	}
	require.False(t, grokBillingSnapshotNeedsRefresh(account, now))

	account.Extra[grokBillingExtraKey] = map[string]any{
		"updated_at": now.Add(-11 * time.Minute).Format(time.RFC3339),
	}
	require.True(t, grokBillingSnapshotNeedsRefresh(account, now))
}

func TestShouldProbeGrokBillingThrottlesAutomaticRetries(t *testing.T) {
	t.Parallel()
	now := time.Now()
	svc := &AccountUsageService{cache: NewUsageCache()}
	require.True(t, svc.shouldProbeGrokBilling(42, now, false))
	require.False(t, svc.shouldProbeGrokBilling(42, now.Add(30*time.Second), false))
	require.True(t, svc.shouldProbeGrokBilling(42, now.Add(30*time.Second), true))
	require.True(t, svc.shouldProbeGrokBilling(42, now.Add(2*time.Minute), false))
}

func TestCurrentGrokBillingWindow(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	billing := &xai.BillingSummary{
		PeriodType:         "weekly",
		PeriodStart:        "2026-07-09T03:25:00Z",
		PeriodEnd:          "2026-07-16T03:25:00Z",
		BillingPeriodStart: "2026-07-01T00:00:00Z",
		BillingPeriodEnd:   "2026-08-01T00:00:00Z",
	}

	start, ok := currentGrokBillingWindow(billing, true, now)
	require.True(t, ok)
	require.Equal(t, time.Date(2026, 7, 9, 3, 25, 0, 0, time.UTC), start)
	start, ok = currentGrokBillingWindow(billing, false, now)
	require.True(t, ok)
	require.Equal(t, time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), start)

	billing.PeriodEnd = "2026-07-11T03:25:00Z"
	_, ok = currentGrokBillingWindow(billing, true, now)
	require.False(t, ok)
	billing.BillingPeriodEnd = "2026-07-12T12:00:00Z"
	_, ok = currentGrokBillingWindow(billing, false, now)
	require.False(t, ok)
}

func TestGrokLocalUsageForBillingReturnsZeroForExpiredWindows(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	repo := &grokUsageLogRepo{stats: &usagestats.AccountStats{Requests: 9}}
	billing := &xai.BillingSummary{
		PeriodType:         "weekly",
		PeriodStart:        "2026-07-01T00:00:00Z",
		PeriodEnd:          "2026-07-08T00:00:00Z",
		BillingPeriodStart: "2026-06-01T00:00:00Z",
		BillingPeriodEnd:   "2026-07-01T00:00:00Z",
	}

	weekly, monthly := grokLocalUsageForBilling(context.Background(), repo, 42, billing, now)
	require.EqualValues(t, 0, weekly.Requests)
	require.EqualValues(t, 0, monthly.Requests)
	require.Empty(t, repo.starts)
}

func TestGrokLocalUsageForBillingQueriesCurrentWindows(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	repo := &grokUsageLogRepo{stats: &usagestats.AccountStats{Requests: 9}}
	billing := &xai.BillingSummary{
		PeriodType:         "weekly",
		PeriodStart:        "2026-07-09T00:00:00Z",
		PeriodEnd:          "2026-07-16T00:00:00Z",
		BillingPeriodStart: "2026-07-01T00:00:00Z",
		BillingPeriodEnd:   "2026-08-01T00:00:00Z",
	}

	weekly, monthly := grokLocalUsageForBilling(context.Background(), repo, 42, billing, now)
	require.EqualValues(t, 9, weekly.Requests)
	require.EqualValues(t, 9, monthly.Requests)
	require.Len(t, repo.starts, 2)
}

func TestGetGrokUsageProbeFailureOverridesQuotaUnknown(t *testing.T) {
	t.Parallel()
	account := &Account{
		ID: 51, Platform: PlatformGrok, Type: AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token": "access-token",
			"expires_at":   time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		},
	}
	repo := &grokQuotaAccountRepo{
		mockAccountRepoForPlatform: &mockAccountRepoForPlatform{accountsByID: map[int64]*Account{51: account}},
	}
	upstream := &grokBillingTestUpstream{
		weekly:  grokBillingTestResponse{status: http.StatusInternalServerError, body: `failed`},
		monthly: grokBillingTestResponse{status: http.StatusInternalServerError, body: `failed`},
	}
	quotaService := NewGrokQuotaService(repo, nil, NewGrokTokenProvider(repo, nil), upstream)
	usageService := &AccountUsageService{grokQuotaFetcher: NewGrokQuotaFetcher(), grokQuotaService: quotaService}

	usage, err := usageService.getGrokUsage(context.Background(), account, false)
	require.NoError(t, err)
	require.Equal(t, "quota_refresh_failed", usage.ErrorCode)
	require.Equal(t, "Grok quota refresh failed", usage.Error)
}
