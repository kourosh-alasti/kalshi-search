package onboard

import (
	"reflect"
	"testing"
)

func TestParseForm(t *testing.T) {
	categories := []string{"Politics", "Sports"}
	tags := map[string][]string{
		"Politics": {"Elections", "Congress"},
		"Sports":   {"NFL", "NBA"},
	}
	values := map[string][]string{
		"category":        {"Politics", "Sports"},
		"sub_politics":    {"Elections"},
		"sub_sports":      {"NFL", "NBA"},
	}

	gotCats, gotSubs := ParseForm(categories, tags, values)
	wantCats := []string{"Politics", "Sports"}
	wantSubs := map[string][]string{
		"Politics": {"Elections"},
		"Sports":   {"NBA", "NFL"},
	}
	if !reflect.DeepEqual(gotCats, wantCats) {
		t.Fatalf("categories = %#v, want %#v", gotCats, wantCats)
	}
	if !reflect.DeepEqual(gotSubs, wantSubs) {
		t.Fatalf("subcategories = %#v, want %#v", gotSubs, wantSubs)
	}
}

func TestParseFormEmpty(t *testing.T) {
	gotCats, gotSubs := ParseForm([]string{"Politics"}, map[string][]string{"Politics": {"Elections"}}, map[string][]string{})
	if len(gotCats) != 0 || len(gotSubs) != 0 {
		t.Fatalf("expected empty selections, got cats=%#v subs=%#v", gotCats, gotSubs)
	}
}

func TestSlug(t *testing.T) {
	if got := slug("Rotten Tomatoes"); got != "rotten-tomatoes" {
		t.Fatalf("slug = %q", got)
	}
}
