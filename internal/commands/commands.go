// Package commands parses inbound SMS messages and executes bot commands:
// category selection, TOOK/PASS feedback, LIST, STATUS, PAUSE, RESUME, ALL.
package commands

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"

	"github.com/kourosh/kalshi-search/internal/learning"
	"github.com/kourosh/kalshi-search/internal/sms"
	"github.com/kourosh/kalshi-search/internal/state"
)

// Handler executes SMS commands and replies via the configured SMS provider.
type Handler struct {
	store  *state.Store
	sms    sms.Client
	scorer *learning.CounterScorer
	sugLog *learning.SuggestionLog
	logger *slog.Logger
}

// New returns a command handler. Replies go to whichever allowed number sent
// the command.
func New(store *state.Store, sms sms.Client, scorer *learning.CounterScorer,
	sugLog *learning.SuggestionLog, logger *slog.Logger) *Handler {
	return &Handler{
		store: store, sms: sms, scorer: scorer, sugLog: sugLog,
		logger: logger.With("component", "commands"),
	}
}

var (
	numberListRe = regexp.MustCompile(`^\d+(\s*[,\s]\s*\d+)*$`)
	feedbackRe   = regexp.MustCompile(`(?i)^(TOOK|PASS)\s+#?(\d+)$`)
)

// Handle processes one inbound message and sends the reply. Unknown input
// gets a help message.
func (h *Handler) Handle(ctx context.Context, from, text string) {
	text = strings.TrimSpace(text)
	h.logger.Info("inbound command", "from", from, "text", text)

	var reply string
	switch {
	case feedbackRe.MatchString(text):
		m := feedbackRe.FindStringSubmatch(text)
		id, _ := strconv.Atoi(m[2])
		reply = h.feedback(ctx, from, strings.EqualFold(m[1], "TOOK"), id)
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
	// Carrier compliance keywords. Telnyx also enforces STOP at the
	// platform level; these keep our responses consistent with the
	// declared 10DLC campaign auto-responses.
	case strings.EqualFold(text, "STOP") || strings.EqualFold(text, "STOPALL") ||
		strings.EqualFold(text, "UNSUBSCRIBE") || strings.EqualFold(text, "CANCEL") ||
		strings.EqualFold(text, "END") || strings.EqualFold(text, "QUIT"):
		reply = h.setPaused(ctx, from, true)
		reply = "You are unsubscribed from Kalshi Alerts and will receive no more messages. Reply START to resubscribe. Msg&data rates may apply."
	case strings.EqualFold(text, "START") || strings.EqualFold(text, "UNSTOP"):
		reply = h.setPaused(ctx, from, false)
		reply = "You are subscribed to Kalshi Alerts: market alerts based on your selected categories. Msg frequency varies. Msg&data rates may apply. Reply HELP for help, STOP to cancel."
	case strings.EqualFold(text, "HELP") || strings.EqualFold(text, "INFO"):
		reply = "Kalshi Alerts: SMS alerts for Kalshi prediction markets. Commands: LIST (categories), STATUS, PAUSE, RESUME, TOOK <id>, PASS <id>. Msg&data rates may apply. Reply STOP to cancel."
	default:
		reply = "Commands:\n1,3 = enable categories\nALL = all categories\nTOOK <id> / PASS <id> = feedback\nLIST = category menu\nSTATUS = current settings\nPAUSE / RESUME"
	}

	if err := h.sms.SendSMS(ctx, from, reply); err != nil {
		h.logger.Error("sending command reply failed", "error", err)
	}
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
		h.logger.Error("saving categories failed", "error", err, "phone", phone)
		return "Something went wrong saving your selection, try again."
	}

	if len(chosen) == 0 {
		return "No valid selections. Reply LIST to see the category menu."
	}
	reply := "Alerts enabled for: " + strings.Join(chosen, ", ")
	if len(invalid) > 0 {
		reply += "\nIgnored invalid numbers: " + strings.Join(invalid, ", ")
	}
	h.logger.Info("categories updated", "phone", phone, "categories", chosen)
	return reply
}

func (h *Handler) selectAll(ctx context.Context, phone string) string {
	var all []string
	err := h.store.Update(ctx, phone, func(d *state.Data) {
		d.Categories = append([]string(nil), d.CategoryMenu...)
		all = d.Categories
	})
	if err != nil {
		h.logger.Error("saving categories failed", "error", err, "phone", phone)
		return "Something went wrong, try again."
	}
	h.logger.Info("categories updated", "phone", phone, "categories", all)
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
			}
		}
		sug = s
	})
	if err != nil {
		h.logger.Error("saving feedback failed", "error", err, "phone", phone)
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
		Kind: "label", Timestamp: sug.SentAt, SuggestionID: id, Label: string(label),
	})

	if took {
		return fmt.Sprintf("Nice, marked #%d (%s) as taken. I'll look for more like it.", id, sug.Title)
	}
	return fmt.Sprintf("Got it, passing on #%d (%s). No more alerts for that market.", id, sug.Title)
}

func (h *Handler) list(ctx context.Context, phone string) string {
	var menu []string
	if err := h.store.View(ctx, phone, func(d *state.Data) { menu = d.CategoryMenu }); err != nil {
		h.logger.Error("loading category menu failed", "error", err, "phone", phone)
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
	b.WriteString("Or reply ALL.")
	return b.String()
}

func (h *Handler) status(ctx context.Context, phone string) string {
	var paused bool
	var categories []string
	var pending, took, passed, positions int
	if err := h.store.View(ctx, phone, func(d *state.Data) {
		paused = d.Paused
		categories = d.Categories
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
		h.logger.Error("loading status failed", "error", err, "phone", phone)
		return "Something went wrong, try again."
	}

	stateStr := "active"
	if paused {
		stateStr = "PAUSED"
	}
	cats := "none (reply LIST to choose)"
	if len(categories) > 0 {
		cats = strings.Join(categories, ", ")
	}
	return fmt.Sprintf("Status: %s\nCategories: %s\nSuggestions: %d sent, %d taken, %d passed, %d auto-detected trades",
		stateStr, cats, pending+took+passed+positions, took, passed, positions)
}

func (h *Handler) setPaused(ctx context.Context, phone string, paused bool) string {
	if err := h.store.Update(ctx, phone, func(d *state.Data) { d.Paused = paused }); err != nil {
		h.logger.Error("saving pause state failed", "error", err, "phone", phone)
		return "Something went wrong, try again."
	}
	h.logger.Info("pause state changed", "phone", phone, "paused", paused)
	if paused {
		return "Alerts paused. Reply RESUME to turn them back on."
	}
	return "Alerts resumed."
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
