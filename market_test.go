package etoro

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestSearchInstruments(t *testing.T) {
	var gotMethod, gotPath, gotQuery string
	var gotAPIKey, gotUserKey, gotRequestID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotAPIKey = r.Header.Get("x-api-key")
		gotUserKey = r.Header.Get("x-user-key")
		gotRequestID = r.Header.Get("x-request-id")
		// The live API duplicates the instrumentId key inside each item;
		// encoding/json keeps the last occurrence, so the bogus first
		// value must be discarded on decode.
		w.Write([]byte(`{
			"page": 1, "pageSize": 10, "totalItems": 2,
			"items": [
				{"instrumentId": 999999, "displayname": "Apple", "internalSymbolFull": "AAPL", "isCurrentlyTradable": true, "currentRate": 211.5, "instrumentId": 1001},
				{"instrumentId": 15569, "displayname": "Apple 24/7", "internalSymbolFull": "AAPL.24-7", "isCurrentlyTradable": false, "instrumentId": 15569}
			]
		}`))
	}))
	defer srv.Close()

	got, err := testClient(srv).SearchInstruments(t.Context(), "AAPL")
	if err != nil {
		t.Fatalf("SearchInstruments: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if want := "/api/v1/market-data/search"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	// Commas must stay literal (the API ignores %2C-encoded lists).
	wantQuery := "fields=" + searchFields + "&internalSymbolFull=AAPL"
	if gotQuery != wantQuery {
		t.Errorf("query = %q, want %q", gotQuery, wantQuery)
	}
	if gotAPIKey != testAPIKey || gotUserKey != testUserKey {
		t.Errorf("auth headers = (%q, %q), want (%q, %q)", gotAPIKey, gotUserKey, testAPIKey, testUserKey)
	}
	if !uuidV4.MatchString(gotRequestID) {
		t.Errorf("x-request-id = %q, not a canonical UUID v4", gotRequestID)
	}
	want := []Instrument{
		{InstrumentID: 1001, DisplayName: "Apple", InternalSymbolFull: "AAPL", IsCurrentlyTradable: true, CurrentRate: 211.5},
		{InstrumentID: 15569, DisplayName: "Apple 24/7", InternalSymbolFull: "AAPL.24-7"},
	}
	if len(got) != len(want) {
		t.Fatalf("items = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("item %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if got[0].InstrumentID != 1001 {
		t.Errorf("duplicate instrumentId key: decoded %d, want last value 1001", got[0].InstrumentID)
	}
}

// Market-data paths carry no demo/real segment, so a Real() client must
// hit the same path a demo client does.
func TestSearchInstrumentsPathModeIndependent(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Write([]byte(`{"items":[]}`))
	}))
	defer srv.Close()

	c := testClient(srv)
	Real()(c)
	if _, err := c.SearchInstruments(t.Context(), "SPY"); err != nil {
		t.Fatalf("SearchInstruments: %v", err)
	}
	if want := "/api/v1/market-data/search"; gotPath != want {
		t.Errorf("real-mode path = %q, want %q", gotPath, want)
	}
}

func TestSearchInstrumentsAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"errorCode":400,"errorMessage":"fields is required"}`))
	}))
	defer srv.Close()

	_, err := testClient(srv).SearchInstruments(t.Context(), "AAPL")
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if apiErr.Status != http.StatusBadRequest || apiErr.Message != "fields is required" {
		t.Errorf("APIError = %+v, want status 400 with API message", apiErr)
	}
	if s := err.Error(); strings.Contains(s, testAPIKey) || strings.Contains(s, testUserKey) {
		t.Errorf("error %q leaks key material", s)
	}
}

func TestResolveInstrument(t *testing.T) {
	const searchBody = `{
		"items": [
			{"instrumentId": 1001, "internalSymbolFull": "AAPL", "displayname": "Apple"},
			{"instrumentId": 15569, "internalSymbolFull": "AAPL.24-7", "displayname": "Apple 24/7"},
			{"instrumentId": 14254, "internalSymbolFull": "AAPL.EUR", "displayname": "Apple EUR"}
		]
	}`
	for _, tc := range []struct {
		name    string
		symbol  string
		body    string
		want    Instrument
		wantErr string // substring; empty = success expected
	}{
		{
			name:   "exact match among contains matches",
			symbol: "AAPL",
			body:   searchBody,
			want:   Instrument{InstrumentID: 1001, InternalSymbolFull: "AAPL", DisplayName: "Apple"},
		},
		{
			name:   "match is case-insensitive",
			symbol: "aapl",
			body:   searchBody,
			want:   Instrument{InstrumentID: 1001, InternalSymbolFull: "AAPL", DisplayName: "Apple"},
		},
		{
			name:    "no exact match lists candidates",
			symbol:  "AAP",
			body:    searchBody,
			wantErr: "AAPL, AAPL.24-7, AAPL.EUR",
		},
		{
			name:    "empty result set",
			symbol:  "NOPE",
			body:    `{"items":[]}`,
			wantErr: `no instrument found for symbol "NOPE"`,
		},
		{
			name:   "ambiguous exact matches",
			symbol: "aapl",
			body: `{"items":[
				{"instrumentId": 1001, "internalSymbolFull": "AAPL"},
				{"instrumentId": 2002, "internalSymbolFull": "aapl"}
			]}`,
			wantErr: "ambiguous",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.URL.Query().Get("internalSymbolFull"); got != tc.symbol {
					t.Errorf("internalSymbolFull = %q, want %q", got, tc.symbol)
				}
				w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			got, err := testClient(srv).ResolveInstrument(t.Context(), tc.symbol)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want substring %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveInstrument: %v", err)
			}
			if got != tc.want {
				t.Errorf("instrument = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestCandles(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Write([]byte(`{
			"interval": "OneDay",
			"candles": [
				{
					"instrumentId": 1001,
					"candles": [
						{"instrumentID": 1001, "fromDate": "2026-07-15T00:00:00Z", "open": 210.0, "high": 214.0, "low": 209.5, "close": 213.2, "volume": 28450000.0},
						{"instrumentID": 1001, "fromDate": "2026-07-16T00:00:00Z", "open": 213.4, "high": 215.1, "low": 212.8, "close": 214.7, "volume": 7540459.0}
					],
					"rangeOpen": 210.0, "rangeClose": 214.7, "rangeHigh": 215.1, "rangeLow": 209.5, "volume": 35990459.0
				}
			]
		}`))
	}))
	defer srv.Close()

	got, err := testClient(srv).Candles(t.Context(), 1001, IntervalOneDay, 2)
	if err != nil {
		t.Fatalf("Candles: %v", err)
	}
	if want := "/api/v1/market-data/instruments/1001/history/candles/asc/OneDay/2"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if len(got) != 2 {
		t.Fatalf("candles = %d, want 2", len(got))
	}
	want := Candle{
		InstrumentID: 1001,
		FromDate:     time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC),
		Open:         210.0, High: 214.0, Low: 209.5, Close: 213.2,
		Volume: 28450000.0,
	}
	if !got[0].FromDate.Equal(want.FromDate) {
		t.Errorf("fromDate = %v, want %v", got[0].FromDate, want.FromDate)
	}
	got[0].FromDate = want.FromDate
	if got[0] != want {
		t.Errorf("candle = %+v, want %+v", got[0], want)
	}
	if got[1].Close != 214.7 || got[1].Volume != 7540459.0 {
		t.Errorf("second candle = %+v, want close 214.7 volume 7540459", got[1])
	}
}

func TestCandlesCountClamping(t *testing.T) {
	for _, tc := range []struct {
		count    int
		wantPath string
	}{
		{0, "/api/v1/market-data/instruments/42/history/candles/asc/OneWeek/1"},
		{-3, "/api/v1/market-data/instruments/42/history/candles/asc/OneWeek/1"},
		{5000, "/api/v1/market-data/instruments/42/history/candles/asc/OneWeek/1000"},
		{500, "/api/v1/market-data/instruments/42/history/candles/asc/OneWeek/500"},
	} {
		var gotPath string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			w.Write([]byte(`{"interval":"OneWeek","candles":[{"instrumentId":42,"candles":[{"instrumentID":42,"fromDate":"2026-07-12T21:00:00Z","open":1,"high":1,"low":1,"close":1,"volume":1}]}]}`))
		}))
		if _, err := testClient(srv).Candles(t.Context(), 42, IntervalOneWeek, tc.count); err != nil {
			t.Errorf("count %d: %v", tc.count, err)
		}
		if gotPath != tc.wantPath {
			t.Errorf("count %d: path = %q, want %q", tc.count, gotPath, tc.wantPath)
		}
		srv.Close()
	}
}

// A bad instrument id gets HTTP 200 with "candles": null from the live
// API, never a 404; the client must turn that into an error naming the
// instrument.
func TestCandlesNullForUnknownInstrument(t *testing.T) {
	for _, body := range []string{
		`{"interval":"OneDay","candles":null}`,
		`{"interval":"OneDay","candles":[]}`,
		`{"interval":"OneDay","candles":[{"instrumentId":999999999,"candles":null}]}`,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(body))
		}))
		_, err := testClient(srv).Candles(t.Context(), 999999999, IntervalOneDay, 5)
		if err == nil {
			t.Errorf("body %s: want error for null/empty candles", body)
		} else if !strings.Contains(err.Error(), "999999999") {
			t.Errorf("body %s: error %q should name the instrument id", body, err)
		}
		srv.Close()
	}
}

func TestIntervalValues(t *testing.T) {
	want := []Interval{
		IntervalOneMinute, IntervalFiveMinutes, IntervalTenMinutes,
		IntervalFifteenMinutes, IntervalThirtyMinutes, IntervalOneHour,
		IntervalFourHours, IntervalOneDay, IntervalOneWeek,
	}
	wantStrings := []string{
		"OneMinute", "FiveMinutes", "TenMinutes", "FifteenMinutes",
		"ThirtyMinutes", "OneHour", "FourHours", "OneDay", "OneWeek",
	}
	for i, iv := range want {
		if string(iv) != wantStrings[i] {
			t.Errorf("interval %d = %q, want %q", i, iv, wantStrings[i])
		}
	}
}

func TestRates(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Write([]byte(`{
			"rates": [
				{"instrumentID": 1001, "ask": 214.8, "bid": 214.6, "lastExecution": 214.7, "conversionRateAsk": 1.0, "conversionRateBid": 1.0, "date": "2026-07-16T14:30:00Z", "priceRateID": 77},
				{"instrumentID": 14254, "ask": 183.2, "bid": 183.0, "lastExecution": 183.1, "conversionRateAsk": 1.09, "conversionRateBid": 1.08, "date": "2026-07-16T14:30:00Z", "priceRateID": 78}
			]
		}`))
	}))
	defer srv.Close()

	got, err := testClient(srv).Rates(t.Context(), []int{1001, 14254})
	if err != nil {
		t.Fatalf("Rates: %v", err)
	}
	if want := "/api/v1/market-data/instruments/rates"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if want := "instrumentIds=1001,14254"; gotQuery != want {
		t.Errorf("query = %q, want %q", gotQuery, want)
	}
	if len(got) != 2 {
		t.Fatalf("rates = %d, want 2", len(got))
	}
	if got[0].InstrumentID != 1001 || got[0].Ask != 214.8 || got[0].Bid != 214.6 || got[0].PriceRateID != 77 {
		t.Errorf("rate[0] = %+v, want id 1001 ask 214.8 bid 214.6 priceRateID 77", got[0])
	}
	if got[1].ConversionRateAsk != 1.09 {
		t.Errorf("rate[1].ConversionRateAsk = %v, want 1.09", got[1].ConversionRateAsk)
	}
	wantDate := time.Date(2026, 7, 16, 14, 30, 0, 0, time.UTC)
	if !got[0].Date.Equal(wantDate) {
		t.Errorf("rate[0].Date = %v, want %v", got[0].Date, wantDate)
	}
}

func TestRatesRequiresIDs(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write([]byte(`{"rates":[]}`))
	}))
	defer srv.Close()

	if _, err := testClient(srv).Rates(t.Context(), nil); err == nil {
		t.Error("want error for empty id list")
	}
	if calls != 0 {
		t.Errorf("server saw %d requests, want 0", calls)
	}
}

func TestInstrumentDisplayData(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Write([]byte(`{
			"instrumentDisplayDatas": [
				{
					"instrumentID": 1001,
					"instrumentDisplayName": "Apple",
					"instrumentTypeID": 5,
					"exchangeID": 4,
					"symbolFull": "AAPL",
					"stocksIndustryId": 7,
					"priceSource": "efx",
					"hasExpirationDate": false,
					"isInternalInstrument": false,
					"images": [{"instrumentID": 1001, "width": 50, "height": 50, "uri": "https://example.test/aapl-50.png", "backgroundColor": "#000000", "textColor": "#ffffff"}]
				}
			]
		}`))
	}))
	defer srv.Close()

	got, err := testClient(srv).InstrumentDisplayData(t.Context(), []int{1001})
	if err != nil {
		t.Fatalf("InstrumentDisplayData: %v", err)
	}
	if want := "/api/v1/market-data/instruments"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if want := (url.Values{"instrumentIds": {"1001"}}).Encode(); gotQuery != want {
		t.Errorf("query = %q, want %q", gotQuery, want)
	}
	if len(got) != 1 {
		t.Fatalf("items = %d, want 1", len(got))
	}
	d := got[0]
	if d.InstrumentID != 1001 || d.InstrumentDisplayName != "Apple" || d.SymbolFull != "AAPL" ||
		d.InstrumentTypeID != 5 || d.ExchangeID != 4 || d.StocksIndustryID != 7 || d.PriceSource != "efx" {
		t.Errorf("display data = %+v", d)
	}
	if len(d.Images) != 1 || d.Images[0].Width != 50 || d.Images[0].URI != "https://example.test/aapl-50.png" {
		t.Errorf("images = %+v", d.Images)
	}
}

func TestInstrumentDisplayDataNoIDs(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Write([]byte(`{"instrumentDisplayDatas":[]}`))
	}))
	defer srv.Close()

	if _, err := testClient(srv).InstrumentDisplayData(t.Context(), nil); err != nil {
		t.Fatalf("InstrumentDisplayData: %v", err)
	}
	if gotQuery != "" {
		t.Errorf("query = %q, want empty (no ids means all instruments)", gotQuery)
	}
}

func TestExchanges(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Write([]byte(`{"exchangeInfo":[{"exchangeID":4,"exchangeDescription":"NASDAQ"},{"exchangeID":5,"exchangeDescription":"NYSE"}]}`))
	}))
	defer srv.Close()

	got, err := testClient(srv).Exchanges(t.Context())
	if err != nil {
		t.Fatalf("Exchanges: %v", err)
	}
	if want := "/api/v1/market-data/exchanges"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	want := []Exchange{{ExchangeID: 4, ExchangeDescription: "NASDAQ"}, {ExchangeID: 5, ExchangeDescription: "NYSE"}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("exchanges = %+v, want %+v", got, want)
	}
}

func TestInstrumentTypes(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Write([]byte(`{"instrumentTypes":[{"instrumentTypeID":5,"instrumentTypeDescription":"Stocks"},{"instrumentTypeID":10,"instrumentTypeDescription":"Cryptocurrencies"}]}`))
	}))
	defer srv.Close()

	got, err := testClient(srv).InstrumentTypes(t.Context())
	if err != nil {
		t.Fatalf("InstrumentTypes: %v", err)
	}
	if want := "/api/v1/market-data/instrument-types"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	want := []InstrumentType{
		{InstrumentTypeID: 5, InstrumentTypeDescription: "Stocks"},
		{InstrumentTypeID: 10, InstrumentTypeDescription: "Cryptocurrencies"},
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("instrument types = %+v, want %+v", got, want)
	}
}

func TestClosingPrices(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Write([]byte(`[
			{
				"instrumentId": 1001,
				"officialClosingPrice": 213.2,
				"isMarketOpen": true,
				"closingPrices": {
					"daily":   {"price": 213.2, "date": "2026-07-15T00:00:00Z"},
					"weekly":  {"price": 209.9, "date": "2026-07-10T00:00:00Z"},
					"monthly": {"price": -1, "date": "0001-01-01T00:00:00Z"}
				}
			}
		]`))
	}))
	defer srv.Close()

	got, err := testClient(srv).ClosingPrices(t.Context())
	if err != nil {
		t.Fatalf("ClosingPrices: %v", err)
	}
	if want := "/api/v1/market-data/instruments/history/closing-price"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if gotQuery != "" {
		t.Errorf("query = %q, want empty (endpoint takes no parameters)", gotQuery)
	}
	if len(got) != 1 {
		t.Fatalf("items = %d, want 1", len(got))
	}
	p := got[0]
	if p.InstrumentID != 1001 || p.OfficialClosingPrice != 213.2 {
		t.Errorf("record = %+v", p)
	}
	if !p.ClosingPrices.Daily.HasData() || p.ClosingPrices.Daily.Price != 213.2 {
		t.Errorf("daily = %+v, want data with price 213.2", p.ClosingPrices.Daily)
	}
	if !p.ClosingPrices.Weekly.HasData() {
		t.Errorf("weekly = %+v, want HasData true", p.ClosingPrices.Weekly)
	}
	if p.ClosingPrices.Monthly.HasData() {
		t.Errorf("monthly = %+v is the -1/0001-01-01 sentinel; HasData must be false", p.ClosingPrices.Monthly)
	}
}

func TestPeriodCloseHasData(t *testing.T) {
	realDate := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		pc   PeriodClose
		want bool
	}{
		{"normal close", PeriodClose{Price: 100, Date: realDate}, true},
		{"full sentinel", PeriodClose{Price: -1, Date: time.Time{}}, false},
		{"sentinel price only", PeriodClose{Price: -1, Date: realDate}, false},
		{"zero date only", PeriodClose{Price: 100, Date: time.Time{}}, false},
		{"absent field", PeriodClose{}, false},
	} {
		if got := tc.pc.HasData(); got != tc.want {
			t.Errorf("%s: HasData() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestJoinIntsCSV(t *testing.T) {
	for _, tc := range []struct {
		ids  []int
		want string
	}{
		{[]int{1001}, "1001"},
		{[]int{1001, 14254, 8754}, "1001,14254,8754"},
		{nil, ""},
	} {
		if got := joinIntsCSV(tc.ids); got != tc.want {
			t.Errorf("joinIntsCSV(%v) = %q, want %q", tc.ids, got, tc.want)
		}
	}
}

func TestMarketDataAPIErrorOnRates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"errorCode":400,"errorMessage":"invalid instrumentIds"}`)
	}))
	defer srv.Close()

	_, err := testClient(srv).Rates(t.Context(), []int{-1})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
		t.Fatalf("err = %v, want *APIError with status 400", err)
	}
	if !strings.Contains(err.Error(), "invalid instrumentIds") {
		t.Errorf("error %q should carry the API message", err)
	}
}
