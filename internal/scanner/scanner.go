// Package scanner implements the polling loop that finds qualifying Kalshi
// markets, ranks them with the preference model, and sends SMS alerts.
package scanner

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/kourosh/kalshi-search/internal/config"
	"github.com/kourosh/kalshi-search/internal/kalshi"
	"github.com/kourosh/kalshi-search/internal/learning"
	"github.com/kourosh/kalshi-search/internal/state"
	"github.com/kourosh/kalshi-search/internal/telnyx"
)

// Scanner runs the periodic market scan.
type Scanner struct {
	cfg     *config.Config
	kalshi  *kalshi.Client
	sms     *telnyx.Client
	store   *state.Store
	scorer  *learning.CounterScorer
	sugLog  *learning.SuggestionLog
	logger  *slog.Logger
	healthy func(bool)
}

// New wires up a scanner. healthy is called with the success/failure of each
// scan cycle (used by the health endpoint).
func New(cfg *config.Config, kc *kalshi.Client, sms *telnyx.Client, store *state.Store,
	scorer *learning.CounterScorer, sugLog *learning.SuggestionLog, logger *slog.Logger, healthy func(bool)) *Scanner {
	return &Scanner{
		cfg: cfg, kalshi: kc, sms: sms, store: store,
		scorer: scorer, sugLog: sugLog,
		logger: logger.With("component", "scanner"), healthy: healthy,
	}
}

// Run polls until ctx is cancelled. The first cycle runs immediately.
func (s *Scanner) Run(ctx context.Context) {
	s.logger.Info("scanner started", "interval", s.cfg.PollInterval.String(), "users", len(s.cfg.AlertPhoneNumbers))
	ticker := time.NewTicker(s.cfg.PollInterval)
	defer ticker.Stop()

	for {
		if err := s.cycle(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			s.logger.Error("scan cycle failed", "error", err)
			s.healthy(false)
		} else {
			s.healthy(true)
		}

		select {
		case <-ticker.C:
		case <-ctx.Done():
			s.logger.Info("scanner stopping")
			return
		}
	}
}

type candidate struct {
	event    kalshi.Event
	market   kalshi.Market
	features []string
	score    float64
}

func (s *Scanner) cycle(ctx context.Context) error {
	start := time.Now()

	events, err := s.kalshi.ListOpenEvents(ctx)
	if err != nil {
		return fmt.Errorf("fetching events: %w", err)
	}

	// Implicit feedback from portfolio positions runs even while paused.
	if err := s.detectPositions(ctx); err != nil {
		s.logger.Warn("position detection failed", "error", err)
	}

	for _, phone := range s.cfg.AlertPhoneNumbers {
		if err := s.cycleForUser(ctx, phone, events); err != nil {
			return fmt.Errorf("scan for %s: %w", phone, err)
		}
	}

	s.logger.Info("scan cycle complete",
		"duration_ms", time.Since(start).Milliseconds(),
		"events", len(events), "users", len(s.cfg.AlertPhoneNumbers))
	return nil
}

func (s *Scanner) cycleForUser(ctx context.Context, phone string, events []kalshi.Event) error {
	var paused, onboarded bool
	var categories []string
	if err := s.store.View(ctx, phone, func(d *state.Data) {
		paused = d.Paused
		onboarded = d.Onboarded
		categories = append([]string(nil), d.Categories...)
	}); err != nil {
		return err
	}

	if !onboarded {
		return s.onboard(ctx, phone, events)
	}
	if paused {
		s.logger.Info("scan skipped: alerts paused", "phone", phone, "events_fetched", len(events))
		return nil
	}
	if len(categories) == 0 {
		s.logger.Info("scan skipped: no categories enabled yet", "phone", phone)
		return nil
	}

	now := time.Now()
	enabled := map[string]bool{}
	for _, c := range categories {
		enabled[strings.ToLower(c)] = true
	}

	var candidates []candidate
	counts := map[string]int{}
	totalMarkets := 0

	for _, ev := range events {
		for _, m := range ev.Markets {
			totalMarkets++
			if reason := s.disqualify(ctx, phone, ev, m, now, enabled); reason != "" {
				counts[reason]++
				s.logger.Debug("market filtered", "phone", phone, "ticker", m.Ticker, "reason", reason,
					"yes_ask", m.YesAsk, "volume", m.Volume, "open_interest", m.OpenInterest)
				continue
			}
			feats := learning.ExtractFeatures(ev, m, now)
			candidates = append(candidates, candidate{
				event: ev, market: m, features: feats, score: s.scorer.Score(ctx, phone, feats),
			})
		}
	}

	sort.Slice(candidates, func(i, j int) bool { return candidates[i].score > candidates[j].score })

	if s.cfg.MinSuggestionScore > 0 {
		kept := candidates[:0]
		for _, c := range candidates {
			if c.score >= s.cfg.MinSuggestionScore {
				kept = append(kept, c)
			} else {
				counts["below_min_score"]++
			}
		}
		candidates = kept
	}

	if len(candidates) > s.cfg.MaxAlertsPerMessage {
		counts["over_digest_limit"] += len(candidates) - s.cfg.MaxAlertsPerMessage
		candidates = candidates[:s.cfg.MaxAlertsPerMessage]
	}

	s.logger.Info("user scan complete",
		"phone", phone, "events", len(events), "markets", totalMarkets,
		"matches", len(candidates), "filtered", counts)

	if len(candidates) == 0 {
		return nil
	}
	return s.alert(ctx, phone, candidates)
}

// disqualify returns a non-empty reason when the market fails a filter.
func (s *Scanner) disqualify(ctx context.Context, phone string, ev kalshi.Event, m kalshi.Market, now time.Time, enabled map[string]bool) string {
	if m.Status != "" && m.Status != "active" && m.Status != "open" {
		return "not_active"
	}
	if m.YesAsk <= 0 || m.YesAsk > s.cfg.MaxPriceCents {
		return "price"
	}
	if m.Volume < s.cfg.MinVolume {
		return "volume"
	}
	if m.OpenInterest < s.cfg.MinOpenInterest {
		return "open_interest"
	}
	closeIn := m.CloseTime.Sub(now)
	if closeIn <= 0 || closeIn > time.Duration(s.cfg.CloseWithinHours)*time.Hour {
		return "close_time"
	}
	if !enabled[strings.ToLower(ev.Category)] {
		if !(s.cfg.LearnExpandCategories && s.scorer.SeriesBoost(ctx, phone, ev.SeriesTicker)) {
			return "category"
		}
	}

	skip := ""
	_ = s.store.View(ctx, phone, func(d *state.Data) {
		prev, ok := d.Alerted[m.Ticker]
		if !ok {
			return
		}
		if prev.Suppressed {
			skip = "passed"
			return
		}
		if m.YesAsk > prev.LastPriceCents-s.cfg.RealertDropCents {
			skip = "already_alerted"
		}
	})
	return skip
}

// alert sends one digest SMS for the given candidates and records state.
func (s *Scanner) alert(ctx context.Context, phone string, candidates []candidate) error {
	now := time.Now()
	var lines []string
	var newSuggestions []*state.Suggestion
	var records []learning.SuggestionRecord

	err := s.store.Update(ctx, phone, func(d *state.Data) {
		for _, c := range candidates {
			id := d.NextSuggestionID
			d.NextSuggestionID++

			sug := &state.Suggestion{
				ID: id, Ticker: c.market.Ticker, Title: marketLabel(c.event, c.market),
				Features: c.features, PriceCents: c.market.YesAsk,
				SentAt: now, Label: state.LabelPending,
			}
			d.Suggestions[fmt.Sprint(id)] = sug
			d.Alerted[c.market.Ticker] = &state.AlertedMarket{
				Ticker: c.market.Ticker, LastPriceCents: c.market.YesAsk,
				LastAlertedAt: now, SuggestionID: id,
			}
			newSuggestions = append(newSuggestions, sug)

			lines = append(lines, fmt.Sprintf("#%d %s\nYES %d¢ (%.1fx) closes %s\n%s",
				id, sug.Title, c.market.YesAsk, 100/float64(c.market.YesAsk),
				c.market.CloseTime.Local().Format("Jan 2 3:04PM"),
				marketURL(c.event)))

			records = append(records, learning.SuggestionRecord{
				Kind: "suggestion", Timestamp: now, SuggestionID: id,
				Ticker: c.market.Ticker, Title: sug.Title,
				Category: c.event.Category, SeriesTicker: c.event.SeriesTicker,
				YesAsk: c.market.YesAsk, YesBid: c.market.YesBid,
				Volume: c.market.Volume, OpenInterest: c.market.OpenInterest,
				CloseTime: c.market.CloseTime, Features: c.features, Score: c.score,
			})
		}
	})
	if err != nil {
		return fmt.Errorf("persisting suggestions: %w", err)
	}
	for _, rec := range records {
		s.sugLog.Append(ctx, phone, rec)
	}

	body := fmt.Sprintf("Kalshi picks (reply TOOK <id> / PASS <id>):\n\n%s\n\nReply STOP to unsubscribe.", strings.Join(lines, "\n\n"))
	if err := s.sms.SendSMS(ctx, phone, body); err != nil {
		return fmt.Errorf("sending alert sms to %s: %w", phone, err)
	}

	for _, sug := range newSuggestions {
		s.logger.Info("alert sent", "phone", phone, "suggestion_id", sug.ID, "ticker", sug.Ticker,
			"price_cents", sug.PriceCents, "title", sug.Title)
	}
	return nil
}

// detectPositions polls portfolio positions and records implicit "accepted"
// feedback for suggestions whose market now has a position.
func (s *Scanner) detectPositions(ctx context.Context) error {
	positions, err := s.kalshi.ListPositions(ctx)
	if err != nil {
		return err
	}

	type accepted struct {
		phone    string
		id       int
		features []string
	}
	var newlyAccepted []accepted

	for _, phone := range s.cfg.AlertPhoneNumbers {
		err := s.store.Update(ctx, phone, func(d *state.Data) {
			for _, p := range positions {
				if p.Position == 0 || d.KnownPositions[p.Ticker] {
					continue
				}
				d.KnownPositions[p.Ticker] = true
				for _, sug := range d.Suggestions {
					if sug.Ticker == p.Ticker && sug.Label == state.LabelPending {
						sug.Label = state.LabelPosition
						newlyAccepted = append(newlyAccepted, accepted{phone, sug.ID, sug.Features})
						s.logger.Info("implicit accept via position", "phone", phone, "ticker", p.Ticker, "suggestion_id", sug.ID)
					}
				}
			}
		})
		if err != nil {
			return err
		}
	}

	for _, a := range newlyAccepted {
		s.scorer.Feed(ctx, a.phone, a.features, true)
		s.sugLog.Append(ctx, a.phone, learning.SuggestionRecord{
			Kind: "label", Timestamp: time.Now(), SuggestionID: a.id, Label: string(state.LabelPosition),
		})
	}
	return nil
}

// onboard texts the initial category menu and marks the user onboarded.
func (s *Scanner) onboard(ctx context.Context, phone string, events []kalshi.Event) error {
	menu := CategoriesFromEvents(events)
	if len(menu) == 0 {
		return fmt.Errorf("no categories found in open events")
	}

	if err := s.store.Update(ctx, phone, func(d *state.Data) {
		d.CategoryMenu = menu
		d.Onboarded = true
	}); err != nil {
		return err
	}

	body := "Welcome to Kalshi Alerts! Reply with numbers to enable categories (e.g. 1,3):\n" +
		FormatCategoryMenu(menu) + "\nOr reply ALL. Other commands: LIST, STATUS, PAUSE, RESUME. Msg&data rates may apply. Reply STOP to unsubscribe, HELP for help."
	if err := s.sms.SendSMS(ctx, phone, body); err != nil {
		_ = s.store.Update(ctx, phone, func(d *state.Data) { d.Onboarded = false })
		return fmt.Errorf("sending onboarding sms to %s: %w", phone, err)
	}
	s.logger.Info("onboarding sms sent", "phone", phone, "categories", menu)
	return nil
}

// CategoriesFromEvents returns the sorted distinct categories present in events.
func CategoriesFromEvents(events []kalshi.Event) []string {
	seen := map[string]bool{}
	var menu []string
	for _, ev := range events {
		if ev.Category == "" || seen[ev.Category] {
			continue
		}
		seen[ev.Category] = true
		menu = append(menu, ev.Category)
	}
	sort.Strings(menu)
	return menu
}

// FormatCategoryMenu renders the numbered category list for SMS.
func FormatCategoryMenu(menu []string) string {
	var b strings.Builder
	for i, c := range menu {
		fmt.Fprintf(&b, "%d. %s\n", i+1, c)
	}
	return b.String()
}

func marketLabel(ev kalshi.Event, m kalshi.Market) string {
	label := m.Title
	if label == "" {
		label = ev.Title
	}
	if m.YesSubTitle != "" && m.YesSubTitle != label {
		label += " — " + m.YesSubTitle
	}
	return label
}

// marketURL builds a kalshi.com link that opens the market in the app.
func marketURL(ev kalshi.Event) string {
	return fmt.Sprintf("https://kalshi.com/markets/%s", strings.ToLower(ev.SeriesTicker))
}
