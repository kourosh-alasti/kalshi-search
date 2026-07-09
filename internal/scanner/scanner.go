// Package scanner implements the polling loop that finds qualifying Kalshi
// markets, ranks them with the preference model, and sends alerts.
package scanner

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/kourosh/kalshi-search/internal/commands"
	"github.com/kourosh/kalshi-search/internal/config"
	"github.com/kourosh/kalshi-search/internal/inbound"
	"github.com/kourosh/kalshi-search/internal/kalshi"
	"github.com/kourosh/kalshi-search/internal/learning"
	"github.com/kourosh/kalshi-search/internal/metrics"
	"github.com/kourosh/kalshi-search/internal/scanstats"
	"github.com/kourosh/kalshi-search/internal/sign"
	"github.com/kourosh/kalshi-search/internal/sms"
	"github.com/kourosh/kalshi-search/internal/state"
)

// Scanner runs the periodic market scan.
type Scanner struct {
	cfg      *config.Config
	kalshi   *kalshi.Client
	sms      sms.Client
	store    *state.Store
	scorer   *learning.CounterScorer
	sugLog   *learning.SuggestionLog
	inbound  *inbound.Queue
	cmds     *commands.Handler
	signer   *sign.Signer
	stats    *scanstats.Tracker
	logger   *slog.Logger
	healthy  func(bool)
	isLeader func() bool

	seriesTags   map[string][]string
	seriesTagsAt time.Time

	eventsCache   []kalshi.Event
	eventsCacheAt time.Time
}

// New wires up a scanner.
func New(cfg *config.Config, kc *kalshi.Client, smsClient sms.Client, store *state.Store,
	scorer *learning.CounterScorer, sugLog *learning.SuggestionLog, inboundQ *inbound.Queue,
	cmds *commands.Handler, signer *sign.Signer, stats *scanstats.Tracker,
	logger *slog.Logger, healthy func(bool), isLeader func() bool) *Scanner {
	return &Scanner{
		cfg: cfg, kalshi: kc, sms: smsClient, store: store,
		scorer: scorer, sugLog: sugLog, inbound: inboundQ, cmds: cmds,
		signer: signer, stats: stats,
		logger: logger.With("component", "scanner"), healthy: healthy, isLeader: isLeader,
	}
}

// Run polls until ctx is cancelled.
func (s *Scanner) Run(ctx context.Context) {
	s.logger.Info("scanner started", "interval", s.cfg.PollInterval.String(), "users", len(s.cfg.AlertRecipients))
	ticker := time.NewTicker(s.cfg.PollInterval)
	defer ticker.Stop()

	for {
		if !s.isLeader() {
			s.logger.Debug("not leader, skipping scan cycle")
			select {
			case <-ticker.C:
			case <-ctx.Done():
				return
			}
			continue
		}

		cycleCtx, cancel := context.WithTimeout(ctx, s.cfg.ScanCycleTimeout)
		timedOut := false
		err := s.cycle(cycleCtx)
		if cycleCtx.Err() == context.DeadlineExceeded {
			timedOut = true
			err = fmt.Errorf("scan cycle timed out after %s", s.cfg.ScanCycleTimeout)
		}
		cancel()

		if err != nil {
			if ctx.Err() != nil {
				return
			}
			s.logger.Error("scan cycle failed", "error", err, "timed_out", timedOut)
			s.healthy(false)
			metrics.ObserveScan(s.cfg.ScanCycleTimeout, 0, false)
			if s.stats != nil && timedOut {
				s.stats.RecordCycle(0, 0, 0, s.cfg.ScanCycleTimeout, false, err.Error(), true)
			}
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

type userFilters struct {
	maxPrice       int
	minVolume      int
	minOpenInterest int
	closeWithin    time.Duration
}

func (s *Scanner) filtersForUser(overrides state.FilterOverrides) userFilters {
	f := userFilters{
		maxPrice:       s.cfg.MaxPriceCents,
		minVolume:      s.cfg.MinVolume,
		minOpenInterest: s.cfg.MinOpenInterest,
		closeWithin:    time.Duration(s.cfg.CloseWithinHours) * time.Hour,
	}
	if overrides.MaxPriceCents != nil {
		f.maxPrice = *overrides.MaxPriceCents
	}
	if overrides.MinVolume != nil {
		f.minVolume = *overrides.MinVolume
	}
	if overrides.MinOpenInterest != nil {
		f.minOpenInterest = *overrides.MinOpenInterest
	}
	if overrides.CloseWithinHours != nil {
		f.closeWithin = time.Duration(*overrides.CloseWithinHours) * time.Hour
	}
	return f
}

type candidate struct {
	event    kalshi.Event
	market   kalshi.Market
	features []string
	score    float64
	explain  string
	watch    bool
}

func (s *Scanner) cycle(ctx context.Context) error {
	start := time.Now()

	if err := s.processInbound(ctx); err != nil {
		s.logger.Warn("inbound processing failed", "error", err)
	}

	events, err := s.fetchEvents(ctx)
	if err != nil {
		return fmt.Errorf("fetching events: %w", err)
	}
	if err := s.refreshSeriesTags(ctx, events); err != nil {
		s.logger.Warn("series tag refresh failed", "error", err)
	}

	if err := s.detectPositions(ctx); err != nil {
		s.logger.Warn("position detection failed", "error", err)
	}

	alertsSent := 0
	for _, recipient := range s.cfg.AlertRecipients {
		n, err := s.cycleForUser(ctx, recipient, events)
		if err != nil {
			return fmt.Errorf("scan for %s: %w", recipient, err)
		}
		alertsSent += n
	}

	dur := time.Since(start)
	s.logger.Info("scan cycle complete",
		"duration_ms", dur.Milliseconds(),
		"events", len(events), "users", len(s.cfg.AlertRecipients), "alerts_sent", alertsSent)
	metrics.ObserveScan(dur, len(events), true)

	if s.stats != nil {
		s.stats.RecordCycle(len(events), len(s.cfg.AlertRecipients), alertsSent, dur, true, "", false)
		if s.inbound != nil {
			if pending, err := s.inbound.PendingCount(ctx); err == nil {
				s.stats.SetInboundPending(pending)
			}
		}
	}
	return nil
}

func (s *Scanner) processInbound(ctx context.Context) error {
	if s.inbound == nil || s.cmds == nil {
		return nil
	}
	msgs, err := s.inbound.Pending(ctx, 50)
	if err != nil {
		return err
	}
	for _, m := range msgs {
		if err := s.cmds.Handle(ctx, m.Phone, m.Body); err != nil {
			s.logger.Warn("inbound command failed", "id", m.ID, "error", err)
			if markErr := s.inbound.MarkFailed(ctx, m.ID, err.Error()); markErr != nil {
				s.logger.Warn("marking inbound failed", "id", m.ID, "error", markErr)
			}
			metrics.InboundProcessed.WithLabelValues("error").Inc()
			continue
		}
		if err := s.inbound.MarkDone(ctx, m.ID); err != nil {
			s.logger.Warn("marking inbound done failed", "id", m.ID, "error", err)
			metrics.InboundProcessed.WithLabelValues("error").Inc()
		} else {
			metrics.InboundProcessed.WithLabelValues("ok").Inc()
		}
	}
	if s.stats != nil && s.inbound != nil {
		if n, err := s.inbound.PendingCount(ctx); err == nil {
			s.stats.SetInboundPending(n)
		}
	}
	return nil
}

func (s *Scanner) fetchEvents(ctx context.Context) ([]kalshi.Event, error) {
	if s.cfg.EventsCacheTTL > 0 && len(s.eventsCache) > 0 &&
		time.Since(s.eventsCacheAt) < s.cfg.EventsCacheTTL {
		return s.eventsCache, nil
	}
	events, err := s.kalshi.ListOpenEvents(ctx)
	if err != nil {
		return nil, err
	}
	s.eventsCache = events
	s.eventsCacheAt = time.Now()
	return events, nil
}

func (s *Scanner) cycleForUser(ctx context.Context, phone string, events []kalshi.Event) (int, error) {
	var paused, onboarded bool
	var categories []string
	var subcategories map[string][]string
	var overrides state.FilterOverrides
	var quiet state.QuietHours
	var alerted map[string]*state.AlertedMarket
	var watchlist map[string]*state.WatchItem
	var knownPositions map[string]bool

	if err := s.store.View(ctx, phone, func(d *state.Data) {
		paused = d.Paused
		onboarded = d.Onboarded
		categories = append([]string(nil), d.Categories...)
		subcategories = copySubcategories(d.Subcategories)
		overrides = d.FilterOverrides
		quiet = d.QuietHours
		alerted = copyAlerted(d.Alerted)
		watchlist = copyWatchlist(d.Watchlist)
		knownPositions = copyBoolMap(d.KnownPositions)
	}); err != nil {
		return 0, err
	}

	if !onboarded {
		err := s.onboard(ctx, phone, events)
		return 0, err
	}
	if paused {
		s.logger.Info("scan skipped: alerts paused", "phone", phone)
		return 0, nil
	}
	if len(categories) == 0 && len(watchlist) == 0 {
		s.logger.Info("scan skipped: no categories or watchlist", "phone", phone)
		return 0, nil
	}

	inQuiet := state.InQuietHours(quiet, time.Now())
	filters := s.filtersForUser(overrides)
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
			watchItem := watchlist[strings.ToUpper(m.Ticker)]
			isWatch := watchItem != nil

			if reason := s.disqualify(ctx, phone, m, ev, now, enabled, subcategories, filters, alerted, knownPositions, isWatch, watchItem); reason != "" {
				counts[reason]++
				learning.RecordFilterReason(reason)
				s.logger.Debug("market filtered", "phone", phone, "ticker", m.Ticker, "reason", reason)
				continue
			}

			prevPrice := 0
			if am, ok := alerted[m.Ticker]; ok {
				prevPrice = am.LastPriceCents
			}
			featCtx := learning.FeatureContext{
				PrevPriceCents: prevPrice,
				HasPosition:    knownPositions[m.Ticker],
			}
			feats := learning.ExtractFeatures(ev, m, now, featCtx)
			score, explain := s.scorer.ScoreAndExplain(ctx, phone, feats)
			candidates = append(candidates, candidate{
				event: ev, market: m, features: feats, score: score,
				explain: explain, watch: isWatch,
			})
		}
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].watch != candidates[j].watch {
			return candidates[i].watch
		}
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		if candidates[i].market.YesAsk != candidates[j].market.YesAsk {
			return candidates[i].market.YesAsk < candidates[j].market.YesAsk
		}
		return candidates[i].market.Volume > candidates[j].market.Volume
	})

	if s.cfg.MinSuggestionScore > 0 {
		kept := candidates[:0]
		for _, c := range candidates {
			if c.watch || c.score >= s.cfg.MinSuggestionScore {
				kept = append(kept, c)
			} else {
				counts["below_min_score"]++
			}
		}
		candidates = kept
	}

	candidates = applySeriesDiversity(candidates, s.cfg.MaxPerSeriesDigest)

	if len(candidates) > s.cfg.MaxAlertsPerMessage {
		counts["over_digest_limit"] += len(candidates) - s.cfg.MaxAlertsPerMessage
		candidates = candidates[:s.cfg.MaxAlertsPerMessage]
	}

	s.logger.Info("user scan complete",
		"phone", phone, "events", len(events), "markets", totalMarkets,
		"matches", len(candidates), "filtered", counts, "quiet_hours", inQuiet)

	if len(candidates) == 0 {
		return 0, nil
	}

	// During quiet hours, only send watchlist matches immediately.
	if inQuiet {
		var urgent []candidate
		for _, c := range candidates {
			if c.watch {
				urgent = append(urgent, c)
			}
		}
		if len(urgent) == 0 {
			s.logger.Info("alerts deferred: quiet hours", "phone", phone, "deferred", len(candidates))
			return 0, nil
		}
		candidates = urgent
	}

	if err := s.alert(ctx, phone, candidates); err != nil {
		return 0, err
	}
	metrics.AlertsSent.WithLabelValues(s.cfg.NotifyChannel).Inc()
	return 1, nil
}

func applySeriesDiversity(candidates []candidate, maxPerSeries int) []candidate {
	if maxPerSeries <= 0 {
		maxPerSeries = 2
	}
	seriesCount := map[string]int{}
	var out []candidate
	for _, c := range candidates {
		series := c.event.SeriesTicker
		if c.watch {
			out = append(out, c)
			continue
		}
		if seriesCount[series] >= maxPerSeries {
			continue
		}
		seriesCount[series]++
		out = append(out, c)
	}
	return out
}

func (s *Scanner) disqualify(ctx context.Context, phone string, m kalshi.Market, ev kalshi.Event, now time.Time,
	enabled map[string]bool, subcategories map[string][]string, filters userFilters,
	alerted map[string]*state.AlertedMarket, knownPositions map[string]bool,
	isWatch bool, watchItem *state.WatchItem) string {

	if isWatch {
		if watchItem.MaxPriceCents > 0 && m.YesAsk > watchItem.MaxPriceCents {
			return "watch_price"
		}
		if m.YesAsk <= 0 || m.YesAsk > s.cfg.MaxPriceCents {
			return "watch_price"
		}
		return ""
	}

	if knownPositions[m.Ticker] {
		return "has_position"
	}
	if m.Status != "" && m.Status != "active" && m.Status != "open" {
		return "not_active"
	}
	if m.YesAsk <= 0 || m.YesAsk > filters.maxPrice {
		return "price"
	}
	if m.Volume < filters.minVolume {
		return "volume"
	}
	if m.OpenInterest < filters.minOpenInterest {
		return "open_interest"
	}
	closeIn := m.CloseTime.Sub(now)
	if closeIn <= 0 || closeIn > filters.closeWithin {
		return "close_time"
	}
	if !enabled[strings.ToLower(ev.Category)] {
		if !(s.cfg.LearnExpandCategories && s.scorer.SeriesBoost(ctx, phone, ev.SeriesTicker)) {
			return "category"
		}
	} else if !matchesSubcategories(ev, subcategories, s.seriesTags) {
		return "subcategory"
	}

	prev, ok := alerted[m.Ticker]
	if !ok {
		return ""
	}
	if prev.Suppressed {
		if !prev.SuppressedUntil.IsZero() && time.Now().After(prev.SuppressedUntil) {
			return ""
		}
		return "passed"
	}
	if m.YesAsk <= prev.LastPriceCents-s.cfg.RealertDropCents {
		return ""
	}
	closeHours := closeIn.Hours()
	if closeHours <= 6 && prev.LastCloseHours > 6 {
		return ""
	}
	if prev.LastVolume > 0 && m.Volume >= prev.LastVolume*2 {
		return ""
	}
	return "already_alerted"
}

func (s *Scanner) alert(ctx context.Context, phone string, candidates []candidate) error {
	now := time.Now()
	var lines []string
	type pendingSug struct {
		sug        *state.Suggestion
		record     learning.SuggestionRecord
		closeHours float64
		volume     int
	}
	var pending []pendingSug

	for _, c := range candidates {
		var id int
		var sug *state.Suggestion
		err := s.store.Update(ctx, phone, func(d *state.Data) {
			id = d.NextSuggestionID
			d.NextSuggestionID++
			sug = &state.Suggestion{
				ID: id, Ticker: c.market.Ticker, Title: marketLabel(c.event, c.market),
				Features: c.features, PriceCents: c.market.YesAsk,
				SentAt: now, Label: state.LabelPending, Score: c.score, Explain: c.explain,
			}
			d.Suggestions[fmt.Sprint(id)] = sug
		})
		if err != nil {
			return fmt.Errorf("allocating suggestion id: %w", err)
		}

		line := s.formatAlertLine(phone, id, c)
		lines = append(lines, line)
		pending = append(pending, pendingSug{
			sug: sug,
			record: learning.SuggestionRecord{
				Kind: "suggestion", Timestamp: now, SuggestionID: id,
				Ticker: c.market.Ticker, Title: sug.Title,
				Category: c.event.Category, SeriesTicker: c.event.SeriesTicker,
				YesAsk: c.market.YesAsk, YesBid: c.market.YesBid,
				Volume: c.market.Volume, OpenInterest: c.market.OpenInterest,
				CloseTime: c.market.CloseTime, Features: c.features, Score: c.score,
			},
			closeHours: c.market.CloseTime.Sub(now).Hours(),
			volume:     c.market.Volume,
		})
	}

	body := s.alertBody(phone, strings.Join(lines, "\n\n"))
	if err := s.sms.SendSMS(ctx, phone, body); err != nil {
		ids := make([]int, len(pending))
		for i, p := range pending {
			ids[i] = p.sug.ID
		}
		_ = s.store.Update(ctx, phone, func(d *state.Data) {
			for _, id := range ids {
				delete(d.Suggestions, fmt.Sprint(id))
			}
			if len(ids) > 0 && d.NextSuggestionID > ids[0] {
				d.NextSuggestionID = ids[0]
			}
		})
		return fmt.Errorf("sending alert to %s: %w", phone, err)
	}

	for _, p := range pending {
		_ = s.store.Update(ctx, phone, func(d *state.Data) {
			d.Alerted[p.sug.Ticker] = &state.AlertedMarket{
				Ticker: p.sug.Ticker, LastPriceCents: p.sug.PriceCents,
				LastAlertedAt: now, SuggestionID: p.sug.ID,
				LastVolume: p.volume, LastCloseHours: p.closeHours,
			}
		})
		s.sugLog.Append(ctx, phone, p.record)
		s.logger.Info("alert sent", "phone", phone, "suggestion_id", p.sug.ID, "ticker", p.sug.Ticker)
	}
	return nil
}

func (s *Scanner) formatAlertLine(phone string, id int, c candidate) string {
	base := fmt.Sprintf("#%d %s\nYES %d¢ (%.1fx) closes %s\n%s",
		id, marketLabel(c.event, c.market), c.market.YesAsk, 100/float64(c.market.YesAsk),
		c.market.CloseTime.Local().Format("Jan 2 3:04PM"),
		marketURL(c.event, c.market))
	if c.explain != "" {
		base += "\n" + c.explain
	}
	if s.cfg.NotifyChannel == "email" && s.signer != nil {
		took := s.signer.FeedbackURL(s.cfg.PublicBaseURL, phone, id, "took", s.cfg.LinkTTL)
		pass := s.signer.FeedbackURL(s.cfg.PublicBaseURL, phone, id, "pass", s.cfg.LinkTTL)
		base += fmt.Sprintf("\nTook: %s\nPass: %s", took, pass)
	}
	return base
}

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
	s.logger.Info("onboarding message sent", "recipient", phone, "link", link)
	return nil
}

func (s *Scanner) alertBody(phone, picks string) string {
	if s.cfg.NotifyChannel == "email" {
		footer := ""
		if s.signer != nil {
			enable := s.signer.ToggleURL(s.cfg.PublicBaseURL, phone, "enable", s.cfg.LinkTTL)
			disable := s.signer.ToggleURL(s.cfg.PublicBaseURL, phone, "disable", s.cfg.LinkTTL)
			footer = fmt.Sprintf("\n\nQuiet hours: Enable %s | Disable %s", enable, disable)
		}
		return fmt.Sprintf("Kalshi picks:\n\n%s%s", picks, footer)
	}
	return fmt.Sprintf("Kalshi picks (reply TOOK <id> / PASS <id>):\n\n%s\n\nReply STOP to unsubscribe.", picks)
}

func (s *Scanner) onboardBody(link string) string {
	if s.cfg.NotifyChannel == "email" {
		return "Welcome to Kalshi Alerts!\n\nChoose categories:\n" + link +
			"\n\nLink expires in " + formatTTL(s.cfg.OnboardingTokenTTL) + "."
	}
	return "Welcome to Kalshi Alerts!\n\nChoose categories (expires " + formatTTL(s.cfg.OnboardingTokenTTL) + "):\n" +
		link + "\n\nReply STOP to unsubscribe."
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

func FormatCategoryMenu(menu []string) string {
	var b strings.Builder
	for i, c := range menu {
		fmt.Fprintf(&b, "%d. %s\n", i+1, c)
	}
	return b.String()
}

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

func (s *Scanner) refreshSeriesTags(ctx context.Context, events []kalshi.Event) error {
	if !s.anyUserNeedsSubcategoryTags(ctx) {
		return nil
	}
	const cacheTTL = time.Hour
	if time.Since(s.seriesTagsAt) < cacheTTL && len(s.seriesTags) > 0 {
		return nil
	}
	refreshCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
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
	return nil
}

func (s *Scanner) anyUserNeedsSubcategoryTags(ctx context.Context) bool {
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for _, recipient := range s.cfg.AlertRecipients {
		var subs map[string][]string
		if err := s.store.View(checkCtx, recipient, func(d *state.Data) { subs = d.Subcategories }); err != nil {
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
	out := make(map[string][]string, len(in))
	for k, v := range in {
		out[k] = append([]string(nil), v...)
	}
	return out
}

func copyAlerted(in map[string]*state.AlertedMarket) map[string]*state.AlertedMarket {
	out := make(map[string]*state.AlertedMarket, len(in))
	for k, v := range in {
		cp := *v
		out[k] = &cp
	}
	return out
}

func copyWatchlist(in map[string]*state.WatchItem) map[string]*state.WatchItem {
	out := make(map[string]*state.WatchItem, len(in))
	for k, v := range in {
		cp := *v
		out[k] = &cp
	}
	return out
}

func copyBoolMap(in map[string]bool) map[string]bool {
	out := make(map[string]bool, len(in))
	for k, v := range in {
		out[k] = v
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

func marketURL(ev kalshi.Event, m kalshi.Market) string {
	if m.Ticker != "" {
		return fmt.Sprintf("https://kalshi.com/markets/%s/%s", strings.ToLower(ev.SeriesTicker), strings.ToLower(m.Ticker))
	}
	return fmt.Sprintf("https://kalshi.com/markets/%s", strings.ToLower(ev.SeriesTicker))
}
