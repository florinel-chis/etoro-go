package backtestsource

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/florinel-chis/gobacktest/source"

	etoro "github.com/florinel-chis/etoro-go"
)

// rewriteTransport redirects every request's scheme+host to an httptest
// server while leaving the path and query untouched. The etoro package
// exposes no way to override its default host from outside the package, so
// tests route through the client's public WithHTTPClient option instead.
type rewriteTransport struct{ srv *httptest.Server }

func (t rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	u, err := url.Parse(t.srv.URL)
	if err != nil {
		return nil, err
	}
	req = req.Clone(req.Context())
	req.URL.Scheme = u.Scheme
	req.URL.Host = u.Host
	return http.DefaultTransport.RoundTrip(req)
}

func testClient(srv *httptest.Server) *etoro.Client {
	return etoro.New("test-api-key", "test-user-key",
		etoro.WithHTTPClient(&http.Client{Transport: rewriteTransport{srv: srv}}))
}

// newSource wraps New and pins the source's clock so bar-count arithmetic
// is deterministic in tests.
func newSource(c *etoro.Client, now time.Time, opts ...Option) source.Source {
	s := New(c, opts...)
	s.(*src).now = func() time.Time { return now }
	return s
}

// searchAAPL mimics the live search response for "AAPL": contains-matching
// returns sibling listings alongside the exact symbol, and each item
// carries the instrumentId key twice (an observed quirk of the endpoint;
// encoding/json keeps the last value).
const searchAAPL = `{"page":1,"pageSize":10,"totalItems":3,"items":[
	{"displayname":"Apple","internalSymbolFull":"AAPL","instrumentId":1001,"isCurrentlyTradable":true,"instrumentId":1001},
	{"displayname":"Apple 24/7","internalSymbolFull":"AAPL.24-7","instrumentId":15569,"isCurrentlyTradable":true,"instrumentId":15569},
	{"displayname":"Apple EUR","internalSymbolFull":"AAPL.EUR","instrumentId":14254,"isCurrentlyTradable":true,"instrumentId":14254}
]}`

// dailyCandlesJSON renders a candles envelope with one daily bar per day
// in [first, first+days), ascending. Bar i closes at 100+i.
func dailyCandlesJSON(id int, first time.Time, days int) string {
	items := make([]string, days)
	for i := range days {
		t := first.AddDate(0, 0, i).Format(time.RFC3339)
		items[i] = fmt.Sprintf(
			`{"instrumentID":%d,"fromDate":%q,"open":%d,"high":%d,"low":%d,"close":%d,"volume":1000}`,
			id, t, 99+i, 101+i, 98+i, 100+i)
	}
	return fmt.Sprintf(`{"interval":"OneDay","candles":[{"instrumentId":%d,"candles":[%s]}]}`,
		id, strings.Join(items, ","))
}

// candleServer serves the AAPL search fixture and daily candles spanning
// [firstBar, firstBar+days) for instrument 1001, counting hits per route.
func candleServer(firstBar time.Time, days int, searchHits, candleHits *int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/market-data/search":
			if searchHits != nil {
				*searchHits++
			}
			fmt.Fprint(w, searchAAPL)
		case strings.Contains(r.URL.Path, "/history/candles/"):
			if candleHits != nil {
				*candleHits++
			}
			fmt.Fprint(w, dailyCandlesJSON(1001, firstBar, days))
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestFetchHappyPath(t *testing.T) {
	now := time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC)
	srv := candleServer(now.AddDate(0, 0, -3), 4, nil, nil) // bars 07-13..07-16
	defer srv.Close()

	s := newSource(testClient(srv), now)
	d, err := s.Fetch(context.Background(), "AAPL", now.AddDate(0, 0, -3), now, source.D1)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	// [start, end) drops the bar stamped at end, and the newest bar is the
	// still-forming session (its period has not elapsed), so the 07-16 bar
	// is excluded twice over while 07-13..07-15 survive (07-15 closes
	// exactly at now, so it is complete and kept).
	if d.Len() != 3 {
		t.Fatalf("got %d bars, want 3", d.Len())
	}
	if got := d.Close().Last(); got != 102 {
		t.Errorf("last close = %v, want 102", got)
	}
	times := d.Time()
	if !times[0].Equal(now.AddDate(0, 0, -3)) || !times[2].Equal(now.AddDate(0, 0, -1)) {
		t.Errorf("bar times = %v..%v, want %v..%v ascending",
			times[0], times[2], now.AddDate(0, 0, -3), now.AddDate(0, 0, -1))
	}
}

func TestFetchWindowFiltering(t *testing.T) {
	now := time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC)
	srv := candleServer(now.AddDate(0, 0, -6), 7, nil, nil) // bars 07-10..07-16
	defer srv.Close()

	start := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC)
	s := newSource(testClient(srv), now)
	d, err := s.Fetch(context.Background(), "AAPL", start, end, source.D1)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	// The family-wide contract is half-open [start, end): the 07-14 bar
	// stamped exactly at end is excluded.
	if d.Len() != 2 {
		t.Fatalf("got %d bars, want 2 (07-12, 07-13)", d.Len())
	}
	times := d.Time()
	if !times[0].Equal(start) {
		t.Errorf("first bar = %v, want %v", times[0], start)
	}
	if !times[1].Equal(end.AddDate(0, 0, -1)) {
		t.Errorf("last bar = %v, want %v", times[1], end.AddDate(0, 0, -1))
	}
}

func TestFetchDepthExceeded(t *testing.T) {
	now := time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC)
	earliest := time.Date(2022, 6, 15, 0, 0, 0, 0, time.UTC)
	var gotCount string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/market-data/search" {
			fmt.Fprint(w, searchAAPL)
			return
		}
		parts := strings.Split(r.URL.Path, "/")
		gotCount = parts[len(parts)-1]
		// A genuinely capped response delivers the full window: 1000 bars
		// whose earliest is still after the requested start.
		fmt.Fprint(w, dailyCandlesJSON(1001, earliest, 1000))
	}))
	defer srv.Close()

	// A six-year daily window needs far more than the endpoint's 1000-bar
	// cap; the fetched window starts in 2022, so the source must refuse
	// rather than hand back a silently truncated series.
	s := newSource(testClient(srv), now)
	_, err := s.Fetch(context.Background(), "AAPL", now.AddDate(-6, 0, 0), now, source.D1)
	if err == nil {
		t.Fatal("Fetch succeeded, want depth-exceeded error")
	}
	if gotCount != "1000" {
		t.Errorf("requested count = %s, want clamped 1000", gotCount)
	}
	for _, want := range []string{"AAPL", "2022-06-15"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestFetchExhaustedHistoryIsNotAnError(t *testing.T) {
	// An instrument that listed after the requested start delivers fewer
	// bars than requested even at the cap: its whole history is present, so
	// the series is returned from its first bar instead of failing — the
	// same behaviour the other sources exhibit for young listings.
	now := time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC)
	listed := now.AddDate(0, 0, -299) // 300 bars, well under the cap
	srv := candleServer(listed, 300, nil, nil)
	defer srv.Close()

	s := newSource(testClient(srv), now)
	d, err := s.Fetch(context.Background(), "AAPL", now.AddDate(-6, 0, 0), now, source.D1)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	// 300 served, minus the still-forming newest bar; end-exclusive also
	// drops the bar stamped at end, which is that same newest bar.
	if d.Len() != 299 {
		t.Fatalf("got %d bars, want 299", d.Len())
	}
	if !d.Time()[0].Equal(listed) {
		t.Errorf("first bar = %v, want listing date %v", d.Time()[0], listed)
	}
}

func TestFetchUnsupportedInterval(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("no HTTP request should be made for an unsupported interval")
	}))
	defer srv.Close()

	s := New(testClient(srv))
	for _, iv := range []source.Interval{source.M2, source.Mo1} {
		_, err := s.Fetch(context.Background(), "AAPL",
			time.Now().Add(-time.Hour), time.Now(), iv)
		if !errors.Is(err, source.ErrUnsupportedInterval) {
			t.Errorf("Fetch(%s): err = %v, want ErrUnsupportedInterval", iv, err)
		}
	}
}

func TestFetchUnknownSymbol(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"page":1,"pageSize":10,"totalItems":0,"items":[]}`)
	}))
	defer srv.Close()

	s := New(testClient(srv))
	_, err := s.Fetch(context.Background(), "NOSUCH",
		time.Now().Add(-time.Hour), time.Now(), source.D1)
	if err == nil || !strings.Contains(err.Error(), "NOSUCH") {
		t.Fatalf("err = %v, want an error naming the symbol", err)
	}
}

func TestFetchNullCandles(t *testing.T) {
	// The API answers 200 with a null candle list for ids it does not
	// know; the source must surface that as an error, not empty data.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/market-data/search" {
			fmt.Fprint(w, searchAAPL)
			return
		}
		fmt.Fprint(w, `{"interval":"OneDay","candles":null}`)
	}))
	defer srv.Close()

	s := New(testClient(srv))
	_, err := s.Fetch(context.Background(), "AAPL",
		time.Now().Add(-time.Hour), time.Now(), source.D1)
	if err == nil || !strings.Contains(err.Error(), "AAPL") {
		t.Fatalf("err = %v, want a no-data error naming the symbol", err)
	}
}

func TestSymbolCacheResolvesOnce(t *testing.T) {
	now := time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC)
	var searchHits, candleHits int
	srv := candleServer(now.AddDate(0, 0, -3), 4, &searchHits, &candleHits)
	defer srv.Close()

	s := newSource(testClient(srv), now)
	for i := range 2 {
		if _, err := s.Fetch(context.Background(), "AAPL",
			now.AddDate(0, 0, -3), now, source.D1); err != nil {
			t.Fatalf("Fetch #%d: %v", i+1, err)
		}
	}
	if searchHits != 1 {
		t.Errorf("search hits = %d, want 1 (second Fetch must use the cache)", searchHits)
	}
	if candleHits != 2 {
		t.Errorf("candle hits = %d, want 2", candleHits)
	}
}

func TestFetchIntervalMapping(t *testing.T) {
	cases := map[source.Interval]string{
		source.M1: "OneMinute", source.M5: "FiveMinutes", source.M10: "TenMinutes",
		source.M15: "FifteenMinutes", source.M30: "ThirtyMinutes",
		source.H1: "OneHour", source.H4: "FourHours",
		source.D1: "OneDay", source.W1: "OneWeek",
	}
	now := time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC)
	for iv, want := range cases {
		var got string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/v1/market-data/search" {
				fmt.Fprint(w, searchAAPL)
				return
			}
			parts := strings.Split(r.URL.Path, "/")
			got = parts[len(parts)-2]
			fmt.Fprint(w, dailyCandlesJSON(1001, now, 1))
		}))
		s := newSource(testClient(srv), now)
		if _, err := s.Fetch(context.Background(), "AAPL",
			now.Add(-time.Hour), now, iv); err != nil {
			t.Errorf("Fetch(%s): %v", iv, err)
		}
		if got != want {
			t.Errorf("interval %s: sent %q, want %q", iv, got, want)
		}
		srv.Close()
	}
}
