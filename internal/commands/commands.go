// Package commands parses inbound messages and executes bot commands.
package commands

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kourosh/kalshi-search/internal/config"
	"github.com/kourosh/kalshi-search/internal/learning"
	"github.com/kourosh/kalshi-search/internal/sign"
	"github.com/kourosh/kalshi-search/internal/sms"
	"github.com/kourosh/kalshi-search/internal/state"
)

// Handler executes commands and replies via the configured notification client.
type Handler struct {
	cfg    *config.Config
	store  *state.Store
	sms    sms.Client
	scorer *learning.PreferenceScorer
	sugLog *learning.SuggestionLog
	signer *sign.Signer
	logger *slog.Logger
}

// New returns a command handler.
func New(cfg *config.Config, store *state.Store, smsClient sms.Client, scorer *learning.PreferenceScorer,
	sugLog *learning.SuggestionLog, signer *sign.Signer, logger *slog.Logger) *Handler {
	return &Handler{
		cfg: cfg, store: store, sms: smsClient, scorer: scorer, sugLog: sugLog,
		signer: signer, logger: logger.With("component", "commands"),
	}
}

var (
	numberListRe = regexp.MustCompile(`^\d+(\s*[,\s]\s*\d+)*$`)
	feedbackRe   = regexp.MustCompile(`(?i)^(TOOK|PASS)\s+#?(\d+)$`)
	watchRe      = regexp.MustCompile(`(?i)^WATCH\s+([A-Z0-9-]+)(?:\s+(\d+))?$`)
	unwatchRe    = regexp.MustCompile(`(?i)^UNWATCH\s+([A-Z0-9-]+)$`)
	quietRe      = regexp.MustCompile(`(?i)^QUIET\s+(\d{1,2})\s*-\s*(\d{1,2})$`)
	whyRe        = regexp.MustCompile(`(?i)^WHY\s+#?(\d+)$`)
	filterRe     = regexp.MustCompile(`(?i)^(PRICE|VOLUME|OI|CLOSE)\s+(\d+)$`)
	enhancedRe   = regexp.MustCompile(`(?i)^ENHANCED\s+(ON|OFF)$`)
	enhanceRe    = regexp.MustCompile(`(?i)^ENHANCE\s+(ON|OFF)$`)
)

// Handle processes one inbound message and delivers the reply.
// Returns an error when the reply could not be sent.
func (h *Handler) Handle(ctx context.Context, from, text string) error {
	text = strings.TrimSpace(text)
	h.logger.Info("inbound command", "from", from, "text", text)

	var reply string
	switch {
	case feedbackRe.MatchString(text):
		m := feedbackRe.FindStringSubmatch(text)
		id, _ := strconv.Atoi(m[2])
		reply = h.feedback(ctx, from, strings.EqualFold(m[1], "TOOK"), id)
	case watchRe.MatchString(text):
		m := watchRe.FindStringSubmatch(text)
		maxPrice := 0
		if m[2] != "" {
			maxPrice, _ = strconv.Atoi(m[2])
		}
		reply = h.watch(ctx, from, strings.ToUpper(m[1]), maxPrice)
	case unwatchRe.MatchString(text):
		m := unwatchRe.FindStringSubmatch(text)
		reply = h.unwatch(ctx, from, strings.ToUpper(m[1]))
	case quietRe.MatchString(text):
		m := quietRe.FindStringSubmatch(text)
		start, _ := strconv.Atoi(m[1])
		end, _ := strconv.Atoi(m[2])
		reply = h.setQuietHours(ctx, from, start, end)
	case strings.EqualFold(text, "QUIET OFF"):
		reply = h.setQuietEnabled(ctx, from, false)
	case strings.EqualFold(text, "ENABLE"):
		reply = h.setQuietEnabled(ctx, from, true)
	case strings.EqualFold(text, "DISABLE"):
		reply = h.setQuietEnabled(ctx, from, false)
	case strings.EqualFold(text, "PREFS"):
		reply = h.prefsLink(ctx, from)
	case whyRe.MatchString(text):
		m := whyRe.FindStringSubmatch(text)
		id, _ := strconv.Atoi(m[1])
		reply = h.why(ctx, from, id)
	case enhancedRe.MatchString(text) || enhanceRe.MatchString(text):
		var action string
		if enhancedRe.MatchString(text) {
			action = enhancedRe.FindStringSubmatch(text)[1]
		} else {
			action = enhanceRe.FindStringSubmatch(text)[1]
		}
		reply = h.setEnhanced(ctx, from, strings.EqualFold(action, "ON"))
	case filterRe.MatchString(text):
		m := filterRe.FindStringSubmatch(text)
		val, _ := strconv.Atoi(m[2])
		reply = h.setFilter(ctx, from, strings.ToUpper(m[1]), val)
	case strings.EqualFold(text, "RESET FILTERS"):
		reply = h.resetFilters(ctx, from)
	case numberListRe.MatchString(text):
		reply = h.selectCategories(ctx, from, text)
	case strings.EqualFold(text, "ALL"):
		reply = h.selectAll(ctx, from)
	case strings.EqualFold(text, "LIST"):
		reply = h.list(ctx, from)
	case strings.EqualFold(text, "STATUS"):
		reply = h.status(ctx, from)
	case strings.EqualFold(text, "PAUSE"):
		reply = h.setPaused(ctx, from, true)
	case strings.EqualFold(text, "RESUME"):
		reply = h.setPaused(ctx, from, false)
	case strings.EqualFold(text, "STOP") || strings.EqualFold(text, "STOPALL") ||
		strings.EqualFold(text, "UNSUBSCRIBE") || strings.EqualFold(text, "CANCEL") ||
		strings.EqualFold(text, "END") || strings.EqualFold(text, "QUIT"):
		if err := h.setPausedState(ctx, from, true); err != nil {
			reply = "Something went wrong, try again."
		} else {
			reply = "You are unsubscribed from Kalshi Alerts and will receive no more messages. Reply START to resubscribe."
		}
	case strings.EqualFold(text, "START") || strings.EqualFold(text, "UNSTOP"):
		if err := h.setPausedState(ctx, from, false); err != nil {
			reply = "Something went wrong, try again."
		} else {
			reply = "You are subscribed to Kalshi Alerts. Reply HELP for commands, STOP to cancel."
		}
	case strings.EqualFold(text, "HELP") || strings.EqualFold(text, "INFO"):
		reply = h.helpText()
	default:
		reply = h.helpText()
	}

	if err := h.sms.SendSMS(ctx, from, reply); err != nil {
		h.logger.Error("sending command reply failed", "error", err)
		return err
	}
	return nil
}

func (h *Handler) helpText() string {
	return "Commands:\nTOOK/PASS <id> — feedback\nWHY <id> — why we suggested it\nPREFS — update categories\n" +
		"ENHANCED ON/OFF — ML-ranked enhanced suggestions\n" +
		"WATCH <ticker> [max¢] / UNWATCH <ticker>\nQUIET 22-8 / QUIET OFF\nENABLE/DISABLE — quiet hours on/off\n" +
		"PRICE/VOLUME/OI/CLOSE <n> — your filter overrides\nRESET FILTERS — clear overrides\nSTATUS, PAUSE, RESUME"
}

// ApplyFeedback records TOOK/PASS from signed web links (no SMS reply).
func (h *Handler) ApplyFeedback(ctx context.Context, phone string, took bool, id int) (string, error) {
	reply := h.feedback(ctx, phone, took, id)
	return reply, nil
}

func (h *Handler) prefsLink(ctx context.Context, phone string) string {
	token, err := h.store.CreateOnboardingToken(ctx, phone, h.cfg.OnboardingTokenTTL)
	if err != nil {
		h.logger.Error("creating prefs token failed", "error", err)
		return "Something went wrong, try again."
	}
	link := h.signer.PrefsURL(h.cfg.PublicBaseURL, token)
	return fmt.Sprintf("Update your preferences here (expires in %s):\n%s", formatTTL(h.cfg.OnboardingTokenTTL), link)
}

func (h *Handler) watch(ctx context.Context, phone, ticker string, maxPrice int) string {
	err := h.store.Update(ctx, phone, func(d *state.Data) {
		d.Watchlist[ticker] = &state.WatchItem{
			Ticker: ticker, MaxPriceCents: maxPrice, CreatedAt: time.Now(),
		}
	})
	if err != nil {
		return "Something went wrong, try again."
	}
	if maxPrice > 0 {
		return fmt.Sprintf("Watching %s at ≤%d¢.", ticker, maxPrice)
	}
	return fmt.Sprintf("Watching %s.", ticker)
}

func (h *Handler) unwatch(ctx context.Context, phone, ticker string) string {
	err := h.store.Update(ctx, phone, func(d *state.Data) {
		delete(d.Watchlist, ticker)
	})
	if err != nil {
		return "Something went wrong, try again."
	}
	return fmt.Sprintf("Stopped watching %s.", ticker)
}

func (h *Handler) setEnhanced(ctx context.Context, phone string, enabled bool) string {
	err := h.store.Update(ctx, phone, func(d *state.Data) {
		d.EnhancedSuggestions = enabled
	})
	if err != nil {
		return "Something went wrong, try again."
	}
	if enabled {
		return fmt.Sprintf("Enhanced suggestions ON. Picks will use ML ranking once you have enough TOOK/PASS feedback (%d+ labels). Reply ENHANCED OFF to revert.",
			h.cfg.MLMinTrainingExamples)
	}
	return "Enhanced suggestions OFF. Using standard preference ranking."
}

func (h *Handler) setQuietHours(ctx context.Context, phone string, start, end int) string {
	if start < 0 || start > 23 || end < 0 || end > 23 {
		return "Hours must be 0-23. Example: QUIET 22-8"
	}
	tz := "America/New_York"
	err := h.store.Update(ctx, phone, func(d *state.Data) {
		d.QuietHours.StartHour = start
		d.QuietHours.EndHour = end
		d.QuietHours.Enabled = true
		if d.QuietHours.Timezone == "" {
			d.QuietHours.Timezone = tz
		}
		tz = d.QuietHours.Timezone
	})
	if err != nil {
		return "Something went wrong, try again."
	}
	return fmt.Sprintf("Quiet hours set to %d:00–%d:00 (%s). Reply DISABLE to turn off.",
		start, end, tz)
}

func (h *Handler) setQuietEnabled(ctx context.Context, phone string, enabled bool) string {
	err := h.store.Update(ctx, phone, func(d *state.Data) {
		d.QuietHours.Enabled = enabled
	})
	if err != nil {
		return "Something went wrong, try again."
	}
	if enabled {
		return "Quiet hours enabled. Reply DISABLE to turn off."
	}
	return "Quiet hours disabled. Alerts will send any time."
}

func (h *Handler) why(ctx context.Context, phone string, id int) string {
	var title string
	var features []string
	var explain string
	found := false
	_ = h.store.View(ctx, phone, func(d *state.Data) {
		if sug, ok := d.Suggestions[strconv.Itoa(id)]; ok {
			found = true
			title = sug.Title
			features = append([]string(nil), sug.Features...)
			explain = sug.Explain
		}
	})
	if !found {
		return fmt.Sprintf("No suggestion #%d found.", id)
	}
	if explain == "" {
		explain = h.scorer.Explain(ctx, phone, features)
	}
	return fmt.Sprintf("#%d %s\n%s", id, title, explain)
}

func (h *Handler) setFilter(ctx context.Context, phone, kind string, val int) string {
	err := h.store.Update(ctx, phone, func(d *state.Data) {
		switch kind {
		case "PRICE":
			d.FilterOverrides.MaxPriceCents = &val
		case "VOLUME":
			d.FilterOverrides.MinVolume = &val
		case "OI":
			d.FilterOverrides.MinOpenInterest = &val
		case "CLOSE":
			d.FilterOverrides.CloseWithinHours = &val
		}
	})
	if err != nil {
		return "Something went wrong, try again."
	}
	return fmt.Sprintf("Your %s filter is now %d.", strings.ToLower(kind), val)
}

func (h *Handler) resetFilters(ctx context.Context, phone string) string {
	err := h.store.Update(ctx, phone, func(d *state.Data) {
		d.FilterOverrides = state.FilterOverrides{}
	})
	if err != nil {
		return "Something went wrong, try again."
	}
	return "Filter overrides cleared. Using service defaults."
}

func (h *Handler) selectCategories(ctx context.Context, phone, text string) string {
	var picks []int
	for _, tok := range regexp.MustCompile(`[,\s]+`).Split(text, -1) {
		if tok == "" {
			continue
		}
		n, err := strconv.Atoi(tok)
		if err != nil {
			continue
		}
		picks = append(picks, n)
	}

	var chosen, invalid []string
	err := h.store.Update(ctx, phone, func(d *state.Data) {
		for _, n := range picks {
			if n < 1 || n > len(d.CategoryMenu) {
				invalid = append(invalid, strconv.Itoa(n))
				continue
			}
			cat := d.CategoryMenu[n-1]
			if !contains(chosen, cat) {
				chosen = append(chosen, cat)
			}
		}
		if len(chosen) > 0 {
			d.Categories = chosen
		}
	})
	if err != nil {
		return "Something went wrong saving your selection, try again."
	}
	if len(chosen) == 0 {
		return "No valid selections. Reply LIST to see the category menu."
	}
	reply := "Alerts enabled for: " + strings.Join(chosen, ", ")
	if len(invalid) > 0 {
		reply += "\nIgnored invalid numbers: " + strings.Join(invalid, ", ")
	}
	return reply
}

func (h *Handler) selectAll(ctx context.Context, phone string) string {
	var all []string
	err := h.store.Update(ctx, phone, func(d *state.Data) {
		d.Categories = append([]string(nil), d.CategoryMenu...)
		all = d.Categories
	})
	if err != nil {
		return "Something went wrong, try again."
	}
	return fmt.Sprintf("Alerts enabled for all %d categories.", len(all))
}

func (h *Handler) feedback(ctx context.Context, phone string, took bool, id int) string {
	label := state.LabelPass
	if took {
		label = state.LabelTook
	}

	var sug *state.Suggestion
	var already state.SuggestionLabel
	err := h.store.Update(ctx, phone, func(d *state.Data) {
		s, ok := d.Suggestions[strconv.Itoa(id)]
		if !ok {
			return
		}
		if s.Label != state.LabelPending {
			already = s.Label
			return
		}
		s.Label = label
		if !took {
			if am, ok := d.Alerted[s.Ticker]; ok {
				am.Suppressed = true
				if h.cfg.PassSuppressDays > 0 {
					am.SuppressedUntil = time.Now().Add(time.Duration(h.cfg.PassSuppressDays) * 24 * time.Hour)
				}
			}
		}
		sug = s
	})
	if err != nil {
		return "Something went wrong, try again."
	}
	if already != "" {
		return fmt.Sprintf("#%d already marked %s.", id, already)
	}
	if sug == nil {
		return fmt.Sprintf("No suggestion #%d found.", id)
	}

	h.scorer.Feed(ctx, phone, sug.Features, took)
	h.sugLog.Append(ctx, phone, learning.SuggestionRecord{
		Kind: "label", Timestamp: time.Now(), SuggestionID: id, Label: string(label),
	})

	if took {
		return fmt.Sprintf("Marked #%d (%s) as taken.", id, sug.Title)
	}
	if h.cfg.PassSuppressDays > 0 {
		return fmt.Sprintf("Passing on #%d (%s). Suppressed for %d days.", id, sug.Title, h.cfg.PassSuppressDays)
	}
	return fmt.Sprintf("Passing on #%d (%s).", id, sug.Title)
}

func (h *Handler) list(ctx context.Context, phone string) string {
	var menu []string
	if err := h.store.View(ctx, phone, func(d *state.Data) { menu = d.CategoryMenu }); err != nil {
		return "Something went wrong, try again."
	}
	if len(menu) == 0 {
		return "Category menu not loaded yet, try again in a minute."
	}
	var b strings.Builder
	b.WriteString("Reply with numbers to enable categories (e.g. 1,3):\n")
	for i, c := range menu {
		fmt.Fprintf(&b, "%d. %s\n", i+1, c)
	}
	b.WriteString("Or reply ALL, or PREFS for the web form.")
	return b.String()
}

func (h *Handler) status(ctx context.Context, phone string) string {
	var paused bool
	var categories []string
	var subcategories map[string][]string
	var quiet state.QuietHours
	var overrides state.FilterOverrides
	var enhanced bool
	var watchlist []string
	var pending, took, passed, positions int
	if err := h.store.View(ctx, phone, func(d *state.Data) {
		paused = d.Paused
		categories = d.Categories
		subcategories = d.Subcategories
		quiet = d.QuietHours
		overrides = d.FilterOverrides
		enhanced = d.EnhancedSuggestions
		for t := range d.Watchlist {
			watchlist = append(watchlist, t)
		}
		for _, s := range d.Suggestions {
			switch s.Label {
			case state.LabelPending:
				pending++
			case state.LabelTook:
				took++
			case state.LabelPass:
				passed++
			case state.LabelPosition:
				positions++
			}
		}
	}); err != nil {
		return "Something went wrong, try again."
	}

	stateStr := "active"
	if paused {
		stateStr = "PAUSED"
	}
	cats := "none (reply PREFS)"
	if len(categories) > 0 {
		cats = strings.Join(categories, ", ")
	}
	sort.Strings(watchlist)
	watchStr := "none"
	if len(watchlist) > 0 {
		watchStr = strings.Join(watchlist, ", ")
	}
	quietStr := "off"
	if quiet.Enabled {
		quietStr = fmt.Sprintf("%d:00-%d:00 %s", quiet.StartHour, quiet.EndHour, quiet.Timezone)
	}
	filterStr := "defaults"
	if overrides.MaxPriceCents != nil || overrides.MinVolume != nil ||
		overrides.MinOpenInterest != nil || overrides.CloseWithinHours != nil {
		filterStr = "custom (RESET FILTERS to clear)"
	}
	enhStr := "off"
	if enhanced {
		enhStr = "on"
	}
	subs := formatSubs(subcategories)
	return fmt.Sprintf("Status: %s\nCategories: %s\nSubcategories: %s\nWatchlist: %s\nQuiet hours: %s\nEnhanced: %s\nFilters: %s\nSuggestions: %d pending, %d took, %d passed, %d auto-trades",
		stateStr, cats, subs, watchStr, quietStr, enhStr, filterStr, pending, took, passed, positions)
}

func formatSubs(subcategories map[string][]string) string {
	if len(subcategories) == 0 {
		return "none"
	}
	var parts []string
	for cat, tags := range subcategories {
		if len(tags) > 0 {
			parts = append(parts, fmt.Sprintf("%s (%s)", cat, strings.Join(tags, ", ")))
		}
	}
	if len(parts) == 0 {
		return "none"
	}
	sort.Strings(parts)
	return strings.Join(parts, "; ")
}

func (h *Handler) setPausedState(ctx context.Context, phone string, paused bool) error {
	return h.store.Update(ctx, phone, func(d *state.Data) { d.Paused = paused })
}

func (h *Handler) setPaused(ctx context.Context, phone string, paused bool) string {
	if err := h.setPausedState(ctx, phone, paused); err != nil {
		return "Something went wrong, try again."
	}
	if paused {
		return "Alerts paused. Reply RESUME to turn them back on."
	}
	return "Alerts resumed."
}

func formatTTL(d time.Duration) string {
	hours := int(d / time.Hour)
	if hours >= 24 && hours%24 == 0 {
		return fmt.Sprintf("%d days", hours/24)
	}
	return d.Round(time.Hour).String()
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
