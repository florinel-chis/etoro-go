// Package etoro is a dependency-free Go client for the eToro Public API
// at public-api.etoro.com: market data (instrument search, candles,
// real-time rates, reference data), balances, identity, cash-account
// transactions, and trading (orders, portfolio, positions, history) against
// either the demo or the real account.
//
// Every request carries the three headers the API requires: x-api-key,
// x-user-key, and an x-request-id that the client generates as a fresh
// UUID v4 per call. For order submission the x-request-id doubles as the
// idempotency key and is echoed back by the API as referenceId, so methods
// that place orders surface the id they sent.
//
// Demo and real trading share one host; they differ only in a path segment.
// A Client targets the demo account unless constructed with the Real
// option. Market-data, balances, identity, and cash endpoints are not
// mode-specific.
//
// Credentials are caller-supplied at construction; the package never reads
// environment variables, never logs key material, and sends it only in
// request headers — never in URLs or error messages.
package etoro
