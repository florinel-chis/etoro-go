package etoro

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// tradeCapture records the single request a trading test expects.
type tradeCapture struct {
	method      string
	path        string
	query       string
	contentType string
	header      http.Header
	body        []byte
	hits        int
}

// newTradeServer serves one canned response and records the request.
func newTradeServer(t *testing.T, status int, response string) (*httptest.Server, *tradeCapture) {
	t.Helper()
	cap := &tradeCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.hits++
		cap.method = r.Method
		cap.path = r.URL.Path
		cap.query = r.URL.RawQuery
		cap.contentType = r.Header.Get("Content-Type")
		cap.header = r.Header.Clone()
		cap.body, _ = io.ReadAll(r.Body)
		w.WriteHeader(status)
		if response != "" {
			w.Write([]byte(response))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, cap
}

func floatPtr(v float64) *float64 { return &v }

func TestCreateOrder(t *testing.T) {
	const response = `{"token":"11111111-2222-4333-8444-555555555555","orderId":987654,"referenceId":"ignored-by-test"}`
	for _, tc := range []struct {
		name     string
		opts     []Option
		wantPath string
	}{
		{"demo", nil, "/api/v2/trading/execution/demo/orders"},
		{"real", []Option{Real()}, "/api/v2/trading/execution/orders"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, cap := newTradeServer(t, http.StatusOK, response)
			c := testClient(srv, tc.opts...)
			req := OrderRequest{
				Action:         ActionOpen,
				Transaction:    TransactionBuy,
				InstrumentID:   1001,
				SettlementType: SettlementReal,
				OrderType:      OrderTypeMarket,
				Leverage:       1,
				Amount:         floatPtr(50),
				OrderCurrency:  "usd",
				TakeProfitRate: floatPtr(250.5),
			}
			res, err := c.CreateOrder(t.Context(), req)
			if err != nil {
				t.Fatalf("CreateOrder: %v", err)
			}
			if cap.method != http.MethodPost {
				t.Errorf("method = %q, want POST", cap.method)
			}
			if cap.path != tc.wantPath {
				t.Errorf("path = %q, want %q", cap.path, tc.wantPath)
			}
			if cap.contentType != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", cap.contentType)
			}
			if got := cap.header.Get("x-api-key"); got != testAPIKey {
				t.Errorf("x-api-key = %q, want %q", got, testAPIKey)
			}
			if got := cap.header.Get("x-user-key"); got != testUserKey {
				t.Errorf("x-user-key = %q, want %q", got, testUserKey)
			}
			sentID := cap.header.Get("x-request-id")
			if !uuidV4.MatchString(sentID) {
				t.Errorf("x-request-id = %q, not a canonical UUID v4", sentID)
			}
			if res.RequestID != sentID {
				t.Errorf("RequestID = %q, want the sent x-request-id %q", res.RequestID, sentID)
			}
			var sent map[string]any
			if err := json.Unmarshal(cap.body, &sent); err != nil {
				t.Fatalf("request body is not JSON: %v", err)
			}
			for key, want := range map[string]any{
				"action":         "open",
				"transaction":    "buy",
				"instrumentId":   1001.0,
				"settlementType": "real",
				"orderType":      "mkt",
				"leverage":       1.0,
				"amount":         50.0,
				"orderCurrency":  "usd",
				"takeProfitRate": 250.5,
			} {
				if sent[key] != want {
					t.Errorf("body[%q] = %v, want %v", key, sent[key], want)
				}
			}
			if _, ok := sent["symbol"]; ok {
				t.Error("body carries empty symbol; unset fields must be omitted")
			}
			if res.Token != "11111111-2222-4333-8444-555555555555" || res.OrderID != 987654 {
				t.Errorf("result = %+v, want decoded token and orderId", res)
			}
		})
	}
}

func TestCreateOrderValidation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("invalid order reached the server")
	}))
	defer srv.Close()
	c := testClient(srv)

	for _, tc := range []struct {
		name string
		req  OrderRequest
	}{
		{"missing action", OrderRequest{Transaction: TransactionBuy, Symbol: "AAPL", Amount: floatPtr(50)}},
		{"missing transaction", OrderRequest{Action: ActionOpen, Symbol: "AAPL", Amount: floatPtr(50)}},
		{"neither symbol nor instrument", OrderRequest{Action: ActionOpen, Transaction: TransactionBuy, Amount: floatPtr(50)}},
		{"both symbol and instrument", OrderRequest{Action: ActionOpen, Transaction: TransactionBuy, Symbol: "AAPL", InstrumentID: 1001, Amount: floatPtr(50)}},
		{"no sizing field", OrderRequest{Action: ActionOpen, Transaction: TransactionBuy, Symbol: "AAPL"}},
		{"two sizing fields", OrderRequest{Action: ActionOpen, Transaction: TransactionBuy, Symbol: "AAPL", Amount: floatPtr(50), Units: floatPtr(2)}},
		{"mit without trigger rate", OrderRequest{Action: ActionOpen, Transaction: TransactionBuy, Symbol: "AAPL", Amount: floatPtr(50), OrderType: OrderTypeMarketIfTouched}},
		{"missing order type", OrderRequest{Action: ActionOpen, Transaction: TransactionBuy, Symbol: "AAPL", Amount: floatPtr(50), SettlementType: SettlementCFD, Leverage: 1}},
		{"unknown order type", OrderRequest{Action: ActionOpen, Transaction: TransactionBuy, Symbol: "AAPL", Amount: floatPtr(50), SettlementType: SettlementCFD, Leverage: 1, OrderType: "limit"}},
		{"close without positions", OrderRequest{Action: ActionClose, Transaction: TransactionSell}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := c.CreateOrder(t.Context(), tc.req); err == nil {
				t.Error("want a validation error, got nil")
			}
		})
	}
}

func TestCreateOrderAPIError(t *testing.T) {
	srv, _ := newTradeServer(t, http.StatusBadRequest,
		`{"type":"about:blank","title":"Bad Request","status":400,"detail":"leverage is required for open orders"}`)
	c := testClient(srv)

	res, err := c.CreateOrder(t.Context(), OrderRequest{
		Action:         ActionOpen,
		Transaction:    TransactionBuy,
		Symbol:         "AAPL",
		Amount:         floatPtr(50),
		SettlementType: SettlementCFD,
		OrderType:      OrderTypeMarket,
		Leverage:       1,
	})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
		t.Fatalf("err = %v, want *APIError with status 400", err)
	}
	if !strings.Contains(err.Error(), "leverage is required for open orders") {
		t.Errorf("error %q should carry the problem detail", err)
	}
	if strings.Contains(err.Error(), testAPIKey) || strings.Contains(err.Error(), testUserKey) {
		t.Errorf("error %q leaks key material", err)
	}
	if !uuidV4.MatchString(res.RequestID) {
		t.Errorf("RequestID = %q; the idempotency key must survive an API error", res.RequestID)
	}
}

func TestCancelOrder(t *testing.T) {
	for _, tc := range []struct {
		name     string
		opts     []Option
		wantPath string
	}{
		{"demo", nil, "/api/v2/trading/execution/demo/orders/987654"},
		{"real", []Option{Real()}, "/api/v2/trading/execution/orders/987654"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, cap := newTradeServer(t, http.StatusOK, `{"token":"aaaa1111-2222-4333-8444-555555555555"}`)
			token, err := testClient(srv, tc.opts...).CancelOrder(t.Context(), 987654)
			if err != nil {
				t.Fatalf("CancelOrder: %v", err)
			}
			if cap.method != http.MethodDelete {
				t.Errorf("method = %q, want DELETE", cap.method)
			}
			if cap.path != tc.wantPath {
				t.Errorf("path = %q, want %q", cap.path, tc.wantPath)
			}
			if token != "aaaa1111-2222-4333-8444-555555555555" {
				t.Errorf("token = %q", token)
			}
		})
	}
}

func TestClosePositionBodyCasingPerMode(t *testing.T) {
	const response = `{"orderForClose":{"positionID":9001,"instrumentID":1001,"unitsToDeduct":0.5,` +
		`"orderID":444,"orderType":2,"statusID":1,"CID":123,` +
		`"openDateTime":"2026-07-16T09:30:00Z","lastUpdate":"2026-07-16T09:30:00Z"},` +
		`"token":"bbbb1111-2222-4333-8444-555555555555"}`
	for _, tc := range []struct {
		name      string
		opts      []Option
		wantPath  string
		wantKey   string
		rejectKey string
		units     *float64
		wantUnits string
	}{
		{
			name:      "demo full close",
			wantPath:  "/api/v1/trading/execution/demo/market-close-orders/positions/9001",
			wantKey:   "InstrumentID",
			rejectKey: "InstrumentId",
			units:     nil,
			wantUnits: "null",
		},
		{
			name:      "real partial close",
			opts:      []Option{Real()},
			wantPath:  "/api/v1/trading/execution/market-close-orders/positions/9001",
			wantKey:   "InstrumentId",
			rejectKey: "InstrumentID",
			units:     floatPtr(0.5),
			wantUnits: "0.5",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, cap := newTradeServer(t, http.StatusOK, response)
			res, err := testClient(srv, tc.opts...).ClosePosition(t.Context(), 9001, 1001, tc.units)
			if err != nil {
				t.Fatalf("ClosePosition: %v", err)
			}
			if cap.method != http.MethodPost {
				t.Errorf("method = %q, want POST", cap.method)
			}
			if cap.path != tc.wantPath {
				t.Errorf("path = %q, want %q", cap.path, tc.wantPath)
			}
			var sent map[string]json.RawMessage
			if err := json.Unmarshal(cap.body, &sent); err != nil {
				t.Fatalf("request body is not JSON: %v", err)
			}
			if string(sent[tc.wantKey]) != "1001" {
				t.Errorf("body[%q] = %s, want 1001", tc.wantKey, sent[tc.wantKey])
			}
			if _, ok := sent[tc.rejectKey]; ok {
				t.Errorf("body carries %q; this mode must send %q", tc.rejectKey, tc.wantKey)
			}
			if string(sent["UnitsToDeduct"]) != tc.wantUnits {
				t.Errorf("body[UnitsToDeduct] = %s, want %s", sent["UnitsToDeduct"], tc.wantUnits)
			}
			if res.Token == "" || res.OrderForClose.PositionID != 9001 || res.OrderForClose.OrderID != 444 {
				t.Errorf("result = %+v, want decoded receipt", res)
			}
		})
	}
}

func TestCancelCloseOrder(t *testing.T) {
	for _, tc := range []struct {
		name     string
		opts     []Option
		wantPath string
	}{
		{"demo", nil, "/api/v1/trading/execution/demo/market-close-orders/444"},
		{"real", []Option{Real()}, "/api/v1/trading/execution/market-close-orders/444"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, cap := newTradeServer(t, http.StatusOK, `{"token":"cccc1111-2222-4333-8444-555555555555"}`)
			token, err := testClient(srv, tc.opts...).CancelCloseOrder(t.Context(), 444)
			if err != nil {
				t.Fatalf("CancelCloseOrder: %v", err)
			}
			if cap.method != http.MethodDelete {
				t.Errorf("method = %q, want DELETE", cap.method)
			}
			if cap.path != tc.wantPath {
				t.Errorf("path = %q, want %q", cap.path, tc.wantPath)
			}
			if token == "" {
				t.Error("token not decoded")
			}
		})
	}
}

func TestModifyPositionSLTP(t *testing.T) {
	const response = `{"operationId":"dddd1111-2222-4333-8444-555555555555","positionId":9001,"referenceId":"eeee1111-2222-4333-8444-555555555555"}`
	for _, tc := range []struct {
		name     string
		opts     []Option
		wantPath string
	}{
		{"demo", nil, "/api/v2/trading/demo/positions/9001"},
		{"real", []Option{Real()}, "/api/v2/trading/positions/9001"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, cap := newTradeServer(t, http.StatusAccepted, response)
			res, err := testClient(srv, tc.opts...).ModifyPositionSLTP(t.Context(), 9001, floatPtr(180.5), nil)
			if err != nil {
				t.Fatalf("ModifyPositionSLTP: %v", err)
			}
			if cap.method != http.MethodPatch {
				t.Errorf("method = %q, want PATCH", cap.method)
			}
			if cap.path != tc.wantPath {
				t.Errorf("path = %q, want %q", cap.path, tc.wantPath)
			}
			var sent map[string]any
			if err := json.Unmarshal(cap.body, &sent); err != nil {
				t.Fatalf("request body is not JSON: %v", err)
			}
			if sent["stopLossRate"] != 180.5 {
				t.Errorf("body[stopLossRate] = %v, want 180.5", sent["stopLossRate"])
			}
			if _, ok := sent["takeProfitRate"]; ok {
				t.Error("body carries takeProfitRate; a nil side must be omitted")
			}
			if res.PositionID != 9001 || res.OperationID == "" || res.ReferenceID == "" {
				t.Errorf("result = %+v, want decoded 202 acknowledgement", res)
			}
		})
	}

	t.Run("both nil is rejected", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Error("empty edit reached the server")
		}))
		defer srv.Close()
		if _, err := testClient(srv).ModifyPositionSLTP(t.Context(), 9001, nil, nil); err == nil {
			t.Error("want a validation error, got nil")
		}
	})
}

const orderLookupJSON = `{
	"accountId":77,"gcid":123456,"portfolioId":1,"orderId":987654,
	"action":"open","transaction":"buy","type":"mkt","etoroOrderTypeId":10,
	"status":{"id":3,"name":"Filled","errorCode":0},
	"asset":{"symbol":"AAPL","instrumentId":1001,"currency":"USD","settlementType":"real","leverage":1,"side":"long"},
	"orderCurrency":"usd","requestedAmount":50,"totalCosts":0.12,
	"positionExecutions":[{
		"positionId":9001,"state":"open","investedAmountCurrency":50,"remainingUnits":0.23,
		"openingData":{"openTime":"2026-07-16T09:30:00Z","orderId":987654,
			"executionTime":"2026-07-16T09:30:01Z","units":0.23,"avgPrice":212.5,
			"avgConversionRate":1,"marketSpread":0.02,"markup":0.01,"priceId":42,"fees":0,"taxes":0}
	}],
	"requestTime":"2026-07-16T09:30:00Z","lastUpdate":"2026-07-16T09:30:01Z",
	"openActionType":"regular","requestType":"byAmount"
}`

func checkOrderLookup(t *testing.T, got OrderLookup) {
	t.Helper()
	if got.OrderID != 987654 || got.Action != "open" || got.Type != "mkt" {
		t.Errorf("lookup = %+v, want orderId 987654 open mkt", got)
	}
	if got.Status.ID != OrderStatusFilled || got.Status.Name != "Filled" {
		t.Errorf("status = %+v, want Filled (3)", got.Status)
	}
	if got.Asset.InstrumentID != 1001 || got.Asset.Symbol != "AAPL" {
		t.Errorf("asset = %+v, want AAPL/1001", got.Asset)
	}
	if got.RequestedAmount == nil || *got.RequestedAmount != 50 {
		t.Errorf("requestedAmount = %v, want 50", got.RequestedAmount)
	}
	if len(got.PositionExecutions) != 1 || got.PositionExecutions[0].PositionID != 9001 {
		t.Fatalf("positionExecutions = %+v, want one entry for position 9001", got.PositionExecutions)
	}
	od := got.PositionExecutions[0].OpeningData
	if od.AvgPrice != 212.5 || od.Units == nil || *od.Units != 0.23 {
		t.Errorf("openingData = %+v, want avgPrice 212.5 and units 0.23", od)
	}
}

func TestOrderInfo(t *testing.T) {
	for _, tc := range []struct {
		name     string
		opts     []Option
		wantPath string
	}{
		{"demo", nil, "/api/v2/trading/info/demo/orders:lookup"},
		{"real", []Option{Real()}, "/api/v2/trading/info/orders:lookup"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, cap := newTradeServer(t, http.StatusOK, orderLookupJSON)
			got, err := testClient(srv, tc.opts...).OrderInfo(t.Context(), 987654)
			if err != nil {
				t.Fatalf("OrderInfo: %v", err)
			}
			if cap.method != http.MethodGet {
				t.Errorf("method = %q, want GET", cap.method)
			}
			if cap.path != tc.wantPath {
				t.Errorf("path = %q, want %q", cap.path, tc.wantPath)
			}
			if cap.query != "orderId=987654" {
				t.Errorf("query = %q, want orderId=987654", cap.query)
			}
			checkOrderLookup(t, got)
		})
	}
}

func TestOrderInfoByReference(t *testing.T) {
	srv, cap := newTradeServer(t, http.StatusOK, orderLookupJSON)
	const ref = "ffff1111-2222-4333-8444-555555555555"
	got, err := testClient(srv).OrderInfoByReference(t.Context(), ref)
	if err != nil {
		t.Fatalf("OrderInfoByReference: %v", err)
	}
	if cap.path != "/api/v2/trading/info/demo/orders:lookup" {
		t.Errorf("path = %q", cap.path)
	}
	if cap.query != "referenceId="+ref {
		t.Errorf("query = %q, want referenceId=%s", cap.query, ref)
	}
	checkOrderLookup(t, got)
}

func TestCloseOrderInfo(t *testing.T) {
	for _, tc := range []struct {
		name     string
		opts     []Option
		wantPath string
	}{
		{"demo", nil, "/api/v1/trading/info/demo/close-orders/444"},
		{"real", []Option{Real()}, "/api/v1/trading/info/real/close-orders/444"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, cap := newTradeServer(t, http.StatusOK, orderLookupJSON)
			got, err := testClient(srv, tc.opts...).CloseOrderInfo(t.Context(), 444)
			if err != nil {
				t.Fatalf("CloseOrderInfo: %v", err)
			}
			if cap.method != http.MethodGet {
				t.Errorf("method = %q, want GET", cap.method)
			}
			if cap.path != tc.wantPath {
				t.Errorf("path = %q, want %q", cap.path, tc.wantPath)
			}
			checkOrderLookup(t, got)
		})
	}
}

const clientPortfolioJSON = `{"clientPortfolio":{
	"positions":[{
		"positionID":9001,"CID":123,"openDateTime":"2026-07-01T10:00:00Z","openRate":210.0,
		"instrumentID":1001,"mirrorID":0,"isBuy":true,"amount":50,"leverage":1,"units":0.238,
		"isNoStopLoss":true,"isNoTakeProfit":false,"takeProfitRate":250.5,
		"unrealizedPnL":{"pnL":2.5,"pnlAssetCurrency":2.5,"closeRate":220.5,
			"timestamp":"2026-07-16T10:00:00Z"}
	}],
	"credit":812.34,
	"mirrors":[],
	"orders":[{"orderId":31,"cid":123,"openDateTime":"2026-07-15T10:00:00Z","instrumentId":1001,
		"isBuy":true,"rate":200.0,"amount":25,"leverage":1}],
	"ordersForOpen":[{"orderId":32,"cid":123,"instrumentId":1001,"amount":10,"isBuy":true,
		"leverage":1,"mirrorId":0}],
	"ordersForClose":[{"orderId":33,"cid":123,"instrumentId":1001,"unitsToDeduct":0.1,"positionId":9001}],
	"bonusCredit":0,
	"unrealizedPnL":2.5,
	"accountCurrencyId":1
}}`

func checkClientPortfolio(t *testing.T, got ClientPortfolio) {
	t.Helper()
	if got.Credit != 812.34 {
		t.Errorf("credit = %v, want 812.34", got.Credit)
	}
	if len(got.Positions) != 1 {
		t.Fatalf("positions = %+v, want one", got.Positions)
	}
	p := got.Positions[0]
	if p.PositionID != 9001 || p.InstrumentID != 1001 || !p.IsBuy || p.Units != 0.238 {
		t.Errorf("position = %+v, want 9001/1001 long 0.238 units", p)
	}
	if p.UnrealizedPnL == nil || p.UnrealizedPnL.PnL != 2.5 || p.UnrealizedPnL.CloseRate != 220.5 {
		t.Errorf("position unrealizedPnL = %+v, want pnL 2.5 at closeRate 220.5", p.UnrealizedPnL)
	}
	if len(got.Orders) != 1 || got.Orders[0].Rate != 200.0 {
		t.Errorf("orders = %+v, want one MIT order with trigger 200", got.Orders)
	}
	if len(got.OrdersForOpen) != 1 || got.OrdersForOpen[0].Amount != 10 {
		t.Errorf("ordersForOpen = %+v, want one entry with amount 10", got.OrdersForOpen)
	}
	if len(got.OrdersForClose) != 1 || got.OrdersForClose[0].PositionID != 9001 {
		t.Errorf("ordersForClose = %+v, want one entry for position 9001", got.OrdersForClose)
	}
}

func TestPortfolioSummary(t *testing.T) {
	for _, tc := range []struct {
		name     string
		opts     []Option
		wantPath string
	}{
		{"demo", nil, "/api/v1/trading/info/demo/pnl"},
		{"real", []Option{Real()}, "/api/v1/trading/info/real/pnl"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, cap := newTradeServer(t, http.StatusOK, clientPortfolioJSON)
			got, err := testClient(srv, tc.opts...).PortfolioSummary(t.Context())
			if err != nil {
				t.Fatalf("PortfolioSummary: %v", err)
			}
			if cap.method != http.MethodGet {
				t.Errorf("method = %q, want GET", cap.method)
			}
			if cap.path != tc.wantPath {
				t.Errorf("path = %q, want %q", cap.path, tc.wantPath)
			}
			checkClientPortfolio(t, got)
			if got.UnrealizedPnL != 2.5 || got.AccountCurrencyID != 1 {
				t.Errorf("account totals = %v/%v, want 2.5/1", got.UnrealizedPnL, got.AccountCurrencyID)
			}
		})
	}
}

func TestPortfolioBreakdown(t *testing.T) {
	for _, tc := range []struct {
		name     string
		opts     []Option
		wantPath string
	}{
		{"demo", nil, "/api/v1/trading/info/demo/portfolio"},
		{"real", []Option{Real()}, "/api/v1/trading/info/portfolio"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, cap := newTradeServer(t, http.StatusOK, clientPortfolioJSON)
			got, err := testClient(srv, tc.opts...).PortfolioBreakdown(t.Context())
			if err != nil {
				t.Fatalf("PortfolioBreakdown: %v", err)
			}
			if cap.path != tc.wantPath {
				t.Errorf("path = %q, want %q", cap.path, tc.wantPath)
			}
			checkClientPortfolio(t, got)
		})
	}
}

func TestTradingHistory(t *testing.T) {
	const response = `[{"netProfit":3.21,"closeRate":220.1,"closeTimestamp":"2026-07-10T14:00:00Z",
		"positionId":8001,"instrumentId":1001,"isBuy":true,"leverage":1,
		"openRate":210.0,"openTimestamp":"2026-06-01T14:00:00Z","orderId":700,
		"investment":50,"initialInvestment":50,"fees":-0.1,"units":0.238}]`
	for _, tc := range []struct {
		name     string
		opts     []Option
		wantPath string
	}{
		{"demo", nil, "/api/v1/trading/info/trade/demo/history"},
		{"real", []Option{Real()}, "/api/v1/trading/info/trade/history"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, cap := newTradeServer(t, http.StatusOK, response)
			params := TradeHistoryParams{
				MinDate:  time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
				Page:     2,
				PageSize: 50,
			}
			trades, err := testClient(srv, tc.opts...).TradingHistory(t.Context(), params)
			if err != nil {
				t.Fatalf("TradingHistory: %v", err)
			}
			if cap.path != tc.wantPath {
				t.Errorf("path = %q, want %q", cap.path, tc.wantPath)
			}
			if want := "minDate=2026-01-02&page=2&pageSize=50"; cap.query != want {
				t.Errorf("query = %q, want %q", cap.query, want)
			}
			if len(trades) != 1 || trades[0].PositionID != 8001 || trades[0].NetProfit != 3.21 {
				t.Errorf("trades = %+v, want one closed trade for position 8001", trades)
			}
		})
	}

	t.Run("missing MinDate is rejected", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Error("request without MinDate reached the server")
		}))
		defer srv.Close()
		if _, err := testClient(srv).TradingHistory(t.Context(), TradeHistoryParams{}); err == nil {
			t.Error("want an error for zero MinDate, got nil")
		}
	})
}

func TestTradingEligibility(t *testing.T) {
	const response = `{"currency":"USD","eligibilities":[{
		"instrumentId":1001,"symbol":"AAPL","minPositionExposure":10,"maxUnitsPerOrder":10000,
		"allowOpenPosition":true,"allowClosePosition":true,"allowPartialClosePosition":true,
		"unitsQuantityType":"fractional","allowedOrderQuantityType":"all","tradeUnitType":"units",
		"leverageConfigs":[{"settlementType":"real","direction":"long","leverageValues":[1],
			"minPositionAmount":10,"allowStopLossTakeProfit":true}]
	}],"notFoundInstrumentIds":[],"notFoundSymbols":[]}`
	for _, tc := range []struct {
		name     string
		opts     []Option
		wantPath string
	}{
		{"demo", nil, "/api/v2/trading/info/demo/eligibility"},
		{"real", []Option{Real()}, "/api/v2/trading/info/eligibility"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, cap := newTradeServer(t, http.StatusOK, response)
			got, err := testClient(srv, tc.opts...).TradingEligibility(t.Context(), 1001)
			if err != nil {
				t.Fatalf("TradingEligibility: %v", err)
			}
			if cap.method != http.MethodPost {
				t.Errorf("method = %q, want POST", cap.method)
			}
			if cap.path != tc.wantPath {
				t.Errorf("path = %q, want %q", cap.path, tc.wantPath)
			}
			var sent map[string]any
			if err := json.Unmarshal(cap.body, &sent); err != nil {
				t.Fatalf("request body is not JSON: %v", err)
			}
			ids, ok := sent["instrumentIds"].([]any)
			if !ok || len(ids) != 1 || ids[0] != 1001.0 {
				t.Errorf("body[instrumentIds] = %v, want [1001]", sent["instrumentIds"])
			}
			if got.Symbol != "AAPL" || !got.AllowOpenPosition || got.UnitsQuantityType != "fractional" {
				t.Errorf("eligibility = %+v", got)
			}
			if len(got.LeverageConfigs) != 1 || got.LeverageConfigs[0].SettlementType != "real" {
				t.Errorf("leverageConfigs = %+v, want one real/long entry", got.LeverageConfigs)
			}
		})
	}

	t.Run("unknown instrument is an error", func(t *testing.T) {
		srv, _ := newTradeServer(t, http.StatusOK,
			`{"currency":"USD","eligibilities":[],"notFoundInstrumentIds":[999999],"notFoundSymbols":[]}`)
		if _, err := testClient(srv).TradingEligibility(t.Context(), 999999); err == nil {
			t.Error("want an error when the instrument is not returned, got nil")
		}
	})
}

func TestTradingCostEstimate(t *testing.T) {
	const response = `{"instrumentId":1001,"symbol":"AAPL","costs":[
		{"costType":"marketSpread","amount":0.05,"currency":"USD"},
		{"costType":"overnightFee","amount":0.01,"currency":"USD"}
	],"lastUpdated":"2026-07-16T10:00:00Z"}`
	for _, tc := range []struct {
		name     string
		opts     []Option
		wantPath string
	}{
		{"demo", nil, "/api/v2/trading/info/demo/costs"},
		{"real", []Option{Real()}, "/api/v2/trading/info/costs"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, cap := newTradeServer(t, http.StatusOK, response)
			got, err := testClient(srv, tc.opts...).TradingCostEstimate(t.Context(), OrderRequest{
				Action:         ActionOpen,
				Transaction:    TransactionBuy,
				InstrumentID:   1001,
				SettlementType: SettlementCFD,
				OrderType:      OrderTypeMarket,
				Leverage:       1,
				Amount:         floatPtr(50),
			})
			if err != nil {
				t.Fatalf("TradingCostEstimate: %v", err)
			}
			if cap.method != http.MethodPost {
				t.Errorf("method = %q, want POST", cap.method)
			}
			if cap.path != tc.wantPath {
				t.Errorf("path = %q, want %q", cap.path, tc.wantPath)
			}
			if got.InstrumentID != 1001 || len(got.Costs) != 2 {
				t.Fatalf("estimate = %+v, want two cost lines for 1001", got)
			}
			if got.Costs[0].CostType != "marketSpread" || got.Costs[0].Amount != 0.05 {
				t.Errorf("costs[0] = %+v, want marketSpread 0.05", got.Costs[0])
			}
		})
	}

	t.Run("invalid request is rejected client-side", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Error("invalid cost request reached the server")
		}))
		defer srv.Close()
		req := OrderRequest{Action: ActionOpen, Transaction: TransactionBuy} // no asset, no sizing
		if _, err := testClient(srv).TradingCostEstimate(t.Context(), req); err == nil {
			t.Error("want a validation error, got nil")
		}
	})
}
