package vcs

import (
	"context"
	"strings"
	"sync"
)

// Only the bot's own comments are read back as its state: the summary (and
// the reviewed commit, scores and findings kept in it), fingerprints and
// earlier inline comments. Anyone taking part in a PR can write a comment
// that looks like the bot's (issue #21), which could block reviews (the
// bot then edits a note it can't), make --auto skip, or keep findings
// from being posted.

// selfCache remembers who the token is, looked up once per client.
type selfCache struct {
	mu   sync.Mutex
	done bool
	id   string
	err  error
}

func (s *selfCache) get(ctx context.Context, lookup func(context.Context) (string, error)) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.done {
		s.id, s.err = lookup(ctx)
		// A failed lookup is tried again next time; a known identity sticks.
		s.done = s.err == nil
	}
	return s.id, s.err
}

// ghAuthor is a GitHub comment's author.
type ghAuthor struct {
	Login string `json:"login"`
	Type  string `json:"type"`
}

// githubTrusted: a comment by the token's user; with an App or Actions
// token, which has no user (self is ""), a bot account: participants are
// users, so they can't pass for the bot.
func githubTrusted(self string, a ghAuthor) bool {
	if self != "" {
		return strings.EqualFold(a.Login, self)
	}
	return a.Type == "Bot" && strings.HasSuffix(a.Login, "[bot]")
}
