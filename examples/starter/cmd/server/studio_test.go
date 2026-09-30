package main

import (
	"strings"
	"testing"
)

func TestStudioTokens(t *testing.T) {
	const admin = "admin-token-0123456789"
	cases := []struct {
		name    string
		admin   string
		env     map[string]string
		wantErr string
		wantIDs []string
	}{
		{name: "admin only", admin: admin, wantIDs: []string{"admin"}},
		{name: "all three", admin: admin,
			env:     map[string]string{"STARTER_REVIEWER_TOKEN": "reviewer-token-0123456", "STARTER_EDITOR_TOKEN": "editor-token-01234567"},
			wantIDs: []string{"admin", "editor", "reviewer"}},
		{name: "short admin", admin: "short", wantErr: "STARTER_ADMIN_TOKEN"},
		{name: "short editor", admin: admin, env: map[string]string{"STARTER_EDITOR_TOKEN": "short"}, wantErr: "STARTER_EDITOR_TOKEN"},
		{name: "short reviewer", admin: admin, env: map[string]string{"STARTER_REVIEWER_TOKEN": "short"}, wantErr: "STARTER_REVIEWER_TOKEN"},
		{name: "editor repeats admin", admin: admin, env: map[string]string{"STARTER_EDITOR_TOKEN": admin}, wantErr: "STARTER_EDITOR_TOKEN"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("STARTER_REVIEWER_TOKEN", "")
			t.Setenv("STARTER_EDITOR_TOKEN", "")
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			tokens, err := studioTokens(c.admin)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want mention of %s", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, id := range tokens {
				got = append(got, id.Name)
			}
			if len(got) != len(c.wantIDs) {
				t.Fatalf("identities = %v, want %v", got, c.wantIDs)
			}
		})
	}
}
