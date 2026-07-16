package etoro

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Order request enums. The API accepts these exact lowercase strings.
const (
	ActionOpen  = "open"
	ActionClose = "close"

	TransactionBuy        = "buy"
	TransactionSell       = "sell"
	TransactionSellShort  = "sellShort"
	TransactionBuyToCover = "buyToCover"

	OrderTypeMarket          = "mkt"
	OrderTypeMarketIfTouched = "mit"

	SettlementCFD         = "cfd"
	SettlementReal        = "real"
	SettlementRealFutures = "realFutures"
	SettlementMarginTrade = "marginTrade"

	StopLossFixed    = "fixed"
	StopLossTrailing = "trailing"
)

// Order status ids as reported by OrderStatus.ID.
const (
	OrderStatusReceived                = 1
	OrderStatusPlaced                  = 2
	OrderStatusFilled                  = 3
	OrderStatusRejected                = 4
	OrderStatusPartiallyFilled         = 5
	OrderStatusPendingCancel           = 6
	OrderStatusCanceled                = 7
	OrderStatusExpired                 = 8
	OrderStatusCanceledPartiallyFilled = 9
	OrderStatusRejectedPartiallyFilled = 10
)

// OrderRequest describes an order for CreateOrder and TradingCostEstimate.
//
// Action and Transaction are always required. For open orders exactly one
// of Symbol or InstrumentID identifies the asset, exactly one of Amount,
// Units, or Contracts sizes the order, and SettlementType and Leverage are
// required by the API. OrderType is "mkt" (market) or "mit"
// (market-if-touched, which additionally needs TriggerRate). Close orders
// identify what to close through PositionIDs instead.
//
// StopLossRate and TakeProfitRate are absolute rates, not distances.
type OrderRequest struct {
	Action           string   `json:"action"`
	Transaction      string   `json:"transaction"`
	Symbol           string   `json:"symbol,omitempty"`
	InstrumentID     int      `json:"instrumentId,omitempty"`
	SettlementType   string   `json:"settlementType,omitempty"`
	OrderType        string   `json:"orderType,omitempty"`
	TriggerRate      *float64 `json:"triggerRate,omitempty"`
	Leverage         int      `json:"leverage,omitempty"`
	Amount           *float64 `json:"amount,omitempty"`
	Units            *float64 `json:"units,omitempty"`
	Contracts        *float64 `json:"contracts,omitempty"`
	OrderCurrency    string   `json:"orderCurrency,omitempty"`
	StopLossRate     *float64 `json:"stopLossRate,omitempty"`
	TakeProfitRate   *float64 `json:"takeProfitRate,omitempty"`
	StopLossType     string   `json:"stopLossType,omitempty"`
	AdditionalMargin *float64 `json:"additionalMargin,omitempty"`
	PositionIDs      []int64  `json:"positionIds,omitempty"`
}

// validate enforces the request constraints the API would otherwise reject
// server-side, so mistakes surface before a request is spent.
func (r OrderRequest) validate() error {
	if r.Action == "" {
		return errors.New("etoro: order action is required")
	}
	if r.Transaction == "" {
		return errors.New("etoro: order transaction is required")
	}
	if r.Action == ActionClose {
		if len(r.PositionIDs) == 0 {
			return errors.New("etoro: close orders require PositionIDs")
		}
		return nil
	}
	if (r.Symbol == "") == (r.InstrumentID == 0) {
		return errors.New("etoro: open orders require exactly one of Symbol or InstrumentID")
	}
	sized := 0
	for _, f := range []*float64{r.Amount, r.Units, r.Contracts} {
		if f != nil {
			sized++
		}
	}
	if sized != 1 {
		return errors.New("etoro: open orders require exactly one of Amount, Units, or Contracts")
	}
	if r.SettlementType == "" {
		return errors.New("etoro: open orders require SettlementType")
	}
	if r.Leverage <= 0 {
		return errors.New("etoro: open orders require a positive Leverage")
	}
	if r.OrderType == OrderTypeMarketIfTouched && r.TriggerRate == nil {
		return errors.New("etoro: mit orders require TriggerRate")
	}
	return nil
}

// OrderResult is the acknowledgement returned by CreateOrder.
type OrderResult struct {
	Token       string `json:"token"`       // tracking token
	OrderID     int64  `json:"orderId"`     // assigned order id
	ReferenceID string `json:"referenceId"` // echo of the x-request-id header

	// RequestID is the x-request-id this client sent with the order. It is
	// the order's idempotency key and matches ReferenceID on success; use it
	// with OrderInfoByReference to recover an order's status even when the
	// response was lost. It is populated on decode failures too.
	RequestID string `json:"-"`
}

// CreateOrder submits an order (POST /api/v2/trading/execution/{demo/}orders).
// The request is validated client-side first; see OrderRequest for the
// rules. Poll OrderInfo (or OrderInfoByReference with the returned
// RequestID) until the status reaches OrderStatusFilled to learn the
// resulting position ids.
func (c *Client) CreateOrder(ctx context.Context, req OrderRequest) (OrderResult, error) {
	if err := req.validate(); err != nil {
		return OrderResult{}, err
	}
	path := c.modePath(
		"/api/v2/trading/execution/demo/orders",
		"/api/v2/trading/execution/orders",
	)
	body, requestID, err := c.do(ctx, http.MethodPost, path, nil, req)
	if err != nil {
		return OrderResult{RequestID: requestID}, err
	}
	var res OrderResult
	if err := json.Unmarshal(body, &res); err != nil {
		return OrderResult{RequestID: requestID}, fmt.Errorf("etoro: decode order response: %w", err)
	}
	res.RequestID = requestID
	return res, nil
}

// tokenEnvelope is the minimal {token} acknowledgement several execution
// endpoints return.
type tokenEnvelope struct {
	Token string `json:"token"`
}

// CancelOrder cancels a pending order
// (DELETE /api/v2/trading/execution/{demo/}orders/{orderId}) and returns
// the tracking token. Cancelling an already closed or cancelled order is
// idempotent; an executed order can no longer be cancelled.
func (c *Client) CancelOrder(ctx context.Context, orderID int64) (string, error) {
	id := strconv.FormatInt(orderID, 10)
	path := c.modePath(
		"/api/v2/trading/execution/demo/orders/"+id,
		"/api/v2/trading/execution/orders/"+id,
	)
	body, _, err := c.do(ctx, http.MethodDelete, path, nil, nil)
	if err != nil {
		return "", err
	}
	var res tokenEnvelope
	if err := json.Unmarshal(body, &res); err != nil {
		return "", fmt.Errorf("etoro: decode cancel-order response: %w", err)
	}
	return res.Token, nil
}

// CloseOrderReceipt describes the close order created by ClosePosition.
type CloseOrderReceipt struct {
	PositionID    int64     `json:"positionID"`
	InstrumentID  int       `json:"instrumentID"`
	UnitsToDeduct float64   `json:"unitsToDeduct"`
	OrderID       int64     `json:"orderID"`
	OrderType     int       `json:"orderType"`
	StatusID      int       `json:"statusID"`
	CID           int64     `json:"CID"`
	OpenDateTime  time.Time `json:"openDateTime"`
	LastUpdate    time.Time `json:"lastUpdate"`
}

// ClosePositionResult is the acknowledgement returned by ClosePosition.
type ClosePositionResult struct {
	OrderForClose CloseOrderReceipt `json:"orderForClose"`
	Token         string            `json:"token"`
}

// ClosePosition places a market order to close a position
// (POST /api/v1/trading/execution/{demo/}market-close-orders/positions/{positionId}).
// instrumentID must be the position's instrument. A nil unitsToDeduct
// closes the whole position; a value closes that many units.
func (c *Client) ClosePosition(ctx context.Context, positionID int64, instrumentID int, unitsToDeduct *float64) (ClosePositionResult, error) {
	id := strconv.FormatInt(positionID, 10)
	path := c.modePath(
		"/api/v1/trading/execution/demo/market-close-orders/positions/"+id,
		"/api/v1/trading/execution/market-close-orders/positions/"+id,
	)
	// The demo and real specs disagree on the casing of the instrument key
	// ("InstrumentID" demo, "InstrumentId" real); send the documented form
	// for the mode in use.
	instrumentKey := "InstrumentID"
	if !c.demo {
		instrumentKey = "InstrumentId"
	}
	reqBody := map[string]any{
		instrumentKey:   instrumentID,
		"UnitsToDeduct": unitsToDeduct, // marshals to null for a full close
	}
	body, _, err := c.do(ctx, http.MethodPost, path, nil, reqBody)
	if err != nil {
		return ClosePositionResult{}, err
	}
	var res ClosePositionResult
	if err := json.Unmarshal(body, &res); err != nil {
		return ClosePositionResult{}, fmt.Errorf("etoro: decode close-position response: %w", err)
	}
	return res, nil
}

// CancelCloseOrder cancels a pending close order
// (DELETE /api/v1/trading/execution/{demo/}market-close-orders/{orderId})
// and returns the tracking token.
func (c *Client) CancelCloseOrder(ctx context.Context, orderID int64) (string, error) {
	id := strconv.FormatInt(orderID, 10)
	path := c.modePath(
		"/api/v1/trading/execution/demo/market-close-orders/"+id,
		"/api/v1/trading/execution/market-close-orders/"+id,
	)
	body, _, err := c.do(ctx, http.MethodDelete, path, nil, nil)
	if err != nil {
		return "", err
	}
	var res tokenEnvelope
	if err := json.Unmarshal(body, &res); err != nil {
		return "", fmt.Errorf("etoro: decode cancel-close-order response: %w", err)
	}
	return res.Token, nil
}

// PositionEditResult is the asynchronous acknowledgement returned by
// ModifyPositionSLTP (HTTP 202).
type PositionEditResult struct {
	OperationID string `json:"operationId"`
	PositionID  int64  `json:"positionId"`
	ReferenceID string `json:"referenceId"` // echo of the x-request-id header
}

// ModifyPositionSLTP edits a position's stop loss and/or take profit
// (PATCH /api/v2/trading/{demo/}positions/{positionId}). Rates are
// absolute; a nil pointer leaves that side unchanged, and at least one of
// the two must be set. The edit is asynchronous: a 202 acknowledgement is
// returned, not the updated position.
func (c *Client) ModifyPositionSLTP(ctx context.Context, positionID int64, stopLossRate, takeProfitRate *float64) (PositionEditResult, error) {
	if stopLossRate == nil && takeProfitRate == nil {
		return PositionEditResult{}, errors.New("etoro: at least one of stop loss or take profit is required")
	}
	id := strconv.FormatInt(positionID, 10)
	path := c.modePath(
		"/api/v2/trading/demo/positions/"+id,
		"/api/v2/trading/positions/"+id,
	)
	reqBody := struct {
		StopLossRate   *float64 `json:"stopLossRate,omitempty"`
		TakeProfitRate *float64 `json:"takeProfitRate,omitempty"`
	}{stopLossRate, takeProfitRate}
	body, _, err := c.do(ctx, http.MethodPatch, path, nil, reqBody)
	if err != nil {
		return PositionEditResult{}, err
	}
	var res PositionEditResult
	if err := json.Unmarshal(body, &res); err != nil {
		return PositionEditResult{}, fmt.Errorf("etoro: decode position-edit response: %w", err)
	}
	return res, nil
}

// OrderStatus is the state of an order in an OrderLookup.
type OrderStatus struct {
	ID           int    `json:"id"` // one of the OrderStatus* constants
	Name         string `json:"name"`
	ErrorCode    int    `json:"errorCode"` // 0 means no error
	ErrorMessage string `json:"errorMessage"`
}

// OrderAsset identifies the instrument an order trades.
type OrderAsset struct {
	Symbol         string `json:"symbol"`
	InstrumentID   int    `json:"instrumentId"`
	Currency       string `json:"currency"`
	SettlementType string `json:"settlementType"`
	Leverage       int    `json:"leverage"`
	Side           string `json:"side"` // long or short
}

// PositionOpeningData records how a position produced by an order was
// opened.
type PositionOpeningData struct {
	OpenTime          time.Time `json:"openTime"`
	OrderID           int64     `json:"orderId"`
	ExecutionTime     time.Time `json:"executionTime"`
	Units             *float64  `json:"units"`
	Contracts         *float64  `json:"contracts"`
	AvgPrice          float64   `json:"avgPrice"`
	AvgConversionRate float64   `json:"avgConversionRate"`
	MarketSpread      float64   `json:"marketSpread"`
	Markup            float64   `json:"markup"`
	PriceID           int64     `json:"priceId"`
	Fees              float64   `json:"fees"`
	Taxes             float64   `json:"taxes"`
}

// PositionExecution is a position created or affected by an order.
type PositionExecution struct {
	PositionID                     int64               `json:"positionId"`
	State                          string              `json:"state"` // open or closed
	InvestedAmountCurrency         float64             `json:"investedAmountCurrency"`
	InitialExposureAccountCurrency float64             `json:"initialExposureAccountCurrency"`
	InitialExposureAssetCurrency   float64             `json:"initialExposureAssetCurrency"`
	AddedFunds                     float64             `json:"addedFunds"`
	MarginAccountCurrency          float64             `json:"marginAccountCurrency"`
	MarginAssetCurrency            float64             `json:"marginAssetCurrency"`
	RemainingUnits                 float64             `json:"remainingUnits"`
	RemainingContracts             *float64            `json:"remainingContracts"`
	StopLossRate                   *float64            `json:"stopLossRate"`
	TakeProfitRate                 *float64            `json:"takeProfitRate"`
	OpeningData                    PositionOpeningData `json:"openingData"`
}

// OrderLookup is the full status of an order or close order.
type OrderLookup struct {
	AccountID            int64               `json:"accountId"`
	GCID                 int64               `json:"gcid"`
	PortfolioID          int                 `json:"portfolioId"`
	OrderID              int64               `json:"orderId"`
	Action               string              `json:"action"`
	Transaction          string              `json:"transaction"`
	Type                 string              `json:"type"` // mkt or mit
	EtoroOrderTypeID     int                 `json:"etoroOrderTypeId"`
	Status               OrderStatus         `json:"status"`
	Asset                OrderAsset          `json:"asset"`
	OrderCurrency        string              `json:"orderCurrency"`
	RequestedAmount      *float64            `json:"requestedAmount"`
	RequestedUnits       *float64            `json:"requestedUnits"`
	RequestedContracts   *float64            `json:"requestedContracts"`
	FrozenAmount         *float64            `json:"frozenAmount"`
	RequestedTriggerRate *float64            `json:"requestedTriggerRate"`
	OpenStopLossRate     *float64            `json:"openStopLossRate"`
	OpenTakeProfitRate   *float64            `json:"openTakeProfitRate"`
	StopLossType         string              `json:"stopLossType"`
	TotalCosts           float64             `json:"totalCosts"`
	PositionsToClose     []int64             `json:"positionsToClose"`
	PositionExecutions   []PositionExecution `json:"positionExecutions"`
	RequestTime          time.Time           `json:"requestTime"`
	LastUpdate           time.Time           `json:"lastUpdate"`
	OpenActionType       string              `json:"openActionType"`
	RequestType          string              `json:"requestType"` // byAmount, byUnits, or byContracts
}

// orderLookup fetches and decodes one OrderLookup document.
func (c *Client) orderLookup(ctx context.Context, path string, query url.Values) (OrderLookup, error) {
	body, _, err := c.do(ctx, http.MethodGet, path, query, nil)
	if err != nil {
		return OrderLookup{}, err
	}
	var res OrderLookup
	if err := json.Unmarshal(body, &res); err != nil {
		return OrderLookup{}, fmt.Errorf("etoro: decode order lookup: %w", err)
	}
	return res, nil
}

// OrderInfo looks an order up by its id
// (GET /api/v2/trading/info/{demo/}orders:lookup?orderId=).
func (c *Client) OrderInfo(ctx context.Context, orderID int64) (OrderLookup, error) {
	path := c.modePath(
		"/api/v2/trading/info/demo/orders:lookup",
		"/api/v2/trading/info/orders:lookup",
	)
	query := url.Values{"orderId": {strconv.FormatInt(orderID, 10)}}
	return c.orderLookup(ctx, path, query)
}

// OrderInfoByReference looks an order up by the x-request-id it was
// submitted with (GET /api/v2/trading/info/{demo/}orders:lookup?referenceId=).
// This recovers an order's status when the CreateOrder response was lost:
// pass the RequestID from OrderResult.
func (c *Client) OrderInfoByReference(ctx context.Context, referenceID string) (OrderLookup, error) {
	path := c.modePath(
		"/api/v2/trading/info/demo/orders:lookup",
		"/api/v2/trading/info/orders:lookup",
	)
	query := url.Values{"referenceId": {referenceID}}
	return c.orderLookup(ctx, path, query)
}

// CloseOrderInfo looks a close order up by its id
// (GET /api/v1/trading/info/{demo|real}/close-orders/{orderId}). The
// PositionsToClose and PositionExecutions fields report what was closed.
func (c *Client) CloseOrderInfo(ctx context.Context, orderID int64) (OrderLookup, error) {
	id := strconv.FormatInt(orderID, 10)
	path := c.modePath(
		"/api/v1/trading/info/demo/close-orders/"+id,
		"/api/v1/trading/info/real/close-orders/"+id,
	)
	return c.orderLookup(ctx, path, nil)
}

// PositionPnL is the live valuation attached to positions by the pnl
// endpoint. PnL is in the account currency.
type PositionPnL struct {
	PnL                       float64   `json:"pnL"`
	PnLAssetCurrency          float64   `json:"pnlAssetCurrency"`
	ExposureInAccountCurrency float64   `json:"exposureInAccountCurrency"`
	ExposureInAssetCurrency   float64   `json:"exposureInAssetCurrency"`
	MarginInAccountCurrency   float64   `json:"marginInAccountCurrency"`
	MarginInAssetCurrency     float64   `json:"marginInAssetCurrency"`
	MarginCurrencyID          int       `json:"marginCurrencyId"`
	AssetCurrencyID           int       `json:"assetCurrencyId"`
	CloseRate                 float64   `json:"closeRate"`
	CloseConversionRate       float64   `json:"closeConversionRate"`
	Timestamp                 time.Time `json:"timestamp"`
}

// Position is an open position. MirrorID is 0 for manually opened
// positions and non-zero for copy-trading positions. Note the inverted
// IsNoTakeProfit/IsNoStopLoss flags: false means the level is set.
// UnrealizedPnL is populated by PortfolioSummary only; PortfolioBreakdown
// leaves it nil.
type Position struct {
	PositionID             int64        `json:"positionID"`
	CID                    int64        `json:"CID"`
	OpenDateTime           time.Time    `json:"openDateTime"`
	OpenRate               float64      `json:"openRate"`
	InstrumentID           int          `json:"instrumentID"`
	MirrorID               int          `json:"mirrorID"`
	ParentPositionID       int64        `json:"parentPositionID"`
	IsBuy                  bool         `json:"isBuy"`
	TakeProfitRate         float64      `json:"takeProfitRate"`
	StopLossRate           float64      `json:"stopLossRate"`
	Amount                 float64      `json:"amount"` // USD margin incl. collateral
	Leverage               float64      `json:"leverage"`
	OrderID                int64        `json:"orderID"`
	OrderType              int          `json:"orderType"`
	Units                  float64      `json:"units"`
	TotalFees              float64      `json:"totalFees"` // negative = refund
	InitialAmountInDollars float64      `json:"initialAmountInDollars"`
	IsTslEnabled           bool         `json:"isTslEnabled"`
	StopLossVersion        int          `json:"stopLossVersion"`
	RedeemStatusID         int          `json:"redeemStatusID"`
	InitialUnits           float64      `json:"initialUnits"`
	IsPartiallyAltered     bool         `json:"isPartiallyAltered"`
	UnitsBaseValueDollars  float64      `json:"unitsBaseValueDollars"`
	OpenPositionActionType int          `json:"openPositionActionType"`
	SettlementTypeID       int          `json:"settlementTypeID"`
	IsDetached             bool         `json:"isDetached"`
	OpenConversionRate     float64      `json:"openConversionRate"`
	PnLVersion             int          `json:"pnlVersion"`
	TotalExternalFees      float64      `json:"totalExternalFees"`
	TotalExternalTaxes     float64      `json:"totalExternalTaxes"`
	IsNoTakeProfit         bool         `json:"isNoTakeProfit"`
	IsNoStopLoss           bool         `json:"isNoStopLoss"`
	LotCount               float64      `json:"lotCount"`
	UnrealizedPnL          *PositionPnL `json:"unrealizedPnL,omitempty"`
}

// PendingOrder is a pending market-if-touched order (the portfolio's
// orders array). Units > 0 means the order was sized by units, not amount.
type PendingOrder struct {
	OrderID        int64     `json:"orderId"`
	CID            int64     `json:"cid"`
	OpenDateTime   time.Time `json:"openDateTime"`
	InstrumentID   int       `json:"instrumentId"`
	IsBuy          bool      `json:"isBuy"`
	TakeProfitRate float64   `json:"takeProfitRate"`
	StopLossRate   float64   `json:"stopLossRate"`
	Rate           float64   `json:"rate"` // trigger rate
	Amount         float64   `json:"amount"`
	Leverage       int       `json:"leverage"`
	Units          float64   `json:"units"`
	IsTslEnabled   bool      `json:"isTslEnabled"`
	IsNoTakeProfit bool      `json:"isNoTakeProfit"`
	IsNoStopLoss   bool      `json:"isNoStopLoss"`
}

// OrderForOpen is a pending market order to open a position.
type OrderForOpen struct {
	OrderID            int64     `json:"orderId"`
	OrderType          int       `json:"orderType"`
	StatusID           int       `json:"statusId"`
	CID                int64     `json:"cid"`
	OpenDateTime       time.Time `json:"openDateTime"`
	LastUpdate         time.Time `json:"lastUpdate"`
	InstrumentID       int       `json:"instrumentId"`
	Amount             float64   `json:"amount"`
	AmountInUnits      float64   `json:"amountInUnits"`
	IsBuy              bool      `json:"isBuy"`
	Leverage           int       `json:"leverage"`
	StopLossRate       float64   `json:"stopLossRate"`
	TakeProfitRate     float64   `json:"takeProfitRate"`
	IsTslEnabled       bool      `json:"isTslEnabled"`
	MirrorID           int       `json:"mirrorId"`
	FrozenAmount       float64   `json:"frozenAmount"`
	TotalExternalCosts float64   `json:"totalExternalCosts"`
	IsNoTakeProfit     bool      `json:"isNoTakeProfit"`
	IsNoStopLoss       bool      `json:"isNoStopLoss"`
	LotCount           float64   `json:"lotCount"`
}

// OrderForClose is a pending market order to close a position.
type OrderForClose struct {
	OrderID       int64     `json:"orderId"`
	OrderType     int       `json:"orderType"`
	StatusID      int       `json:"statusId"`
	CID           int64     `json:"cid"`
	OpenDateTime  time.Time `json:"openDateTime"`
	LastUpdate    time.Time `json:"lastUpdate"`
	InstrumentID  int       `json:"instrumentId"`
	UnitsToDeduct float64   `json:"unitsToDeduct"`
	LotsToDeduct  float64   `json:"lotsToDeduct"`
	PositionID    int64     `json:"positionId"`
}

// Mirror is a copy-trading relationship and the positions held under it.
type Mirror struct {
	MirrorID                 int             `json:"mirrorID"`
	CID                      int64           `json:"CID"`
	ParentCID                int64           `json:"parentCID"`
	ParentUsername           string          `json:"parentUsername"`
	StopLossPercentage       float64         `json:"stopLossPercentage"`
	StopLossAmount           float64         `json:"stopLossAmount"`
	IsPaused                 bool            `json:"isPaused"`
	CopyExistingPositions    bool            `json:"copyExistingPositions"`
	AvailableAmount          float64         `json:"availableAmount"`
	InitialInvestment        float64         `json:"initialInvestment"`
	DepositSummary           float64         `json:"depositSummary"`
	WithdrawalSummary        float64         `json:"withdrawalSummary"`
	Positions                []Position      `json:"positions"`
	ClosedPositionsNetProfit float64         `json:"closedPositionsNetProfit"`
	StartedCopyDate          time.Time       `json:"startedCopyDate"`
	PendingForClosure        bool            `json:"pendingForClosure"`
	MirrorStatusID           int             `json:"mirrorStatusID"` // 0 active, 1 paused, 2 pending closure, 3 in alignment
	OrdersForOpen            []OrderForOpen  `json:"ordersForOpen"`
	OrdersForClose           []OrderForClose `json:"ordersForClose"`
}

// ClientPortfolio is the account's positions, pending orders, mirrors, and
// cash. Credit is the available trading balance in USD before deducting
// pending orders. UnrealizedPnL and AccountCurrencyID are populated by
// PortfolioSummary only.
type ClientPortfolio struct {
	Positions         []Position      `json:"positions"`
	Credit            float64         `json:"credit"`
	Mirrors           []Mirror        `json:"mirrors"`
	Orders            []PendingOrder  `json:"orders"`
	OrdersForOpen     []OrderForOpen  `json:"ordersForOpen"`
	OrdersForClose    []OrderForClose `json:"ordersForClose"`
	BonusCredit       float64         `json:"bonusCredit"`
	UnrealizedPnL     float64         `json:"unrealizedPnL"`
	AccountCurrencyID int             `json:"accountCurrencyId"` // 1 = USD
}

// clientPortfolio fetches and unwraps one {clientPortfolio: ...} document.
func (c *Client) clientPortfolio(ctx context.Context, path string) (ClientPortfolio, error) {
	body, _, err := c.do(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return ClientPortfolio{}, err
	}
	var envelope struct {
		ClientPortfolio ClientPortfolio `json:"clientPortfolio"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return ClientPortfolio{}, fmt.Errorf("etoro: decode portfolio: %w", err)
	}
	return envelope.ClientPortfolio, nil
}

// PortfolioSummary returns the portfolio with live valuations
// (GET /api/v1/trading/info/{demo|real}/pnl): every position carries an
// UnrealizedPnL and the account-level UnrealizedPnL and AccountCurrencyID
// are set.
func (c *Client) PortfolioSummary(ctx context.Context) (ClientPortfolio, error) {
	return c.clientPortfolio(ctx, c.modePath(
		"/api/v1/trading/info/demo/pnl",
		"/api/v1/trading/info/real/pnl",
	))
}

// PortfolioBreakdown returns the portfolio without valuations
// (GET /api/v1/trading/info/{demo/}portfolio) — the same positions,
// orders, and mirrors as PortfolioSummary, but cheaper and with no
// per-position or account-level unrealized P&L.
func (c *Client) PortfolioBreakdown(ctx context.Context) (ClientPortfolio, error) {
	return c.clientPortfolio(ctx, c.modePath(
		"/api/v1/trading/info/demo/portfolio",
		"/api/v1/trading/info/portfolio",
	))
}

// TradeHistoryParams selects the closed trades TradingHistory returns.
// MinDate is required (the start of the period); Page and PageSize are
// sent only when positive.
type TradeHistoryParams struct {
	MinDate  time.Time
	Page     int
	PageSize int
}

// ClosedTrade is one closed trade from the trading history.
type ClosedTrade struct {
	NetProfit         float64   `json:"netProfit"`
	CloseRate         float64   `json:"closeRate"`
	CloseTimestamp    time.Time `json:"closeTimestamp"`
	PositionID        int64     `json:"positionId"`
	InstrumentID      int       `json:"instrumentId"`
	IsBuy             bool      `json:"isBuy"`
	Leverage          int       `json:"leverage"`
	OpenRate          float64   `json:"openRate"`
	OpenTimestamp     time.Time `json:"openTimestamp"`
	StopLossRate      float64   `json:"stopLossRate"`
	TakeProfitRate    float64   `json:"takeProfitRate"`
	TrailingStopLoss  bool      `json:"trailingStopLoss"`
	OrderID           int64     `json:"orderId"`
	SocialTradeID     int64     `json:"socialTradeId"`
	ParentPositionID  int64     `json:"parentPositionId"`
	Investment        float64   `json:"investment"`
	InitialInvestment float64   `json:"initialInvestment"`
	Fees              float64   `json:"fees"`
	Units             float64   `json:"units"`
}

// TradingHistory returns closed trades since params.MinDate
// (GET /api/v1/trading/info/trade/{demo/}history).
func (c *Client) TradingHistory(ctx context.Context, params TradeHistoryParams) ([]ClosedTrade, error) {
	if params.MinDate.IsZero() {
		return nil, errors.New("etoro: trading history requires MinDate")
	}
	path := c.modePath(
		"/api/v1/trading/info/trade/demo/history",
		"/api/v1/trading/info/trade/history",
	)
	query := url.Values{"minDate": {params.MinDate.Format("2006-01-02")}}
	if params.Page > 0 {
		query.Set("page", strconv.Itoa(params.Page))
	}
	if params.PageSize > 0 {
		query.Set("pageSize", strconv.Itoa(params.PageSize))
	}
	body, _, err := c.do(ctx, http.MethodGet, path, query, nil)
	if err != nil {
		return nil, err
	}
	var trades []ClosedTrade
	if err := json.Unmarshal(body, &trades); err != nil {
		return nil, fmt.Errorf("etoro: decode trading history: %w", err)
	}
	return trades, nil
}

// LeverageConfig is one settlement-type/direction combination an
// instrument can be traded with.
type LeverageConfig struct {
	SettlementType              string  `json:"settlementType"`
	Direction                   string  `json:"direction"` // long or short
	LeverageValues              []int   `json:"leverageValues"`
	IsPotential                 bool    `json:"isPotential"`
	MinPositionAmount           float64 `json:"minPositionAmount"`
	AllowEditStopLoss           bool    `json:"allowEditStopLoss"`
	MinStopLossPercentage       float64 `json:"minStopLossPercentage"`
	MaxStopLossPercentage       float64 `json:"maxStopLossPercentage"`
	DefaultStopLossPercentage   float64 `json:"defaultStopLossPercentage"`
	AllowEditTakeProfit         bool    `json:"allowEditTakeProfit"`
	MinTakeProfitPercentage     float64 `json:"minTakeProfitPercentage"`
	MaxTakeProfitPercentage     float64 `json:"maxTakeProfitPercentage"`
	DefaultTakeProfitPercentage float64 `json:"defaultTakeProfitPercentage"`
	AllowStopLossTakeProfit     bool    `json:"allowStopLossTakeProfit"`
}

// InstrumentEligibility is an instrument's trading configuration: what
// order types it allows, how it is sized, and the valid leverage values
// per settlement type and direction.
type InstrumentEligibility struct {
	InstrumentID                  int              `json:"instrumentId"`
	Symbol                        string           `json:"symbol"`
	MinPositionExposure           float64          `json:"minPositionExposure"`
	MaxUnitsPerOrder              float64          `json:"maxUnitsPerOrder"`
	AllowOpenPosition             bool             `json:"allowOpenPosition"`
	AllowClosePosition            bool             `json:"allowClosePosition"`
	AllowPartialClosePosition     bool             `json:"allowPartialClosePosition"`
	AllowMitOrders                bool             `json:"allowMitOrders"`
	AllowEntryOrders              bool             `json:"allowEntryOrders"`
	AllowExitOrders               bool             `json:"allowExitOrders"`
	AllowTrailingStopLoss         bool             `json:"allowTrailingStopLoss"`
	RequiresW8Ben                 *bool            `json:"requiresW8Ben"`
	UnitsQuantityType             string           `json:"unitsQuantityType"` // whole or fractional
	OrderFillBehaviorType         string           `json:"orderFillBehaviorType"`
	AllowedOrderQuantityType      string           `json:"allowedOrderQuantityType"`
	TradeUnitType                 string           `json:"tradeUnitType"`
	InitialMarginInAssetCurrency  *float64         `json:"initialMarginInAssetCurrency"`
	StopLossMarginInAssetCurrency *float64         `json:"stopLossMarginInAssetCurrency"`
	AdditionalBufferPercent       *float64         `json:"additionalBufferPercent"`
	LeverageConfigs               []LeverageConfig `json:"leverageConfigs"`
}

// TradingEligibility returns the trading configuration for one instrument
// (POST /api/v2/trading/info/{demo/}eligibility). Consult it before
// ordering: it carries the valid leverage values, sizing mode, fractional
// support, and SL/TP bounds. An unknown instrument id is an error.
func (c *Client) TradingEligibility(ctx context.Context, instrumentID int) (InstrumentEligibility, error) {
	path := c.modePath(
		"/api/v2/trading/info/demo/eligibility",
		"/api/v2/trading/info/eligibility",
	)
	reqBody := struct {
		InstrumentIDs []int `json:"instrumentIds"`
	}{[]int{instrumentID}}
	body, _, err := c.do(ctx, http.MethodPost, path, nil, reqBody)
	if err != nil {
		return InstrumentEligibility{}, err
	}
	var envelope struct {
		Eligibilities []InstrumentEligibility `json:"eligibilities"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return InstrumentEligibility{}, fmt.Errorf("etoro: decode eligibility: %w", err)
	}
	for _, e := range envelope.Eligibilities {
		if e.InstrumentID == instrumentID {
			return e, nil
		}
	}
	return InstrumentEligibility{}, fmt.Errorf("etoro: no eligibility data for instrument %d", instrumentID)
}

// TradeCost is one cost component of a hypothetical order.
type TradeCost struct {
	CostType string  `json:"costType"` // markup, marketSpread, transactionFee, overnightFee, overWeekendFee, or sdrt
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}

// CostEstimate is the what-if cost breakdown of an order.
type CostEstimate struct {
	InstrumentID int         `json:"instrumentId"`
	Symbol       string      `json:"symbol"`
	Costs        []TradeCost `json:"costs"`
	LastUpdated  time.Time   `json:"lastUpdated"`
}

// TradingCostEstimate prices an order without placing it
// (POST /api/v2/trading/info/{demo/}costs). The request takes the same
// shape and client-side validation as CreateOrder.
func (c *Client) TradingCostEstimate(ctx context.Context, req OrderRequest) (CostEstimate, error) {
	if err := req.validate(); err != nil {
		return CostEstimate{}, err
	}
	path := c.modePath(
		"/api/v2/trading/info/demo/costs",
		"/api/v2/trading/info/costs",
	)
	body, _, err := c.do(ctx, http.MethodPost, path, nil, req)
	if err != nil {
		return CostEstimate{}, err
	}
	var res CostEstimate
	if err := json.Unmarshal(body, &res); err != nil {
		return CostEstimate{}, fmt.Errorf("etoro: decode cost estimate: %w", err)
	}
	return res, nil
}
