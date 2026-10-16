package main

import (
	"slices"
	"testing"
)

func TestSplitList(t *testing.T) {
	got := splitList(" gemini-3.6-flash, gemini-3.5-flash ,,")
	if want := []string{"gemini-3.6-flash", "gemini-3.5-flash"}; !slices.Equal(got, want) {
		t.Errorf("splitList = %q, want %q", got, want)
	}
	if got := splitList(""); len(got) != 0 {
		t.Errorf("empty = %q", got)
	}
}

func TestParseMembers(t *testing.T) {
	got, err := parseMembers(" 123456:Мама , 789012: Папа ,")
	if err != nil || len(got) != 2 || got[123456] != "Мама" || got[789012] != "Папа" {
		t.Errorf("parseMembers = %v, %v", got, err)
	}
	if got, err := parseMembers(""); err != nil || len(got) != 0 {
		t.Errorf("empty = %v, %v; want open mode", got, err)
	}
	for _, bad := range []string{"123456", "abc:Мама", "123456:", "-1:Мама"} {
		if _, err := parseMembers(bad); err == nil {
			t.Errorf("parseMembers(%q) accepted, want error", bad)
		}
	}
}
