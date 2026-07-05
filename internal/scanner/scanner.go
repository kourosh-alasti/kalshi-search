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
	"github.com/kourosh/kalshi-search/internal/sms"
	"github.com/kourosh/kalshi-search/internal/state"
)

// Scanner runs the periodic market scan.
type Scanner struct {
	cfg     *config.Config
	kalshi  *kalshi.Client
	sms     sms.Client
	store   *state.Store
	scorer  *learning.CounterScorer
	sugLog  *learning.SuggestionLog
	logger  *slog.Logger
	healthy func(bool)

	seriesTags   map[string][]string
	seriesTagsAt time.Time
}

// New wires up a scanner. healthy is called with the success/failure of each
// scan cycle (used by the health endpoint).
func New(cfg *config.Config, kc *kalshi.Client, sms sms.Client, store *state.Store,
	scorer *learning.CounterScorer, sugLog *learning.SuggestionLog, logger *slog.Logger, healthy func(bool)) *Scanner {
	return &Scanner{
		cfg: cfg, kalshi: kc, sms: sms, store: store,
		scorer: scorer, sugLog: sugLog,
		logger: logger.With("component", "scanner"), healthy: healthy,
	}
}

// Run polls until ctx is cancelled. The first cycle runs immediately.
func (s *Scanner) Run(ctx context.Context) {
	s.logger.Info("scanner started", "interval", s.cfg.PollInterval.String(), "users", len(s.cfg.AlertRecipients))
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
	if err := s.refreshSeriesTags(events); err != nil {
		s.logger.Warn("series tag refresh failed", "error", err)
	}

	// Implicit feedback from portfolio positions runs even while paused.
	if err := s.detectPositions(ctx); err != nil {
		s.logger.Warn("position detection failed", "error", err)
	}

	for _, recipient := range s.cfg.AlertRecipients {
		if err := s.cycleForUser(ctx, recipient, events); err != nil {
			return fmt.Errorf("scan for %s: %w", recipient, err)
		}
	}

	s.logger.Info("scan cycle complete",
		"duration_ms", time.Since(start).Milliseconds(),
		"events", len(events), "users", len(s.cfg.AlertRecipients))
	return nil
}

func (s *Scanner) cycleForUser(ctx context.Context, phone string, events []kalshi.Event) error {
	var paused, onboarded bool
	var categories []string
	var subcategories map[string][]string
	if err := s.store.View(ctx, phone, func(d *state.Data) {
		paused = d.Paused
		onboarded = d.Onboarded
		categories = append([]string(nil), d.Categories...)
		subcategories = copySubcategories(d.Subcategories)
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
			if reason := s.disqualify(ctx, phone, ev, m, now, enabled, subcategories); reason != "" {
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
func (s *Scanner) disqualify(ctx context.Context, phone string, ev kalshi.Event, m kalshi.Market, now time.Time, enabled map[string]bool, subcategories map[string][]string) string {
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
	} else if !matchesSubcategories(ev, subcategories, s.seriesTags) {
		return "subcategory"
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

	body := s.alertBody(strings.Join(lines, "\n\n"))
	if err := s.sms.SendSMS(ctx, phone, body); err != nil {
		return fmt.Errorf("sending alert to %s: %w", phone, err)
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

	for _, recipient := range s.cfg.AlertRecipients {
		err := s.store.Update(ctx, recipient, func(d *state.Data) {
			for _, p := range positions {
				if p.Position == 0 || d.KnownPositions[p.Ticker] {
					continue
				}
				d.KnownPositions[p.Ticker] = true
				for _, sug := range d.Suggestions {
					if sug.Ticker == p.Ticker && sug.Label == state.LabelPending {
						sug.Label = state.LabelPosition
						newlyAccepted = append(newlyAccepted, accepted{recipient, sug.ID, sug.Features})
						s.logger.Info("implicit accept via position", "recipient", recipient, "ticker", p.Ticker, "suggestion_id", sug.ID)
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

// onboard sends a short-lived preferences link and marks the user onboarded.
func (s *Scanner) onboard(ctx context.Context, phone string, events []kalshi.Event) error {
	menu := CategoriesFromEvents(events)
	if len(menu) == 0 {
		return fmt.Errorf("no categories found in open events")
	}

	tagsByCategory, err := s.kalshi.ListTagsByCategories(ctx)
	if err != nil {
		return fmt.Errorf("fetching subcategories: %w", err)
	}
	menuTags := TagsForCategories(menu, tagsByCategory)

	token, err := s.store.CreateOnboardingToken(ctx, phone, s.cfg.OnboardingTokenTTL)
	if err != nil {
		return err
	}
	link := s.cfg.PublicBaseURL + "/onboard/" + token

	if err := s.store.Update(ctx, phone, func(d *state.Data) {
		d.CategoryMenu = menu
		d.TagsByCategory = menuTags
		d.Categories = nil
		d.Subcategories = map[string][]string{}
		d.Onboarded = true
	}); err != nil {
		return err
	}

	body := s.onboardBody(link)
	if err := s.sms.SendSMS(ctx, phone, body); err != nil {
		_ = s.store.Update(ctx, phone, func(d *state.Data) { d.Onboarded = false })
		return fmt.Errorf("sending onboarding message to %s: %w", phone, err)
	}
	s.logger.Info("onboarding message sent", "recipient", phone, "categories", menu, "link", link)
	return nil
}

func (s *Scanner) alertBody(picks string) string {
	if s.cfg.NotifyChannel == "email" {
		return fmt.Sprintf("Kalshi picks:\n\n%s", picks)
	}
	return fmt.Sprintf("Kalshi picks (reply TOOK <id> / PASS <id>):\n\n%s\n\nReply STOP to unsubscribe.", picks)
}

func (s *Scanner) onboardBody(link string) string {
	if s.cfg.NotifyChannel == "email" {
		return "Welcome to Kalshi Alerts!\n\n" +
			"Choose which categories and subcategories you want alerts for:\n" + link +
			"\n\nThis link expires in " + formatTTL(s.cfg.OnboardingTokenTTL) +
			". Alerts include kalshi.com links you can open on your phone."
	}
	return "Welcome to Kalshi Alerts!\n\n" +
		"Choose your alert categories here (link expires in " + formatTTL(s.cfg.OnboardingTokenTTL) + "):\n" +
		link + "\n\nMsg&data rates may apply. Reply STOP to unsubscribe, HELP for help."
}

func formatTTL(d time.Duration) string {
	hours := int(d / time.Hour)
	if hours >= 24 && hours%24 == 0 {
		days := hours / 24
		if days == 1 {
			return "24 hours"
		}
		return fmt.Sprintf("%d days", days)
	}
	if hours > 0 && d%time.Hour == 0 {
		return fmt.Sprintf("%d hours", hours)
	}
	return d.Round(time.Minute).String()
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

// TagsForCategories maps Kalshi tag lists onto the event categories we show.
func TagsForCategories(categories []string, tagsByCategory map[string][]string) map[string][]string {
	lookup := map[string][]string{}
	for cat, tags := range tagsByCategory {
		lookup[strings.ToLower(cat)] = tags
	}
	out := make(map[string][]string, len(categories))
	for _, cat := range categories {
		if tags, ok := lookup[strings.ToLower(cat)]; ok && len(tags) > 0 {
			out[cat] = append([]string(nil), tags...)
			sort.Strings(out[cat])
		}
	}
	return out
}

func (s *Scanner) refreshSeriesTags(events []kalshi.Event) error {
	if !s.anyUserNeedsSubcategoryTags() {
		return nil
	}

	const cacheTTL = time.Hour
	if time.Since(s.seriesTagsAt) < cacheTTL && len(s.seriesTags) > 0 {
		return nil
	}

	refreshCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	series, err := s.kalshi.ListSeries(refreshCtx)
	if err != nil {
		return err
	}

	needed := map[string]bool{}
	for _, ev := range events {
		if ev.SeriesTicker != "" {
			needed[ev.SeriesTicker] = true
		}
	}

	tags := make(map[string][]string, len(needed))
	for _, ser := range series {
		if !needed[ser.Ticker] {
			continue
		}
		tags[ser.Ticker] = append([]string(nil), ser.Tags...)
	}
	s.seriesTags = tags
	s.seriesTagsAt = time.Now()
	s.logger.Info("series tags refreshed", "series", len(tags))
	return nil
}

func (s *Scanner) anyUserNeedsSubcategoryTags() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for _, recipient := range s.cfg.AlertRecipients {
		var subs map[string][]string
		if err := s.store.View(ctx, recipient, func(d *state.Data) {
			subs = d.Subcategories
		}); err != nil {
			continue
		}
		for _, tags := range subs {
			if len(tags) > 0 {
				return true
			}
		}
	}
	return false
}

func matchesSubcategories(ev kalshi.Event, selected map[string][]string, seriesTags map[string][]string) bool {
	tags, ok := selected[ev.Category]
	if !ok || len(tags) == 0 {
		return true
	}
	series := seriesTags[ev.SeriesTicker]
	for _, want := range tags {
		for _, have := range series {
			if strings.EqualFold(want, have) {
				return true
			}
		}
	}
	return false
}

func copySubcategories(in map[string][]string) map[string][]string {
	if len(in) == 0 {
		return map[string][]string{}
	}
	out := make(map[string][]string, len(in))
	for k, v := range in {
		out[k] = append([]string(nil), v...)
	}
	return out
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
