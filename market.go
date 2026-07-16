package etoro

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Interval is a candle period for Candles. The API accepts exactly these
// nine values.
type Interval string

// Candle intervals accepted by the candles endpoint.
const (
	IntervalOneMinute      Interval = "OneMinute"
	IntervalFiveMinutes    Interval = "FiveMinutes"
	IntervalTenMinutes     Interval = "TenMinutes"
	IntervalFifteenMinutes Interval = "FifteenMinutes"
	IntervalThirtyMinutes  Interval = "ThirtyMinutes"
	IntervalOneHour        Interval = "OneHour"
	IntervalFourHours      Interval = "FourHours"
	IntervalOneDay         Interval = "OneDay"
	IntervalOneWeek        Interval = "OneWeek"
)

// maxCandles is the server-side cap on candlesCount. Larger requests are
// silently clamped by the API, so the client clamps up front for a
// predictable request.
const maxCandles = 1000

// searchFields is the field projection requested from the search endpoint.
// It matches the fields of Instrument. The server silently drops projection
// fields it cannot return (observed for instrumentTypeID and exchangeID),
// so absent fields decode to their zero values.
const searchFields = "instrumentId,internalSymbolFull,displayname,instrumentTypeID,exchangeID,isCurrentlyTradable,isDelisted,currentRate"

// Instrument is one item from instrument search. InstrumentID is immutable
// (it survives ticker changes and rebrands), so it is safe to cache.
type Instrument struct {
	InstrumentID        int     `json:"instrumentId"`
	InternalSymbolFull  string  `json:"internalSymbolFull"`
	DisplayName         string  `json:"displayname"`
	InstrumentTypeID    int     `json:"instrumentTypeID"`
	ExchangeID          int     `json:"exchangeID"`
	IsCurrentlyTradable bool    `json:"isCurrentlyTradable"`
	IsDelisted          bool    `json:"isDelisted"`
	CurrentRate         float64 `json:"currentRate"`
}

// SearchInstruments looks up instruments whose internalSymbolFull matches
// symbol. The API matches by containment, not equality: searching "AAPL"
// also returns variants such as "AAPL.24-7" and "AAPL.EUR". Use
// ResolveInstrument for an exact-symbol resolution.
func (c *Client) SearchInstruments(ctx context.Context, symbol string) ([]Instrument, error) {
	q := url.Values{
		"fields":             {searchFields},
		"internalSymbolFull": {symbol},
	}
	body, _, err := c.do(ctx, http.MethodGet, "/api/v1/market-data/search", q, nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Page       int          `json:"page"`
		PageSize   int          `json:"pageSize"`
		TotalItems int          `json:"totalItems"`
		Items      []Instrument `json:"items"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("etoro: decode search response: %w", err)
	}
	return resp.Items, nil
}

// ResolveInstrument resolves symbol to a single instrument by exact,
// case-insensitive match on internalSymbolFull among the search results.
// When no result matches exactly, or more than one does, the error lists
// the candidate symbols the search returned.
func (c *Client) ResolveInstrument(ctx context.Context, symbol string) (Instrument, error) {
	items, err := c.SearchInstruments(ctx, symbol)
	if err != nil {
		return Instrument{}, err
	}
	var matches []Instrument
	candidates := make([]string, 0, len(items))
	for _, it := range items {
		candidates = append(candidates, it.InternalSymbolFull)
		if strings.EqualFold(it.InternalSymbolFull, symbol) {
			matches = append(matches, it)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		if len(candidates) == 0 {
			return Instrument{}, fmt.Errorf("etoro: no instrument found for symbol %q", symbol)
		}
		return Instrument{}, fmt.Errorf("etoro: no exact match for symbol %q; search returned: %s",
			symbol, strings.Join(candidates, ", "))
	default:
		exact := make([]string, len(matches))
		for i, m := range matches {
			exact[i] = m.InternalSymbolFull
		}
		return Instrument{}, fmt.Errorf("etoro: symbol %q is ambiguous; exact matches: %s",
			symbol, strings.Join(exact, ", "))
	}
}

// Candle is one OHLCV bar. FromDate is the start of the candle period.
type Candle struct {
	InstrumentID int       `json:"instrumentID"`
	FromDate     time.Time `json:"fromDate"`
	Open         float64   `json:"open"`
	High         float64   `json:"high"`
	Low          float64   `json:"low"`
	Close        float64   `json:"close"`
	Volume       float64   `json:"volume"`
}

// Candles fetches up to count candles for an instrument, oldest first.
// count is clamped to the 1..1000 range the endpoint supports. The window
// always ends at the most recent bar (which may be a still-forming
// session); there is no way to page further back in time — use a coarser
// interval for deeper history.
//
// For an unknown instrument id the API answers 200 with a null candle
// list rather than an error status, so a missing series is reported here
// as an error naming the instrument.
func (c *Client) Candles(ctx context.Context, instrumentID int, interval Interval, count int) ([]Candle, error) {
	if count < 1 {
		count = 1
	}
	if count > maxCandles {
		count = maxCandles
	}
	path := fmt.Sprintf("/api/v1/market-data/instruments/%d/history/candles/asc/%s/%d",
		instrumentID, interval, count)
	body, _, err := c.do(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Interval string `json:"interval"`
		Candles  []struct {
			InstrumentID int      `json:"instrumentId"`
			Candles      []Candle `json:"candles"`
		} `json:"candles"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("etoro: decode candles response: %w", err)
	}
	if len(resp.Candles) == 0 || len(resp.Candles[0].Candles) == 0 {
		return nil, fmt.Errorf("etoro: no candle data for instrument %d (unknown instrument or no data)", instrumentID)
	}
	return resp.Candles[0].Candles, nil
}

// Rate is a real-time price for one instrument. Ask is the buy price, Bid
// the sell price. The conversion rates translate the instrument currency
// to USD.
type Rate struct {
	InstrumentID      int       `json:"instrumentID"`
	Ask               float64   `json:"ask"`
	Bid               float64   `json:"bid"`
	LastExecution     float64   `json:"lastExecution"`
	ConversionRateAsk float64   `json:"conversionRateAsk"`
	ConversionRateBid float64   `json:"conversionRateBid"`
	Date              time.Time `json:"date"`
	PriceRateID       int       `json:"priceRateID"`
}

// Rates fetches real-time bid/ask rates for the given instrument ids.
// At least one id is required.
func (c *Client) Rates(ctx context.Context, ids []int) ([]Rate, error) {
	if len(ids) == 0 {
		return nil, fmt.Errorf("etoro: at least one instrument id is required")
	}
	q := url.Values{"instrumentIds": {joinIntsCSV(ids)}}
	body, _, err := c.do(ctx, http.MethodGet, "/api/v1/market-data/instruments/rates", q, nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Rates []Rate `json:"rates"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("etoro: decode rates response: %w", err)
	}
	return resp.Rates, nil
}

// InstrumentImage is one logo rendition for an instrument.
type InstrumentImage struct {
	InstrumentID    int    `json:"instrumentID"`
	Width           int    `json:"width"`
	Height          int    `json:"height"`
	URI             string `json:"uri"`
	BackgroundColor string `json:"backgroundColor"`
	TextColor       string `json:"textColor"`
}

// InstrumentDisplayData is descriptive metadata for one instrument.
// IsInternalInstrument true means the instrument is restricted from
// public use.
type InstrumentDisplayData struct {
	InstrumentID          int               `json:"instrumentID"`
	InstrumentDisplayName string            `json:"instrumentDisplayName"`
	InstrumentTypeID      int               `json:"instrumentTypeID"`
	ExchangeID            int               `json:"exchangeID"`
	SymbolFull            string            `json:"symbolFull"`
	StocksIndustryID      int               `json:"stocksIndustryId"`
	PriceSource           string            `json:"priceSource"`
	HasExpirationDate     bool              `json:"hasExpirationDate"`
	IsInternalInstrument  bool              `json:"isInternalInstrument"`
	Images                []InstrumentImage `json:"images"`
}

// InstrumentDisplayData fetches display metadata for the given instrument
// ids. With no ids the API returns data for all instruments.
func (c *Client) InstrumentDisplayData(ctx context.Context, ids []int) ([]InstrumentDisplayData, error) {
	var q url.Values
	if len(ids) > 0 {
		q = url.Values{"instrumentIds": {joinIntsCSV(ids)}}
	}
	body, _, err := c.do(ctx, http.MethodGet, "/api/v1/market-data/instruments", q, nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		InstrumentDisplayDatas []InstrumentDisplayData `json:"instrumentDisplayDatas"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("etoro: decode instrument display data response: %w", err)
	}
	return resp.InstrumentDisplayDatas, nil
}

// Exchange is one trading venue known to the platform.
type Exchange struct {
	ExchangeID          int    `json:"exchangeID"`
	ExchangeDescription string `json:"exchangeDescription"`
}

// Exchanges lists all exchanges.
func (c *Client) Exchanges(ctx context.Context) ([]Exchange, error) {
	body, _, err := c.do(ctx, http.MethodGet, "/api/v1/market-data/exchanges", nil, nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		ExchangeInfo []Exchange `json:"exchangeInfo"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("etoro: decode exchanges response: %w", err)
	}
	return resp.ExchangeInfo, nil
}

// InstrumentType is one asset class (stocks, crypto, ...).
type InstrumentType struct {
	InstrumentTypeID          int    `json:"instrumentTypeID"`
	InstrumentTypeDescription string `json:"instrumentTypeDescription"`
}

// InstrumentTypes lists all instrument types.
func (c *Client) InstrumentTypes(ctx context.Context) ([]InstrumentType, error) {
	body, _, err := c.do(ctx, http.MethodGet, "/api/v1/market-data/instrument-types", nil, nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		InstrumentTypes []InstrumentType `json:"instrumentTypes"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("etoro: decode instrument types response: %w", err)
	}
	return resp.InstrumentTypes, nil
}

// PeriodClose is the closing price of one period. The API marks "no data"
// with the sentinel pair Price -1 and the zero date 0001-01-01; use
// HasData to test for it.
type PeriodClose struct {
	Price float64   `json:"price"`
	Date  time.Time `json:"date"`
}

// HasData reports whether the period carries a real closing price rather
// than the API's -1 / 0001-01-01 no-data sentinel.
func (p PeriodClose) HasData() bool {
	return p.Price != -1 && !p.Date.IsZero()
}

// ClosingPriceSet groups the previous daily, weekly, and monthly closes of
// one instrument.
type ClosingPriceSet struct {
	Daily   PeriodClose `json:"daily"`
	Weekly  PeriodClose `json:"weekly"`
	Monthly PeriodClose `json:"monthly"`
}

// InstrumentClosingPrices is the closing-price record of one instrument.
type InstrumentClosingPrices struct {
	InstrumentID         int             `json:"instrumentId"`
	OfficialClosingPrice float64         `json:"officialClosingPrice"`
	ClosingPrices        ClosingPriceSet `json:"closingPrices"`
}

// ClosingPrices fetches the latest official closing prices for all
// instruments in one call. The endpoint takes no parameters.
func (c *Client) ClosingPrices(ctx context.Context) ([]InstrumentClosingPrices, error) {
	body, _, err := c.do(ctx, http.MethodGet, "/api/v1/market-data/instruments/history/closing-price", nil, nil)
	if err != nil {
		return nil, err
	}
	var prices []InstrumentClosingPrices
	if err := json.Unmarshal(body, &prices); err != nil {
		return nil, fmt.Errorf("etoro: decode closing prices response: %w", err)
	}
	return prices, nil
}

// joinIntsCSV renders ids as the comma-separated list format the
// market-data endpoints take for id filters.
func joinIntsCSV(ids []int) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.Itoa(id)
	}
	return strings.Join(parts, ",")
}
