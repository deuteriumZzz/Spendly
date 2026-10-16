package main

import "testing"

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
