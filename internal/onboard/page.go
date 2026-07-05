package onboard

import (
	"fmt"
	"html"
	"sort"
	"strings"
)

// PageData drives the onboarding preference form.
type PageData struct {
	Token          string
	Categories     []string
	TagsByCategory map[string][]string
	Error          string
}

// RenderForm returns the onboarding HTML page with category/subcategory checkboxes.
func RenderForm(data PageData) string {
	var b strings.Builder
	b.WriteString(pageHeader("Choose your alert categories"))
	if data.Error != "" {
		fmt.Fprintf(&b, `<p class="error">%s</p>`, html.EscapeString(data.Error))
	}
	b.WriteString(`<form method="POST" action="/onboard/`)
	b.WriteString(html.EscapeString(data.Token))
	b.WriteString(`">`)
	b.WriteString(`<p class="hint">Select the categories you want alerts for. Optionally narrow each category with subcategories. Leave everything unchecked to finish setup without alerts.</p>`)

	for _, cat := range data.Categories {
		catID := slug(cat)
		fmt.Fprintf(&b, `<div class="category-block" data-category="%s">`, html.EscapeString(catID))
		fmt.Fprintf(&b, `<label class="category-label"><input type="checkbox" name="category" value="%s" class="category-check"> %s</label>`,
			html.EscapeString(cat), html.EscapeString(cat))
		tags := data.TagsByCategory[cat]
		if len(tags) == 0 {
			b.WriteString(`<p class="no-tags">No subcategories available.</p>`)
		} else {
			b.WriteString(`<div class="subcategories">`)
			for _, tag := range tags {
				fmt.Fprintf(&b, `<label class="sub-label"><input type="checkbox" name="sub_%s" value="%s" class="sub-check" disabled> %s</label>`,
					html.EscapeString(catID), html.EscapeString(tag), html.EscapeString(tag))
			}
			b.WriteString(`</div>`)
		}
		b.WriteString(`</div>`)
	}

	b.WriteString(`<button type="submit">Save preferences</button></form>`)
	b.WriteString(pageFooter)
	return b.String()
}

// RenderSuccess returns the post-save confirmation page.
func RenderSuccess() string {
	return pageHeader("Preferences saved") +
		`<p>You're all set. Alerts will only cover the categories and subcategories you selected.</p>
<p>If you left everything unchecked, you won't receive alerts until you update your preferences.</p>` +
		pageFooter
}

// RenderError returns a simple error page for invalid or expired links.
func RenderError(msg string) string {
	return pageHeader("Link unavailable") +
		`<p>` + html.EscapeString(msg) + `</p>
<p>Request a new onboarding link by restarting the service or contacting the bot owner.</p>` +
		pageFooter
}

func pageHeader(title string) string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>%s — Kalshi Alerts</title>
  <style>
    body { font-family: system-ui, sans-serif; max-width: 640px; margin: 2rem auto; padding: 0 1rem; line-height: 1.5; color: #111; }
    h1 { font-size: 1.5rem; margin-bottom: 0.5rem; }
    .hint { color: #444; margin-bottom: 1.5rem; }
    .error { color: #b00020; background: #fde8eb; padding: 0.75rem 1rem; border-radius: 6px; }
    .category-block { border: 1px solid #e5e5e5; border-radius: 8px; padding: 1rem; margin-bottom: 0.75rem; }
    .category-label { font-weight: 600; display: block; margin-bottom: 0.5rem; }
    .subcategories { margin-left: 1.25rem; display: grid; gap: 0.35rem; }
    .sub-label { font-weight: 400; color: #333; font-size: 0.95rem; }
    .no-tags { margin: 0.25rem 0 0 1.25rem; color: #666; font-size: 0.9rem; }
    button { margin-top: 1rem; background: #111; color: #fff; border: none; border-radius: 6px; padding: 0.65rem 1.25rem; font-size: 1rem; cursor: pointer; }
    button:hover { background: #333; }
    input[type="checkbox"] { margin-right: 0.4rem; }
  </style>
</head>
<body>
  <h1>%s</h1>`, html.EscapeString(title), html.EscapeString(title))
}

const pageFooter = `<script>
document.querySelectorAll('.category-check').forEach(function(cat) {
  cat.addEventListener('change', function() {
    var block = cat.closest('.category-block');
    block.querySelectorAll('.sub-check').forEach(function(sub) {
      sub.disabled = !cat.checked;
      if (!cat.checked) sub.checked = false;
    });
  });
});
</script>
</body>
</html>`

func slug(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// ParseForm extracts selected categories and subcategories from a POST body.
func ParseForm(categories []string, tagsByCategory map[string][]string, values map[string][]string) ([]string, map[string][]string) {
	selectedCats := map[string]bool{}
	for _, cat := range values["category"] {
		selectedCats[cat] = true
	}

	var chosen []string
	subs := map[string][]string{}
	for _, cat := range categories {
		if !selectedCats[cat] {
			continue
		}
		chosen = append(chosen, cat)
		catID := slug(cat)
		allowed := map[string]bool{}
		for _, tag := range tagsByCategory[cat] {
			allowed[tag] = true
		}
		for _, tag := range values["sub_"+catID] {
			if allowed[tag] {
				subs[cat] = append(subs[cat], tag)
			}
		}
		if len(subs[cat]) > 0 {
			sort.Strings(subs[cat])
		}
	}
	sort.Strings(chosen)
	return chosen, subs
}
