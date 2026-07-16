package etoro

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// balanceFixture mirrors the live response shape: accountType arrives as a
// bare number, and gcid/amounts are numeric.
const balanceFixture = `{
	"gcid": 12345678,
	"totalBalance": 1500.25,
	"displayCurrency": "USD",
	"balances": [
		{
			"accountId": "20000001",
			"accountType": 1,
			"subType": null,
			"balance": 1500.25,
			"currency": "USD",
			"displayBalance": 1500.25,
			"displayCurrency": "USD",
			"exchangeRate": 1.0
		}
	]
}`

func checkBalanceFixture(t *testing.T, got Balance) {
	t.Helper()
	if got.GCID != 12345678 {
		t.Errorf("GCID = %d, want 12345678", got.GCID)
	}
	if got.TotalBalance != 1500.25 {
		t.Errorf("TotalBalance = %v, want 1500.25", got.TotalBalance)
	}
	if got.DisplayCurrency != "USD" {
		t.Errorf("DisplayCurrency = %q, want USD", got.DisplayCurrency)
	}
	if len(got.Balances) != 1 {
		t.Fatalf("len(Balances) = %d, want 1", len(got.Balances))
	}
	b := got.Balances[0]
	if b.AccountID != "20000001" {
		t.Errorf("AccountID = %q, want 20000001", b.AccountID)
	}
	if b.AccountType != "1" {
		t.Errorf("AccountType = %q, want numeric code kept as %q", b.AccountType, "1")
	}
	if b.Currency != "USD" || b.Balance != 1500.25 || b.ExchangeRate != 1.0 {
		t.Errorf("account amounts = %+v, want balance 1500.25 USD at rate 1.0", b)
	}
}

func TestBalance(t *testing.T) {
	// The balance path is mode-independent: demo and real clients must hit
	// the exact same route.
	for _, tc := range []struct {
		name string
		opts []Option
	}{
		{"demo", nil},
		{"real", []Option{Real()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotMethod, gotPath, gotQuery, gotAPIKey, gotRequestID string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				gotPath = r.URL.Path
				gotQuery = r.URL.RawQuery
				gotAPIKey = r.Header.Get("x-api-key")
				gotRequestID = r.Header.Get("x-request-id")
				w.Write([]byte(balanceFixture))
			}))
			defer srv.Close()

			c := testClient(srv)
			for _, o := range tc.opts {
				o(c)
			}
			got, err := c.Balance(t.Context())
			if err != nil {
				t.Fatalf("Balance: %v", err)
			}
			if gotMethod != http.MethodGet {
				t.Errorf("method = %q, want GET", gotMethod)
			}
			if gotPath != "/api/v1/balances" {
				t.Errorf("path = %q, want /api/v1/balances", gotPath)
			}
			if gotQuery != "" {
				t.Errorf("query = %q, want empty", gotQuery)
			}
			if gotAPIKey != testAPIKey {
				t.Errorf("x-api-key = %q, want %q", gotAPIKey, testAPIKey)
			}
			if !uuidV4.MatchString(gotRequestID) {
				t.Errorf("x-request-id = %q, not a canonical UUID v4", gotRequestID)
			}
			checkBalanceFixture(t, got)
		})
	}
}

func TestAggregatedBalances(t *testing.T) {
	var gotPath string
	var gotQuery map[string][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query()
		w.Write([]byte(`{
			"gcid": 12345678,
			"totalBalance": 1500.25,
			"displayCurrency": "USD",
			"balances": [
				{
					"accountId": "20000001",
					"accountType": 1,
					"balance": 1500.25,
					"currency": "USD",
					"displayBalance": 1500.25,
					"displayCurrency": "USD",
					"exchangeRate": 1.0,
					"equityDetails": {
						"available": 1400.0,
						"frozenCash": 50.5,
						"currentPNL": -12.25,
						"totalUsedMargin": 100.0
					}
				},
				{
					"accountId": "c0ffee00-0000-4000-8000-000000000001",
					"accountType": "Cash",
					"balance": 0,
					"currency": "EUR",
					"displayBalance": 0,
					"displayCurrency": "USD",
					"exchangeRate": 1.08
				}
			]
		}`))
	}))
	defer srv.Close()

	got, err := testClient(srv).AggregatedBalances(t.Context())
	if err != nil {
		t.Fatalf("AggregatedBalances: %v", err)
	}
	if gotPath != "/api/v1/balances" {
		t.Errorf("path = %q, want /api/v1/balances", gotPath)
	}
	for key, want := range map[string]string{
		"expand":              "equityDetails",
		"includeZeroBalances": "true",
	} {
		if vs := gotQuery[key]; len(vs) != 1 || vs[0] != want {
			t.Errorf("query %s = %v, want [%q]", key, vs, want)
		}
	}
	if len(got.Balances) != 2 {
		t.Fatalf("len(Balances) = %d, want 2", len(got.Balances))
	}
	ed := got.Balances[0].EquityDetails
	if ed == nil {
		t.Fatal("Balances[0].EquityDetails = nil, want populated")
	}
	if ed.Available == nil || *ed.Available != 1400.0 {
		t.Errorf("Available = %v, want 1400.0", ed.Available)
	}
	if ed.CurrentPNL == nil || *ed.CurrentPNL != -12.25 {
		t.Errorf("CurrentPNL = %v, want -12.25", ed.CurrentPNL)
	}
	if ed.CryptoID != nil {
		t.Errorf("CryptoID = %v, want nil (absent in payload)", *ed.CryptoID)
	}
	if got.Balances[1].EquityDetails != nil {
		t.Error("Balances[1].EquityDetails should be nil when absent")
	}
	if got.Balances[1].AccountType != AccountTypeCash {
		t.Errorf("Balances[1].AccountType = %q, want %q", got.Balances[1].AccountType, AccountTypeCash)
	}
}

func TestBalancesByType(t *testing.T) {
	for _, accountType := range []AccountType{
		AccountTypeTrading,
		AccountTypeCash,
		AccountTypeCrypto,
	} {
		t.Run(string(accountType), func(t *testing.T) {
			var gotPath, gotQuery string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				gotQuery = r.URL.RawQuery
				w.Write([]byte(balanceFixture))
			}))
			defer srv.Close()

			got, err := testClient(srv).BalancesByType(t.Context(), accountType)
			if err != nil {
				t.Fatalf("BalancesByType: %v", err)
			}
			if want := "/api/v1/balances/" + string(accountType); gotPath != want {
				t.Errorf("path = %q, want %q", gotPath, want)
			}
			if gotQuery != "" {
				t.Errorf("query = %q, want empty", gotQuery)
			}
			checkBalanceFixture(t, got)
		})
	}
}

func TestBalanceHistory(t *testing.T) {
	fixture := `{
		"gcid": 12345678,
		"displayCurrency": "EUR",
		"fromDate": "2026-06-01",
		"toDate": "2026-06-30",
		"snapshots": [
			{
				"date": "2026-06-01",
				"totalCurrencyIso": "USD",
				"totalCash": 1000,
				"totalInvestedAmount": 500,
				"totalPnl": 25.5,
				"totalBalance": 1525.5,
				"displayTotalCash": 920,
				"displayTotalInvestedAmount": 460,
				"displayTotalPnl": 23.46,
				"displayTotalBalance": 1403.46,
				"totalExchangeRate": 0.92,
				"accountSnapshots": [
					{
						"accountId": "20000001",
						"accountType": 1,
						"currency": "USD",
						"cash": 1000,
						"investedAmount": 500,
						"pnl": 25.5,
						"total": 1525.5,
						"usdRate": 1,
						"displayCash": 920,
						"displayInvestedAmount": 460,
						"displayPnl": 23.46,
						"displayTotal": 1403.46,
						"exchangeRate": 0.92
					}
				]
			}
		]
	}`
	for _, tc := range []struct {
		name      string
		params    BalanceHistoryParams
		wantQuery map[string]string
	}{
		{
			name:      "defaults",
			params:    BalanceHistoryParams{},
			wantQuery: map[string]string{},
		},
		{
			name: "full filter",
			params: BalanceHistoryParams{
				AccountTypes:    []AccountType{AccountTypeTrading, AccountTypeCash},
				DisplayCurrency: "EUR",
				FromDate:        time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
				ToDate:          time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC),
			},
			wantQuery: map[string]string{
				"accountTypes":    "Trading,Cash",
				"displayCurrency": "EUR",
				"fromDate":        "2026-06-01",
				"toDate":          "2026-06-30",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotPath string
			var gotQuery map[string][]string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				gotQuery = r.URL.Query()
				w.Write([]byte(fixture))
			}))
			defer srv.Close()

			got, err := testClient(srv).BalanceHistory(t.Context(), tc.params)
			if err != nil {
				t.Fatalf("BalanceHistory: %v", err)
			}
			if gotPath != "/api/v1/balances/history" {
				t.Errorf("path = %q, want /api/v1/balances/history", gotPath)
			}
			if len(gotQuery) != len(tc.wantQuery) {
				t.Errorf("query = %v, want exactly %v", gotQuery, tc.wantQuery)
			}
			for key, want := range tc.wantQuery {
				if vs := gotQuery[key]; len(vs) != 1 || vs[0] != want {
					t.Errorf("query %s = %v, want [%q]", key, vs, want)
				}
			}
			if got.GCID != 12345678 || got.DisplayCurrency != "EUR" {
				t.Errorf("envelope = %+v, want gcid 12345678 in EUR", got)
			}
			if got.FromDate != "2026-06-01" || got.ToDate != "2026-06-30" {
				t.Errorf("range = %q..%q, want 2026-06-01..2026-06-30", got.FromDate, got.ToDate)
			}
			if len(got.Snapshots) != 1 {
				t.Fatalf("len(Snapshots) = %d, want 1", len(got.Snapshots))
			}
			snap := got.Snapshots[0]
			if snap.Date != "2026-06-01" || snap.TotalBalance != 1525.5 || snap.TotalPnl != 25.5 {
				t.Errorf("snapshot = %+v, want 2026-06-01 with totalBalance 1525.5 and pnl 25.5", snap)
			}
			if len(snap.AccountSnapshots) != 1 {
				t.Fatalf("len(AccountSnapshots) = %d, want 1", len(snap.AccountSnapshots))
			}
			acct := snap.AccountSnapshots[0]
			if acct.AccountID != "20000001" || acct.AccountType != "1" || acct.Total != 1525.5 {
				t.Errorf("account snapshot = %+v, want account 20000001 (type 1) totalling 1525.5", acct)
			}
		})
	}
}

func TestIdentity(t *testing.T) {
	var gotMethod, gotPath, gotUserKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotUserKey = r.Header.Get("x-user-key")
		w.Write([]byte(`{
			"gcid": 12345678,
			"realCid": 11111111,
			"demoCid": 22222222,
			"username": "sampletrader",
			"firstName": "Sam",
			"lastName": "Trader",
			"playerLevel": 3,
			"gender": 0,
			"language": 1,
			"dateOfBirth": "1990-01-02T00:00:00Z",
			"avatarUrl": "https://example.invalid/avatar.png",
			"scopes": ["market-data:read", "trade.demo:write"]
		}`))
	}))
	defer srv.Close()

	got, err := testClient(srv).Identity(t.Context())
	if err != nil {
		t.Fatalf("Identity: %v", err)
	}
	if gotMethod != http.MethodGet || gotPath != "/api/v1/me" {
		t.Errorf("request = %s %s, want GET /api/v1/me", gotMethod, gotPath)
	}
	if gotUserKey != testUserKey {
		t.Errorf("x-user-key = %q, want %q", gotUserKey, testUserKey)
	}
	want := Identity{
		GCID:        12345678,
		RealCID:     11111111,
		DemoCID:     22222222,
		Username:    "sampletrader",
		FirstName:   "Sam",
		LastName:    "Trader",
		PlayerLevel: 3,
		Language:    1,
		DateOfBirth: "1990-01-02T00:00:00Z",
		AvatarURL:   "https://example.invalid/avatar.png",
		Scopes:      []string{"market-data:read", "trade.demo:write"},
	}
	if got.GCID != want.GCID || got.RealCID != want.RealCID || got.DemoCID != want.DemoCID ||
		got.Username != want.Username || got.PlayerLevel != want.PlayerLevel ||
		got.DateOfBirth != want.DateOfBirth || got.AvatarURL != want.AvatarURL {
		t.Errorf("Identity = %+v, want %+v", got, want)
	}
	if len(got.Scopes) != 2 || got.Scopes[0] != "market-data:read" || got.Scopes[1] != "trade.demo:write" {
		t.Errorf("Scopes = %v, want %v", got.Scopes, want.Scopes)
	}
}

func TestCashTransactions(t *testing.T) {
	const accountID = "c0ffee00-0000-4000-8000-000000000001"
	fixture := `{
		"results": [
			{
				"id": "tx-1",
				"accountId": "` + accountID + `",
				"transactionType": "card",
				"transactionSubtype": "cardPayment",
				"direction": "debit",
				"status": "settled",
				"amount": "12.34",
				"currency": "EUR",
				"originalAmount": "13.50",
				"originalCurrency": "USD",
				"conversionRate": "0.9141",
				"postedAt": "2026-07-15T09:30:00Z",
				"counterparty": {"name": "Grocer", "type": "merchant"},
				"cardTransactionDetails": {
					"cardId": "card-9",
					"merchantName": "Grocer",
					"country": "DE",
					"authorizationStatus": "normal"
				}
			},
			{
				"id": "tx-2",
				"accountId": "` + accountID + `",
				"transactionType": "internalTransfer",
				"transactionSubtype": "transferReceived",
				"direction": "credit",
				"status": "settled",
				"amount": "100.00",
				"currency": "EUR",
				"postedAt": "2026-07-14T08:00:00Z",
				"counterparty": {"name": "Trading account", "type": "internal_account"},
				"internalTransferTransactionDetails": {"transferId": "tr-7"}
			}
		],
		"pagination": {"pageSize": 100, "nextPageToken": "cursor-2", "hasNext": true}
	}`
	for _, tc := range []struct {
		name      string
		params    CashTransactionsParams
		wantQuery map[string]string
	}{
		{
			name:      "defaults",
			params:    CashTransactionsParams{},
			wantQuery: map[string]string{},
		},
		{
			name:      "sized page with cursor",
			params:    CashTransactionsParams{PageSize: 100, PageToken: "cursor-1"},
			wantQuery: map[string]string{"pageSize": "100", "pageToken": "cursor-1"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotMethod, gotPath string
			var gotQuery map[string][]string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				gotPath = r.URL.Path
				gotQuery = r.URL.Query()
				w.Write([]byte(fixture))
			}))
			defer srv.Close()

			got, err := testClient(srv).CashTransactions(t.Context(), accountID, tc.params)
			if err != nil {
				t.Fatalf("CashTransactions: %v", err)
			}
			if gotMethod != http.MethodGet {
				t.Errorf("method = %q, want GET", gotMethod)
			}
			if want := "/api/v1/money/accounts/cash/" + accountID + "/transactions"; gotPath != want {
				t.Errorf("path = %q, want %q", gotPath, want)
			}
			if len(gotQuery) != len(tc.wantQuery) {
				t.Errorf("query = %v, want exactly %v", gotQuery, tc.wantQuery)
			}
			for key, want := range tc.wantQuery {
				if vs := gotQuery[key]; len(vs) != 1 || vs[0] != want {
					t.Errorf("query %s = %v, want [%q]", key, vs, want)
				}
			}
			if len(got.Results) != 2 {
				t.Fatalf("len(Results) = %d, want 2", len(got.Results))
			}
			tx := got.Results[0]
			if tx.ID != "tx-1" || tx.TransactionType != "card" || tx.Direction != "debit" || tx.Status != "settled" {
				t.Errorf("tx-1 = %+v, want settled card debit", tx)
			}
			if tx.Amount != "12.34" || tx.Currency != "EUR" || tx.ConversionRate != "0.9141" {
				t.Errorf("tx-1 amounts = %q %q rate %q, want decimal strings preserved", tx.Amount, tx.Currency, tx.ConversionRate)
			}
			if want := time.Date(2026, 7, 15, 9, 30, 0, 0, time.UTC); !tx.PostedAt.Equal(want) {
				t.Errorf("tx-1 PostedAt = %v, want %v", tx.PostedAt, want)
			}
			if tx.Counterparty.Name != "Grocer" || tx.Counterparty.Type != "merchant" {
				t.Errorf("tx-1 counterparty = %+v, want merchant Grocer", tx.Counterparty)
			}
			if tx.CardTransactionDetails == nil || tx.CardTransactionDetails.Country != "DE" {
				t.Errorf("tx-1 card details = %+v, want country DE", tx.CardTransactionDetails)
			}
			if tx.InternalTransferTransactionDetails != nil {
				t.Error("tx-1 internal transfer details should be nil")
			}
			tx2 := got.Results[1]
			if tx2.InternalTransferTransactionDetails == nil || tx2.InternalTransferTransactionDetails.TransferID != "tr-7" {
				t.Errorf("tx-2 internal transfer details = %+v, want transferId tr-7", tx2.InternalTransferTransactionDetails)
			}
			if tx2.CardTransactionDetails != nil {
				t.Error("tx-2 card details should be nil")
			}
			p := got.Pagination
			if p.PageSize != 100 || p.NextPageToken != "cursor-2" || !p.HasNext {
				t.Errorf("pagination = %+v, want pageSize 100, cursor-2, hasNext", p)
			}
		})
	}
}

func TestAccountTypeUnmarshalJSON(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want AccountType
	}{
		{`"Trading"`, AccountTypeTrading},
		{`"Cash"`, AccountTypeCash},
		{`1`, "1"},
		{`42`, "42"},
		{`null`, ""},
	} {
		var got AccountType
		if err := json.Unmarshal([]byte(tc.in), &got); err != nil {
			t.Errorf("unmarshal %s: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("unmarshal %s = %q, want %q", tc.in, got, tc.want)
		}
	}
	var got AccountType
	if err := json.Unmarshal([]byte(`{"bad":"shape"}`), &got); err == nil {
		t.Error("unmarshal of a JSON object should fail")
	}
}

func TestBalanceAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"code":"InvalidRange","message":"date range exceeds 365 days","requestId":"r-1"}`))
	}))
	defer srv.Close()

	_, err := testClient(srv).BalanceHistory(t.Context(), BalanceHistoryParams{})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if apiErr.Status != http.StatusBadRequest {
		t.Errorf("Status = %d, want 400", apiErr.Status)
	}
	if !strings.Contains(apiErr.Message, "date range exceeds 365 days") {
		t.Errorf("Message = %q, want the API message carried through", apiErr.Message)
	}
	if msg := err.Error(); strings.Contains(msg, testAPIKey) || strings.Contains(msg, testUserKey) {
		t.Errorf("error %q leaks key material", msg)
	}
}

func TestIdentityDecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`not json`))
	}))
	defer srv.Close()

	if _, err := testClient(srv).Identity(t.Context()); err == nil {
		t.Fatal("want decode error on malformed body")
	}
}
