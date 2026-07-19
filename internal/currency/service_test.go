package currency_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/ayushmazumdar/payment-ledger/internal/currency"
)

/* TestRateService is the single entry point for all RateService tests. */
func TestRateService(t *testing.T) {
	t.Run("SameCurrency_ReturnsOneWithoutFetch", testSameCurrency)
	t.Run("LiveFetch_Success", testLiveFetchSuccess)
	t.Run("LiveFetch_CacheHit", testLiveFetchCacheHit)
	t.Run("APIDown_StaleCache_Served", testAPIDownStaleCacheServed)
	t.Run("APIDown_CacheTooOld_HardFail", testAPIDownCacheTooOld)
	t.Run("APIDown_NoCache_HardFail", testAPIDownNoCache)
	t.Run("Convert_AppliesRate", testConvertAppliesRate)
}

// ── Mock HTTP client ──────────────────────────────────────────────────────────

/*
mockFetcher implements currency.Fetcher. Each call to Do pops the next
response from the queue. This lets tests define a sequence of API responses
(e.g. first call succeeds, second fails) without making real network calls.
*/
type mockFetcher struct {
	responses []*http.Response
	errs      []error
	callCount int
}

func (m *mockFetcher) Do(_ *http.Request) (*http.Response, error) {
	i := m.callCount
	m.callCount++
	if i >= len(m.responses) {
		return nil, errors.New("mock: no more responses configured")
	}
	return m.responses[i], m.errs[i]
}

/* okResponse builds a 200 JSON response body with the given rate for `to` currency. */
func okResponse(to string, rate float64) *http.Response {
	body := fmt.Sprintf(`{"base":"USD","date":"2026-01-01","rates":{%q:%g}}`, to, rate)
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewBufferString(body)),
	}
}

/* networkErr builds a (nil, error) pair simulating a network failure. */
func networkErr() (*http.Response, error) {
	return nil, errors.New("mock: network error")
}

// ── Helper to build a RateService with controlled TTLs ───────────────────────

/*
newTestService builds a RateService with overridden TTLs.
liveTTL=0  → cache is immediately stale after any fetch.
staleTTL=0 → stale cache is also immediately expired (hard fail on API down).
*/
func newTestService(fetcher currency.Fetcher, liveTTL, staleTTL time.Duration) *currency.RateService {
	svc := currency.NewRateService(fetcher)
	currency.SetTTLs(svc, liveTTL, staleTTL)
	return svc
}

// ── Tests ─────────────────────────────────────────────────────────────────────

func testSameCurrency(t *testing.T) {
	/* nil fetcher: NewRateService will use the real http.Client but it must not be called. */
	svc := newTestService(nil, 1*time.Hour, 24*time.Hour)

	rate, err := svc.GetRate(context.Background(), "INR", "INR")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rate.Value != 1.0 {
		t.Errorf("expected rate 1.0 for same currency, got %f", rate.Value)
	}
	if rate.RateSource != "live" {
		t.Errorf("expected RateSource=live, got %s", rate.RateSource)
	}
}

func testLiveFetchSuccess(t *testing.T) {
	resp, respErr := okResponse("INR", 83.5), error(nil)
	mock := &mockFetcher{
		responses: []*http.Response{resp},
		errs:      []error{respErr},
	}
	svc := newTestService(mock, 1*time.Hour, 24*time.Hour)

	rate, err := svc.GetRate(context.Background(), "USD", "INR")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rate.Value != 83.5 {
		t.Errorf("expected rate 83.5, got %f", rate.Value)
	}
	if rate.RateSource != "live" {
		t.Errorf("expected RateSource=live, got %s", rate.RateSource)
	}
	if mock.callCount != 1 {
		t.Errorf("expected 1 HTTP call, got %d", mock.callCount)
	}
}

func testLiveFetchCacheHit(t *testing.T) {
	mock := &mockFetcher{
		responses: []*http.Response{okResponse("INR", 83.5)},
		errs:      []error{nil},
	}
	/* liveTTL = 1 hour — cache is fresh after the first call. */
	svc := newTestService(mock, 1*time.Hour, 24*time.Hour)

	/* First call: fetches from API and populates cache. */
	if _, err := svc.GetRate(context.Background(), "USD", "INR"); err != nil {
		t.Fatalf("first call: %v", err)
	}

	/* Second call: must hit the cache — no additional HTTP request. */
	rate, err := svc.GetRate(context.Background(), "USD", "INR")
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if rate.Value != 83.5 {
		t.Errorf("expected cached rate 83.5, got %f", rate.Value)
	}
	if mock.callCount != 1 {
		t.Errorf("expected 1 HTTP call (cache hit on second), got %d", mock.callCount)
	}
}

func testAPIDownStaleCacheServed(t *testing.T) {
	r1 := okResponse("INR", 83.5)
	r2, e2 := networkErr()
	mock := &mockFetcher{
		responses: []*http.Response{r1, r2},
		errs:      []error{nil, e2},
	}
	/*
		liveTTL=0 → cache is stale immediately after first call.
		staleTTL=24h → stale cache is still within the acceptable window.
	*/
	svc := newTestService(mock, 0, 24*time.Hour)

	/* First call: populates cache. */
	if _, err := svc.GetRate(context.Background(), "USD", "INR"); err != nil {
		t.Fatalf("first call: %v", err)
	}

	/* Second call: stale cache + API down → serve stale. */
	rate, err := svc.GetRate(context.Background(), "USD", "INR")
	if err != nil {
		t.Fatalf("expected stale cache to be served, got error: %v", err)
	}
	if rate.RateSource != "stale_cache" {
		t.Errorf("expected RateSource=stale_cache, got %s", rate.RateSource)
	}
	if rate.Value != 83.5 {
		t.Errorf("expected stale rate 83.5, got %f", rate.Value)
	}
}

func testAPIDownCacheTooOld(t *testing.T) {
	r1 := okResponse("INR", 83.5)
	r2, e2 := networkErr()
	mock := &mockFetcher{
		responses: []*http.Response{r1, r2},
		errs:      []error{nil, e2},
	}
	/*
		liveTTL=0 AND staleTTL=0 → both windows expire immediately.
		Second call: stale cache exists but is too old → hard fail.
	*/
	svc := newTestService(mock, 0, 0)

	if _, err := svc.GetRate(context.Background(), "USD", "INR"); err != nil {
		t.Fatalf("first call: %v", err)
	}

	_, err := svc.GetRate(context.Background(), "USD", "INR")
	if !errors.Is(err, currency.ErrRateUnavailable) {
		t.Errorf("expected ErrRateUnavailable, got %v", err)
	}
}

func testAPIDownNoCache(t *testing.T) {
	r1, e1 := networkErr()
	mock := &mockFetcher{
		responses: []*http.Response{r1},
		errs:      []error{e1},
	}
	svc := newTestService(mock, 1*time.Hour, 24*time.Hour)

	/* First call: API down, no cache at all → hard fail immediately. */
	_, err := svc.GetRate(context.Background(), "USD", "INR")
	if !errors.Is(err, currency.ErrRateUnavailable) {
		t.Errorf("expected ErrRateUnavailable, got %v", err)
	}
}

func testConvertAppliesRate(t *testing.T) {
	mock := &mockFetcher{
		responses: []*http.Response{okResponse("INR", 83.5)},
		errs:      []error{nil},
	}
	svc := newTestService(mock, 1*time.Hour, 24*time.Hour)

	/*
		Convert 100 USD cents ($1.00) at rate 83.5 to INR paise.
		int64(100 * 83.5) = 8350 paise = ₹83.50
	*/
	converted, rate, err := svc.Convert(context.Background(), 100, "USD", "INR")
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if converted != 8350 {
		t.Errorf("expected 8350 paise, got %d", converted)
	}
	if rate.Value != 83.5 {
		t.Errorf("expected rate 83.5, got %f", rate.Value)
	}
}
