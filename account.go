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

// AccountType identifies an account category in balance requests, used both
// as a path segment and as an accountTypes query filter.
type AccountType string

// Account types accepted by the balance endpoints.
const (
	AccountTypeTrading   AccountType = "Trading"
	AccountTypeCash      AccountType = "Cash"
	AccountTypeOptions   AccountType = "Options"
	AccountTypeCrypto    AccountType = "Crypto"
	AccountTypeMoneyFarm AccountType = "MoneyFarm"
	AccountTypeSpaceship AccountType = "Spaceship"
)

// UnmarshalJSON accepts both the named form documented for request
// parameters ("Trading", "Cash", ...) and the bare numeric code the live
// service has been observed to return in balance payloads (for example 1
// for the trading account). Numeric codes are kept as their decimal string.
func (a *AccountType) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*a = AccountType(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return fmt.Errorf("etoro: accountType must be a JSON string or number")
	}
	*a = AccountType(n.String())
	return nil
}

// Balance is the current-balance response shared by the balance endpoints:
// the total across the returned accounts plus a per-account breakdown.
type Balance struct {
	GCID            int64            `json:"gcid"`
	TotalBalance    float64          `json:"totalBalance"`
	DisplayCurrency string           `json:"displayCurrency"`
	Balances        []AccountBalance `json:"balances"`
}

// AccountBalance is one account's balance within a Balance response.
type AccountBalance struct {
	AccountID       string         `json:"accountId"`
	AccountType     AccountType    `json:"accountType"`
	SubType         string         `json:"subType"`
	Balance         float64        `json:"balance"` // in the account's native currency
	Currency        string         `json:"currency"`
	DisplayBalance  float64        `json:"displayBalance"`
	DisplayCurrency string         `json:"displayCurrency"`
	ExchangeRate    float64        `json:"exchangeRate"`
	EquityDetails   *EquityDetails `json:"equityDetails"` // only when requested with expand=equityDetails
}

// EquityDetails carries provider-specific equity figures for an account.
// Every field is nullable and which ones are populated depends on the
// account type (trading accounts fill the margin fields, crypto accounts
// the crypto ones).
type EquityDetails struct {
	Available              *float64 `json:"available"`  // buying power (Trading/Options/MoneyFarm/Cash)
	FrozenCash             *float64 `json:"frozenCash"` // Trading
	CurrentPNL             *float64 `json:"currentPNL"` // Trading
	TotalUsedMargin        *float64 `json:"totalUsedMargin"`
	CryptoID               *int     `json:"cryptoId"`
	Balance                *float64 `json:"balance"`
	TotalBalance           *float64 `json:"totalBalance"`
	SpendableBalance       *float64 `json:"spendableBalance"`
	BalanceInFiat          *float64 `json:"balanceInFiat"`
	TotalBalanceInFiat     *float64 `json:"totalBalanceInFiat"`
	SpendableBalanceInFiat *float64 `json:"spendableBalanceInFiat"`
	FiatConversionCurrency string   `json:"fiatConversionCurrency"`
	OrderIndex             *int     `json:"orderIndex"`
}

// Balance returns the current balance of the accounts that hold a non-zero
// balance, in the default display currency (USD).
//
// GET /api/v1/balances
func (c *Client) Balance(ctx context.Context) (Balance, error) {
	return c.getBalance(ctx, "/api/v1/balances", nil)
}

// AggregatedBalances returns the current balance of every account,
// including zero-balance accounts, with per-account equity details
// expanded.
//
// GET /api/v1/balances?expand=equityDetails&includeZeroBalances=true
func (c *Client) AggregatedBalances(ctx context.Context) (Balance, error) {
	q := url.Values{
		"expand":              {"equityDetails"},
		"includeZeroBalances": {"true"},
	}
	return c.getBalance(ctx, "/api/v1/balances", q)
}

// BalancesByType returns the current balance of the accounts of one type,
// for example AccountTypeTrading.
//
// GET /api/v1/balances/{accountType}
func (c *Client) BalancesByType(ctx context.Context, accountType AccountType) (Balance, error) {
	return c.getBalance(ctx, "/api/v1/balances/"+url.PathEscape(string(accountType)), nil)
}

func (c *Client) getBalance(ctx context.Context, path string, q url.Values) (Balance, error) {
	body, _, err := c.do(ctx, http.MethodGet, path, q, nil)
	if err != nil {
		return Balance{}, err
	}
	var out Balance
	if err := json.Unmarshal(body, &out); err != nil {
		return Balance{}, fmt.Errorf("etoro: decode balance response: %w", err)
	}
	return out, nil
}

// BalanceHistoryParams filters a BalanceHistory request. The zero value
// requests the server default: all account types over the last 30 days,
// in USD. History is limited to the last 12 months and a range may span at
// most 365 days; the server responds 404 when no data exists for the range.
type BalanceHistoryParams struct {
	AccountTypes    []AccountType // optional filter; empty = all types
	DisplayCurrency string        // optional ISO 4217 code; empty = USD
	FromDate        time.Time     // optional; sent as YYYY-MM-DD
	ToDate          time.Time     // optional; sent as YYYY-MM-DD
}

func (p BalanceHistoryParams) query() url.Values {
	q := url.Values{}
	if len(p.AccountTypes) > 0 {
		names := make([]string, len(p.AccountTypes))
		for i, t := range p.AccountTypes {
			names[i] = string(t)
		}
		q.Set("accountTypes", strings.Join(names, ","))
	}
	if p.DisplayCurrency != "" {
		q.Set("displayCurrency", p.DisplayCurrency)
	}
	if !p.FromDate.IsZero() {
		q.Set("fromDate", p.FromDate.Format("2006-01-02"))
	}
	if !p.ToDate.IsZero() {
		q.Set("toDate", p.ToDate.Format("2006-01-02"))
	}
	return q
}

// BalanceHistory is a series of daily balance snapshots.
type BalanceHistory struct {
	GCID            int64             `json:"gcid"`
	DisplayCurrency string            `json:"displayCurrency"`
	FromDate        string            `json:"fromDate"` // YYYY-MM-DD
	ToDate          string            `json:"toDate"`   // YYYY-MM-DD
	Snapshots       []BalanceSnapshot `json:"snapshots"`
}

// BalanceSnapshot is the account-wide state on one day.
type BalanceSnapshot struct {
	Date                       string            `json:"date"`
	TotalCurrencyISO           string            `json:"totalCurrencyIso"`
	TotalCash                  float64           `json:"totalCash"`
	TotalInvestedAmount        float64           `json:"totalInvestedAmount"`
	TotalPnl                   float64           `json:"totalPnl"`
	TotalBalance               float64           `json:"totalBalance"`
	DisplayTotalCash           float64           `json:"displayTotalCash"`
	DisplayTotalInvestedAmount float64           `json:"displayTotalInvestedAmount"`
	DisplayTotalPnl            float64           `json:"displayTotalPnl"`
	DisplayTotalBalance        float64           `json:"displayTotalBalance"`
	TotalExchangeRate          float64           `json:"totalExchangeRate"`
	AccountSnapshots           []AccountSnapshot `json:"accountSnapshots"`
}

// AccountSnapshot is one account's state within a BalanceSnapshot.
type AccountSnapshot struct {
	AccountID             string      `json:"accountId"`
	AccountType           AccountType `json:"accountType"`
	Currency              string      `json:"currency"`
	Cash                  float64     `json:"cash"`
	InvestedAmount        float64     `json:"investedAmount"`
	Pnl                   float64     `json:"pnl"`
	Total                 float64     `json:"total"`
	USDRate               float64     `json:"usdRate"`
	DisplayCash           float64     `json:"displayCash"`
	DisplayInvestedAmount float64     `json:"displayInvestedAmount"`
	DisplayPnl            float64     `json:"displayPnl"`
	DisplayTotal          float64     `json:"displayTotal"`
	ExchangeRate          float64     `json:"exchangeRate"`
}

// BalanceHistory returns daily balance snapshots for the range selected by
// params, across all account types.
//
// GET /api/v1/balances/history
func (c *Client) BalanceHistory(ctx context.Context, params BalanceHistoryParams) (BalanceHistory, error) {
	body, _, err := c.do(ctx, http.MethodGet, "/api/v1/balances/history", params.query(), nil)
	if err != nil {
		return BalanceHistory{}, err
	}
	var out BalanceHistory
	if err := json.Unmarshal(body, &out); err != nil {
		return BalanceHistory{}, fmt.Errorf("etoro: decode balance history response: %w", err)
	}
	return out, nil
}

// Identity is the authenticated user's profile, including the customer ids
// of the real and demo accounts and the OAuth scopes granted to the
// credential pair. Fetch it once to verify credentials and discover what
// the keys are allowed to do.
type Identity struct {
	GCID        int64    `json:"gcid"`    // global customer id
	RealCID     int64    `json:"realCid"` // real-account customer id
	DemoCID     int64    `json:"demoCid"` // demo-account customer id
	Username    string   `json:"username"`
	FirstName   string   `json:"firstName"`
	MiddleName  string   `json:"middleName"`
	LastName    string   `json:"lastName"`
	PlayerLevel int      `json:"playerLevel"` // 1 Bronze, 2 Platinum, 3 Gold, 4 Internal, 5 Silver, 6 PlatinumPlus, 7 Diamond
	Gender      int      `json:"gender"`      // 0 unknown, 1 male, 2 female
	Language    int      `json:"language"`    // 1 English, 2 German, ...
	DateOfBirth string   `json:"dateOfBirth"`
	AvatarURL   string   `json:"avatarUrl"`
	Scopes      []string `json:"scopes"` // OAuth scopes granted to this credential pair
}

// Identity returns the authenticated user's profile.
//
// GET /api/v1/me
func (c *Client) Identity(ctx context.Context) (Identity, error) {
	body, _, err := c.do(ctx, http.MethodGet, "/api/v1/me", nil, nil)
	if err != nil {
		return Identity{}, err
	}
	var out Identity
	if err := json.Unmarshal(body, &out); err != nil {
		return Identity{}, fmt.Errorf("etoro: decode identity response: %w", err)
	}
	return out, nil
}

// CashTransactionsParams selects a page of cash-account transactions. The
// zero value requests the first page at the server default size (50).
type CashTransactionsParams struct {
	PageSize  int    // optional; server default 50, max 500
	PageToken string // opaque cursor from a previous page's Pagination.NextPageToken
}

func (p CashTransactionsParams) query() url.Values {
	q := url.Values{}
	if p.PageSize > 0 {
		q.Set("pageSize", strconv.Itoa(p.PageSize))
	}
	if p.PageToken != "" {
		q.Set("pageToken", p.PageToken)
	}
	return q
}

// CashTransactionsPage is one page of a cash account's transactions.
type CashTransactionsPage struct {
	Results    []CashTransaction `json:"results"`
	Pagination CashPagination    `json:"pagination"`
}

// CashPagination is the cursor state of a CashTransactionsPage. Feed
// NextPageToken into the next request's CashTransactionsParams.PageToken
// while HasNext is true.
type CashPagination struct {
	PageSize      int    `json:"pageSize"`
	NextPageToken string `json:"nextPageToken"` // empty on the last page
	HasNext       bool   `json:"hasNext"`
}

// CashTransaction is one movement on a cash account. Monetary amounts are
// decimal strings as sent by the API, preserving exact precision.
type CashTransaction struct {
	ID                                 string                              `json:"id"`
	AccountID                          string                              `json:"accountId"`
	TransactionType                    string                              `json:"transactionType"`    // card, internalTransfer, bankTransfer, balanceAdjustment
	TransactionSubtype                 string                              `json:"transactionSubtype"` // cardPayment, transferReceived, refund, fee, ...
	Direction                          string                              `json:"direction"`          // debit or credit
	Status                             string                              `json:"status"`             // failed, authorized, settled, rejected, returned, expired, unknown
	Amount                             string                              `json:"amount"`             // decimal string
	Currency                           string                              `json:"currency"`           // ISO 4217
	OriginalAmount                     string                              `json:"originalAmount"`
	OriginalCurrency                   string                              `json:"originalCurrency"`
	ConversionRate                     string                              `json:"conversionRate"`
	PostedAt                           time.Time                           `json:"postedAt"`
	Counterparty                       CashCounterparty                    `json:"counterparty"`
	CardTransactionDetails             *CardTransactionDetails             `json:"cardTransactionDetails"`
	BankTransferTransactionDetails     *BankTransferTransactionDetails     `json:"bankTransferTransactionDetails"`
	InternalTransferTransactionDetails *InternalTransferTransactionDetails `json:"internalTransferTransactionDetails"`
}

// CashCounterparty is the other party of a cash transaction.
type CashCounterparty struct {
	Name string `json:"name"`
	Type string `json:"type"` // merchant, bank_account, internal_account, unknown
}

// CardTransactionDetails is set on card transactions.
type CardTransactionDetails struct {
	CardID              string `json:"cardId"`
	MerchantName        string `json:"merchantName"`
	Country             string `json:"country"`
	AuthorizationStatus string `json:"authorizationStatus"`
}

// BankTransferTransactionDetails is set on bank-transfer transactions.
type BankTransferTransactionDetails struct {
	BankIdentifier   []BankIdentifier `json:"bankIdentifier"`
	Description      string           `json:"description"`
	PaymentReference string           `json:"paymentReference"`
}

// BankIdentifier is one named identifier of a counterparty bank account,
// for example an IBAN or a sort code.
type BankIdentifier struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// InternalTransferTransactionDetails is set on transfers between eToro
// accounts.
type InternalTransferTransactionDetails struct {
	TransferID string `json:"transferId"`
}

// CashTransactions returns one page of transactions for a cash account.
// The accountID must be the cash account's id (a UUID) as reported by the
// balance endpoints, not the numeric trading account id.
//
// GET /api/v1/money/accounts/cash/{accountId}/transactions
func (c *Client) CashTransactions(ctx context.Context, accountID string, params CashTransactionsParams) (CashTransactionsPage, error) {
	path := "/api/v1/money/accounts/cash/" + url.PathEscape(accountID) + "/transactions"
	body, _, err := c.do(ctx, http.MethodGet, path, params.query(), nil)
	if err != nil {
		return CashTransactionsPage{}, err
	}
	var out CashTransactionsPage
	if err := json.Unmarshal(body, &out); err != nil {
		return CashTransactionsPage{}, fmt.Errorf("etoro: decode cash transactions response: %w", err)
	}
	return out, nil
}
