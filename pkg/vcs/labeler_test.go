package vcs

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGitLabLabeler(t *testing.T) {
	var edited map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/labels"):
			if r.URL.Query().Get("include_ancestor_groups") != "true" {
				t.Errorf("group labels: %s", r.URL.RawQuery)
			}
			if r.URL.Query().Get("page") == "1" {
				w.Header().Set("X-Next-Page", "2")
				_, _ = io.WriteString(w, `[{"name":"type::bug","description":"A defect"},{"name":"backend"}]`)
				return
			}
			_, _ = io.WriteString(w, `[{"name":"type::bug"},{"name":"component::database"}]`)
		case r.Method == "GET" && strings.Contains(r.URL.Path, "/merge_requests/3"):
			_, _ = io.WriteString(w, `{"labels":["backend","Quality Score::90"]}`)
		case r.Method == "PUT" && strings.Contains(r.URL.Path, "/merge_requests/3"):
			_ = json.NewDecoder(r.Body).Decode(&edited)
			_, _ = io.WriteString(w, `{}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
		}
	}))
	defer srv.Close()
	g := NewGitLab(srv.URL, "t", false)
	pr := &PR{Ref: Ref{Platform: GitLab, Host: "g", Repo: "grp/app", Number: 3}}
	avail, err := g.AvailableLabels(context.Background(), pr)
	if err != nil || len(avail) != 3 || avail[0].Description != "A defect" {
		t.Fatalf("available (deduplicated across pages): %+v %v", avail, err)
	}
	cur, err := g.PRLabels(context.Background(), pr)
	if err != nil || strings.Join(cur, ",") != "backend,Quality Score::90" {
		t.Errorf("current: %v %v", cur, err)
	}
	if err := g.EditLabels(context.Background(), pr, []string{"type::bug", "component::database"}, []string{"type::feature"}); err != nil {
		t.Fatal(err)
	}
	if edited["add_labels"] != "type::bug,component::database" || edited["remove_labels"] != "type::feature" {
		t.Errorf("edit: %v", edited)
	}
	if !IsScoreLabel("Quality Score::90") || !IsScoreLabel("Security Score: 70") || IsScoreLabel("type::bug") {
		t.Error("IsScoreLabel")
	}
	var _ Labeler = g
	var _ Labeler = NewGitHub("https://api.github.com", StaticToken("t"))
}
