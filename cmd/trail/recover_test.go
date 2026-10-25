package main

import "testing"

func TestRecoveryHint(t *testing.T) {
	cases := []struct {
		tool     string
		readOnly bool
		contains string
	}{
		{"mcp__gmail__send_email", false, "unsent"},
		{"mcp__slack__post_message", false, "unsent"},
		{"mcp__gmail__search_threads", true, ""},
		{"mcp__github__list_issues", true, ""},
		{"mcp__github__create_issue", false, "Delete or edit it in github"},
		{"mcp__drive__delete_file", false, "trash"},
		{"mcp__stripe__create_refund", false, "Money"},
		{"mcp__linear__update_issue", false, "Change it back"},
		{"mcp__weird__frobnicate", false, "by hand"},
	}
	for _, c := range cases {
		hint, ro := recoveryHint(c.tool)
		if ro != c.readOnly {
			t.Errorf("%s: readOnly = %v", c.tool, ro)
		}
		if c.contains != "" && !contains2(hint, c.contains) {
			t.Errorf("%s: hint %q lacks %q", c.tool, hint, c.contains)
		}
	}
}

func contains2(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
