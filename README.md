# etoro-go

A dependency-free Go client for the [eToro Public API](https://api-portal.etoro.com)
at `public-api.etoro.com`: market data (instrument search, candles, real-time
rates, reference data), balances, identity, cash-account transactions, and
trading (orders, positions, portfolio, history) against either the demo or
the real account.

## Authentication

Every request carries three headers: `x-api-key`, `x-user-key`, and an
`x-request-id` the client generates as a fresh UUID v4 per call. The key pair
is issued in the eToro API portal. Credentials are caller-supplied at
construction — the package never reads environment variables, never logs key
material, and sends it only in request headers, never in URLs or error
messages. Keeping the keys in the environment and binding them at startup is
the recommended pattern:

```go
c := etoro.New(os.Getenv("ETORO_API_KEY"), os.Getenv("ETORO_USER_KEY"))
```

A client targets the **demo** (paper-trading) account by default. Demo and
real share one host and differ only by a path segment on the trading routes;
market-data, balances, identity, and cash endpoints are identical in both
modes:

```go
demo := etoro.New(apiKey, userKey)               // demo trading (default)
real := etoro.New(apiKey, userKey, etoro.Real()) // real-money trading
```

`Identity(ctx)` (`GET /api/v1/me`) verifies the pair and reports the OAuth
scopes it was granted — a 403 elsewhere means the pair lacks that endpoint's
scope.

## Market data: search → candles

```go
ctx := context.Background()
c := etoro.New(os.Getenv("ETORO_API_KEY"), os.Getenv("ETORO_USER_KEY"))

// Search matches internalSymbolFull by containment, so "AAPL" also returns
// "AAPL.24-7" and "AAPL.EUR". ResolveInstrument narrows to the exact symbol.
inst, err := c.ResolveInstrument(ctx, "AAPL")
if err != nil {
    log.Fatal(err)
}

candles, err := c.Candles(ctx, inst.InstrumentID, etoro.IntervalOneDay, 250)
if err != nil {
    log.Fatal(err)
}
for _, bar := range candles { // oldest first
    fmt.Println(bar.FromDate.Format("2006-01-02"), bar.Open, bar.High, bar.Low, bar.Close, bar.Volume)
}
```

Two candle-endpoint behaviours worth knowing, both verified against the live
API:

- **Depth is capped at 1000 bars, and the window always ends at the most
  recent bar.** There are no from/to parameters and no way to page further
  back in time; requesting more than 1000 is silently clamped by the server
  (the client clamps to 1000 up front for a predictable request). 1000
  dailies reach back about four years — for deeper history use a coarser
  interval (`IntervalOneWeek` at full depth covers roughly 19 years).
- **An unknown instrument id is not an HTTP error.** The API answers
  `200` with `"candles": null`. The client detects this and returns an error
  naming the instrument instead of an empty slice.

## Balance

```go
bal, err := c.Balance(ctx) // GET /api/v1/balances
if err != nil {
    log.Fatal(err)
}
fmt.Printf("total %.2f %s\n", bal.TotalBalance, bal.DisplayCurrency)
for _, acc := range bal.Balances {
    fmt.Printf("  %s (%s): %.2f %s\n", acc.AccountID, acc.AccountType, acc.Balance, acc.Currency)
}
```

`AggregatedBalances` adds zero-balance accounts and per-account equity
details (buying power, used margin, crypto holdings); `BalancesByType`
filters to one account type; `BalanceHistory` returns daily snapshots for up
to 365 days within the last 12 months.

## Trading

Consult `TradingEligibility` for an instrument's valid leverage values,
sizing mode, and SL/TP bounds, and `TradingCostEstimate` to price an order
without placing it. Order submission validates client-side first (see
`OrderRequest`), and the `x-request-id` sent with the order doubles as its
idempotency key: it is echoed back as `referenceId`, surfaced as
`OrderResult.RequestID`, and accepted by `OrderInfoByReference` to recover an
order's status even when the response was lost.

> **This places an order.** With a client constructed via `etoro.Real()` the
> code below trades real money on the real account. Run it against the
> default demo mode until you have verified the behaviour end to end.

```go
amount := 50.0
res, err := c.CreateOrder(ctx, etoro.OrderRequest{
    Action:         etoro.ActionOpen,
    Transaction:    etoro.TransactionBuy,
    Symbol:         "AAPL",
    OrderType:      etoro.OrderTypeMarket,
    SettlementType: etoro.SettlementCFD,
    Leverage:       1,
    Amount:         &amount, // USD; exactly one of Amount, Units, Contracts
})
if err != nil {
    log.Fatal(err)
}

// Execution is asynchronous: poll until filled to learn the position ids.
info, err := c.OrderInfo(ctx, res.OrderID)
if err != nil {
    log.Fatal(err)
}
if info.Status.ID == etoro.OrderStatusFilled {
    for _, p := range info.PositionExecutions {
        fmt.Println("opened position", p.PositionID)
    }
}
```

The rest of the trading surface: `CancelOrder`, `ClosePosition` (full or
partial), `CancelCloseOrder`, `ModifyPositionSLTP`, `PortfolioSummary` (with
live per-position P&L), `PortfolioBreakdown` (same portfolio, no
valuations), `CloseOrderInfo`, and `TradingHistory`.

## Backtest data source

The nested module `github.com/florinel-chis/etoro-go/backtestsource` adapts
the client to the `gobacktest` engine's `source.Source`:

```go
import (
    etoro "github.com/florinel-chis/etoro-go"
    etorobs "github.com/florinel-chis/etoro-go/backtestsource"
    "github.com/florinel-chis/gobacktest/source"
)

src := etorobs.New(etoro.New(apiKey, userKey))
data, err := src.Fetch(ctx, "AAPL", start, end, source.D1)
```

Symbols resolve to instrument ids once per source (ids are immutable, so the
cache never goes stale) and candles are fetched natively. The candle endpoint
serves only the most recent window of at most 1000 bars per interval — about
4 years of daily bars, or an instrument's full history on weekly bars. When a
requested window reaches deeper than that, `Fetch` returns an error naming
the earliest available bar instead of silently truncating; instruments whose
whole history fits (young listings) are returned from their first bar. Bars
follow the family-wide `[start, end)` contract, and the newest, still-forming
session is dropped rather than reported as final OHLCV.

## Rate limits and errors

Endpoints are rate-limited in pools (market data 120/60s shared across the
group; trading execution 20/60s shared per mode; costs and eligibility
20/60s each; balances, identity, and cash draw from the shared default
60/60s quota). The server reports the quota in `ratelimit-*`
response headers. On a 429 the client retries exactly once, honoring
`Retry-After` when present (2s otherwise) and reusing the same
`x-request-id`, since that id is the operation's idempotency key. Any 2xx
status is success; every other status is returned as an `*APIError` carrying
the HTTP status and the API's error message — never request headers or key
material.

## Tests

All tests are hermetic (`httptest` servers with fixtures shaped like live
responses); `go test ./...` needs no network or credentials.

## License

MIT
