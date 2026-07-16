// Package backtestsource adapts the etoro-go client to gobacktest's
// source.Source interface.
//
// The eToro candle endpoint has no from/to parameters: it always returns
// the most recent window of at most 1000 bars, regardless of the requested
// sort direction (verified against the live API — asc and desc yield the
// identical window, only ordered differently). The adapter therefore sizes
// the request so the window reaches back to the requested start, fetches in
// ascending order, and drops bars outside [start, end]. When the start
// predates what a maximum-size window can reach, Fetch fails with an error
// naming the earliest available bar rather than silently truncating the
// series.
package backtestsource

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	backtest "github.com/florinel-chis/gobacktest"
	"github.com/florinel-chis/gobacktest/source"

	etoro "github.com/florinel-chis/etoro-go"
)

// maxWindow is the server-side cap on the candle count per request. It
// mirrors the limit enforced by the candle endpoint (and by etoro-go's
// Candles); a request sized at this cap that still does not reach the
// requested start means the history is out of range for the interval.
const maxWindow = 1000

// defaultBuffer is how many extra bars beyond the computed [start, now]
// span are requested, absorbing weekends, holidays, and session gaps so
// the window still reaches back to start.
const defaultBuffer = 10

// intervals maps canonical source intervals to the eToro candle enum and
// the nominal bar duration used to size a request. The API serves nine
// periods; source.M2 and source.Mo1 have no counterpart.
var intervals = map[source.Interval]struct {
	native etoro.Interval
	dur    time.Duration
}{
	source.M1:  {etoro.IntervalOneMinute, time.Minute},
	source.M5:  {etoro.IntervalFiveMinutes, 5 * time.Minute},
	source.M10: {etoro.IntervalTenMinutes, 10 * time.Minute},
	source.M15: {etoro.IntervalFifteenMinutes, 15 * time.Minute},
	source.M30: {etoro.IntervalThirtyMinutes, 30 * time.Minute},
	source.H1:  {etoro.IntervalOneHour, time.Hour},
	source.H4:  {etoro.IntervalFourHours, 4 * time.Hour},
	source.D1:  {etoro.IntervalOneDay, 24 * time.Hour},
	source.W1:  {etoro.IntervalOneWeek, 7 * 24 * time.Hour},
}

// Option configures the source returned by New.
type Option func(*src)

// WithBuffer sets how many extra bars beyond the computed span are
// requested to absorb non-trading gaps. Negative values are ignored.
func WithBuffer(bars int) Option {
	return func(s *src) {
		if bars >= 0 {
			s.buffer = bars
		}
	}
}

type src struct {
	c      *etoro.Client
	buffer int
	now    func() time.Time

	mu  sync.Mutex
	ids map[string]int // symbol (upper-cased) -> instrument id
}

// New returns a source.Source backed by the given eToro client. Symbols
// are resolved to instrument ids on first use and cached for the lifetime
// of the source; instrument ids are immutable on the platform, so the
// cache never goes stale.
func New(c *etoro.Client, opts ...Option) source.Source {
	s := &src{c: c, buffer: defaultBuffer, now: time.Now, ids: make(map[string]int)}
	for _, o := range opts {
		o(s)
	}
	return s
}

// instrumentID resolves symbol to its instrument id, consulting the cache
// first. Resolution is exact and case-insensitive (see
// etoro.ResolveInstrument), so the cache key is the upper-cased symbol.
func (s *src) instrumentID(ctx context.Context, symbol string) (int, error) {
	key := strings.ToUpper(symbol)
	s.mu.Lock()
	id, ok := s.ids[key]
	s.mu.Unlock()
	if ok {
		return id, nil
	}
	inst, err := s.c.ResolveInstrument(ctx, symbol)
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	s.ids[key] = inst.InstrumentID
	s.mu.Unlock()
	return inst.InstrumentID, nil
}

func (s *src) Fetch(ctx context.Context, symbol string, start, end time.Time, interval source.Interval) (*backtest.Data, error) {
	iv, ok := intervals[interval]
	if !ok {
		return nil, fmt.Errorf("etoro: %w: %q", source.ErrUnsupportedInterval, interval)
	}
	id, err := s.instrumentID(ctx, symbol)
	if err != nil {
		return nil, err
	}

	// The candle window always ends at the most recent bar, so the request
	// must span from the requested start to now — not merely start to end —
	// or a fully historical window would come back empty.
	span := s.now().Sub(start)
	if span < 0 {
		span = 0
	}
	count := int(span/iv.dur) + 1 + s.buffer
	capped := count >= maxWindow
	if capped {
		count = maxWindow
	}

	candles, err := s.c.Candles(ctx, id, iv.native, count)
	if err != nil {
		return nil, fmt.Errorf("etoro: fetch candles for %q: %w", symbol, err)
	}

	// A full-size window that still does not reach start means deeper
	// history exists but is unreachable through this endpoint — fail loudly.
	// A short delivery (fewer bars than requested) means the instrument's
	// entire history is present (e.g. it listed after start); that matches
	// the other sources' semantics, so the series is returned from its
	// first bar.
	earliest := candles[0].FromDate
	if capped && len(candles) >= maxWindow && start.Before(earliest) {
		return nil, fmt.Errorf(
			"etoro: %q %s history reaches back to %s only (endpoint serves at most %d bars ending at the most recent one); requested start %s is out of range — use a coarser interval for deeper history",
			symbol, interval, earliest.Format(time.RFC3339), maxWindow, start.Format(time.RFC3339))
	}

	// Bars are kept on the family-wide [start, end) contract. The newest
	// bar has no completeness flag on this API and is always the live,
	// still-forming session, so any bar whose period has not elapsed yet
	// is dropped rather than fed to a backtest as final OHLCV.
	now := s.now()
	bars := make([]backtest.Bar, 0, len(candles))
	for _, cd := range candles {
		if cd.FromDate.Before(start) || !cd.FromDate.Before(end) {
			continue
		}
		if cd.FromDate.Add(iv.dur).After(now) {
			continue
		}
		bars = append(bars, backtest.Bar{Time: cd.FromDate, Open: cd.Open, High: cd.High,
			Low: cd.Low, Close: cd.Close, Volume: cd.Volume})
	}
	return backtest.FromBars(bars), nil
}
