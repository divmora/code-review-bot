package vcs

import (
	"context"
	"errors"
	"testing"
)

func TestTrustedAuthors(t *testing.T) {
	for _, c := range []struct {
		self string
		a    ghAuthor
		want bool
	}{
		{"gh-bot", ghAuthor{"gh-bot", "User"}, true},
		{"gh-bot", ghAuthor{"GH-Bot", "User"}, true},
		{"gh-bot", ghAuthor{"mallory", "User"}, false},
		{"gh-bot", ghAuthor{"other-app[bot]", "Bot"}, false}, // a user token: only that user
		{"", ghAuthor{"my-app[bot]", "Bot"}, true},           // App or Actions token
		{"", ghAuthor{"github-actions[bot]", "Bot"}, true},
		{"", ghAuthor{"mallory", "User"}, false},
		{"", ghAuthor{"mallory[bot]", "User"}, false},
	} {
		if got := githubTrusted(c.self, c.a); got != c.want {
			t.Errorf("github %q %+v = %v", c.self, c.a, got)
		}
	}
	if !bitbucketTrusted("{u1}", "{u1}", "user") || bitbucketTrusted("{u1}", "{u2}", "app_user") ||
		!bitbucketTrusted("", "{x}", "app_user") || bitbucketTrusted("", "{x}", "user") {
		t.Error("bitbucket trust")
	}
}

func TestSelfCache(t *testing.T) {
	var s selfCache
	calls := 0
	fail := func(context.Context) (string, error) { calls++; return "", errors.New("down") }
	ok := func(context.Context) (string, error) { calls++; return "42", nil }
	if _, err := s.get(context.Background(), fail); err == nil {
		t.Fatal("error passed on")
	}
	if id, _ := s.get(context.Background(), ok); id != "42" {
		t.Fatal("a failed lookup is tried again")
	}
	if id, _ := s.get(context.Background(), fail); id != "42" || calls != 2 {
		t.Errorf("a known identity sticks: %q %d", id, calls)
	}
}
