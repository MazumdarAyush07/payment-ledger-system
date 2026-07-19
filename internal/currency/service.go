package currency

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

/*
Fetcher is the interface used to make HTTP requests to the exchange rate API.
Defining it as an interface (rather than using http.Client directly) allows
tests to inject a mock that returns canned responses without making real
network calls.
*/
type Fetcher interface {
	Do(req *http.Request) (*http.Response, error)
}

/*
Rate holds the result of a single currency conversion lookup.
RateSource is "live" when fetched from the API, "stale_cache" when the
primary API was down but a cached rate within the 24-hour window was used.
*/
type Rate struct {
	From       string
	To         string
	Value      float64
	FetchedAt  time.Time
	RateSource string // "live" or "stale_cache"
}

/*
cachedRate is a single entry stored inside the in-memory cache.
It bundles the rate value with the time it was fetched so TTL checks work.
*/
type cachedRate struct {
	value     float64
	fetchedAt time.Time
}

/*
RateService fetches exchange rates from frankfurter.app and serves them with
a layered fallback strategy:

  1. Serve from in-memory cache if the cached value is < 1 hour old.
  2. Fetch from the live API (3-second timeout) if the cache is stale.
  3. On API failure, serve the stale cache if it is < 24 hours old
     and mark RateSource = "stale_cache".
  4. If no cache or cache is ≥ 24 hours old and the API is down,
     return ErrRateUnavailable.

The zero dependency rule: this package imports nothing from internal/ledger
or internal/api. It is a self-contained utility.
*/
type RateService struct {
	fetcher  Fetcher
	mu       sync.Mutex         // guards cache
	cache    map[string]cachedRate
	liveTTL  time.Duration      // how long a "live" cache entry is valid (1 hour)
	staleTTL time.Duration      // maximum age before we refuse to serve stale data (24 hours)
	baseURL  string
}

/*
NewRateService constructs a RateService with production defaults.
Pass a custom Fetcher to override the HTTP client (used in tests).
*/
func NewRateService(fetcher Fetcher) *RateService {
	if fetcher == nil {
		fetcher = &http.Client{Timeout: 3 * time.Second}
	}
	return &RateService{
		fetcher:  fetcher,
		cache:    make(map[string]cachedRate),
		liveTTL:  1 * time.Hour,
		staleTTL: 24 * time.Hour,
		baseURL:  "https://api.frankfurter.app",
	}
}

/*
SetTTLs overrides the live and stale TTLs on a RateService.
This is exported for use in tests — it allows forcing cache expiry
immediately (liveTTL=0) or making the stale window expire instantly
(staleTTL=0) without real time.Sleep calls.
*/
func SetTTLs(s *RateService, live, stale time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.liveTTL = live
	s.staleTTL = stale
}

/*
GetRate returns the exchange rate for converting one unit of `from` into `to`.

Fallback order:
 1. Fresh cache (age < liveTTL) → return cached value, RateSource = "live"
 2. Stale but usable cache (age < staleTTL) after API failure → RateSource = "stale_cache"
 3. No usable cache and API down → ErrRateUnavailable
*/
func (s *RateService) GetRate(ctx context.Context, from, to string) (Rate, error) {
	if from == to {
		return Rate{From: from, To: to, Value: 1.0, FetchedAt: time.Now(), RateSource: "live"}, nil
	}

	cacheKey := from + "_" + to

	/* Step 1: check if we have a fresh cached rate. */
	s.mu.Lock()
	entry, ok := s.cache[cacheKey]
	s.mu.Unlock()

	if ok && time.Since(entry.fetchedAt) < s.liveTTL {
		return Rate{
			From:       from,
			To:         to,
			Value:      entry.value,
			FetchedAt:  entry.fetchedAt,
			RateSource: "live",
		}, nil
	}

	/* Step 2: cache is stale or empty — try the live API. */
	value, fetchedAt, err := s.fetchLive(ctx, from, to)
	if err == nil {
		/* API succeeded — update cache and return fresh rate. */
		s.mu.Lock()
		s.cache[cacheKey] = cachedRate{value: value, fetchedAt: fetchedAt}
		s.mu.Unlock()
		return Rate{
			From:       from,
			To:         to,
			Value:      value,
			FetchedAt:  fetchedAt,
			RateSource: "live",
		}, nil
	}

	/* Step 3: API failed — serve stale cache if it is young enough. */
	if ok && time.Since(entry.fetchedAt) < s.staleTTL {
		return Rate{
			From:       from,
			To:         to,
			Value:      entry.value,
			FetchedAt:  entry.fetchedAt,
			RateSource: "stale_cache",
		}, nil
	}

	/* Step 4: nothing usable — hard fail. */
	return Rate{}, fmt.Errorf("%w: %s→%s: %v", ErrRateUnavailable, from, to, err)
}

/*
Convert applies the exchange rate to `amount` (in minor units of `from`)
and returns the equivalent amount in minor units of `to`.

Cross-currency conversion must happen before entries are built because the
ledger engine only accepts balanced entries in a single currency per transaction.

Example: converting ₹500 (amount=50000 paise) to USD at rate 0.012
  result = int64(50000 * 0.012) = 600 (cents)
*/
func (s *RateService) Convert(ctx context.Context, amount int64, from, to string) (int64, Rate, error) {
	rate, err := s.GetRate(ctx, from, to)
	if err != nil {
		return 0, Rate{}, err
	}
	converted := int64(float64(amount) * rate.Value)
	return converted, rate, nil
}

/* frankfurterResponse is the JSON shape returned by api.frankfurter.app/latest. */
type frankfurterResponse struct {
	Base  string             `json:"base"`
	Date  string             `json:"date"`
	Rates map[string]float64 `json:"rates"`
}

/*
fetchLive makes a single HTTP GET to frankfurter.app.
The caller's context carries the 3-second deadline set on the http.Client.
Returns the exchange rate value and the time it was fetched.
*/
func (s *RateService) fetchLive(ctx context.Context, from, to string) (float64, time.Time, error) {
	url := fmt.Sprintf("%s/latest?from=%s&to=%s", s.baseURL, from, to)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("currency: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := s.fetcher.Do(req)
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("currency: fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, time.Time{}, fmt.Errorf("currency: unexpected status %d from %s", resp.StatusCode, url)
	}

	var body frankfurterResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return 0, time.Time{}, fmt.Errorf("currency: decode response: %w", err)
	}

	value, ok := body.Rates[to]
	if !ok {
		return 0, time.Time{}, fmt.Errorf("currency: rate for %s not in response", to)
	}

	return value, time.Now(), nil
}
