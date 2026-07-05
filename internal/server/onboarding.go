package server

import (
	"context"
	"net/http"
	"time"

	"github.com/kourosh/kalshi-search/internal/onboard"
	"github.com/kourosh/kalshi-search/internal/state"
)

func (s *Server) handleOnboardGet(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if token == "" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	tok, err := s.store.LookupOnboardingToken(ctx, token)
	if err != nil {
		s.logger.Warn("invalid onboarding link", "error", err, "remote", r.RemoteAddr)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusGone)
		_, _ = w.Write([]byte(onboard.RenderError("This link is invalid, expired, or has already been used.")))
		return
	}

	var menu []string
	var tags map[string][]string
	if err := s.store.View(ctx, tok.Phone, func(d *state.Data) {
		menu = append([]string(nil), d.CategoryMenu...)
		tags = copyTags(d.TagsByCategory)
	}); err != nil {
		s.logger.Error("loading onboarding menu failed", "error", err, "phone", tok.Phone)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(onboard.RenderForm(onboard.PageData{
		Token: token, Categories: menu, TagsByCategory: tags,
	})))
}

func (s *Server) handleOnboardPost(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if token == "" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	tok, err := s.store.LookupOnboardingToken(ctx, token)
	if err != nil {
		s.logger.Warn("invalid onboarding submit", "error", err, "remote", r.RemoteAddr)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusGone)
		_, _ = w.Write([]byte(onboard.RenderError("This link is invalid, expired, or has already been used.")))
		return
	}

	var menu []string
	var tags map[string][]string
	if err := s.store.View(ctx, tok.Phone, func(d *state.Data) {
		menu = append([]string(nil), d.CategoryMenu...)
		tags = copyTags(d.TagsByCategory)
	}); err != nil {
		s.logger.Error("loading onboarding menu failed", "error", err, "phone", tok.Phone)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	categories, subcategories := onboard.ParseForm(menu, tags, r.PostForm)
	if err := s.store.Update(ctx, tok.Phone, func(d *state.Data) {
		d.Categories = categories
		d.Subcategories = subcategories
	}); err != nil {
		s.logger.Error("saving onboarding preferences failed", "error", err, "phone", tok.Phone)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(onboard.RenderForm(onboard.PageData{
			Token: token, Categories: menu, TagsByCategory: tags,
			Error: "Something went wrong saving your preferences. Please try again.",
		})))
		return
	}
	if err := s.store.MarkOnboardingTokenUsed(ctx, token); err != nil {
		s.logger.Error("marking onboarding token used failed", "error", err, "token", token)
	}

	s.logger.Info("onboarding complete",
		"phone", tok.Phone, "categories", categories, "subcategories", subcategories)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(onboard.RenderSuccess()))
}

func copyTags(in map[string][]string) map[string][]string {
	out := make(map[string][]string, len(in))
	for k, v := range in {
		out[k] = append([]string(nil), v...)
	}
	return out
}
