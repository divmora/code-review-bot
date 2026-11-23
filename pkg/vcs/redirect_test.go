package vcs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// Issue #23: a redirect to another host must not carry the token there.
func TestRedirectsStayOnTheHost(t *testing.T) {
	var leaked atomic.Value
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked.Store(r.Header.Get("PRIVATE-TOKEN") + r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"username":"x"}`))
	}))
	defer evil.Close()
	// 127.0.0.1 and localhost are different hosts to the redirect rule.
	evilURL := strings.Replace(evil.URL, "127.0.0.1", "localhost", 1)
	gl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v4/moved" {
			_, _ = w.Write([]byte(`{"username":"gitlab-bot"}`))
			return
		}
		if r.URL.Path == "/api/v4/user" && r.URL.Query().Get("same") == "1" {
			http.Redirect(w, r, "/api/v4/moved", http.StatusFound)
			return
		}
		http.Redirect(w, r, evilURL+"/collect", http.StatusFound)
	}))
	defer gl.Close()

	c := NewGitLab(gl.URL, "glpat-secret", false)
	_, err := c.CurrentUser(context.Background())
	if err == nil || !strings.Contains(err.Error(), "another host") {
		t.Errorf("a redirect to another host is refused: %v", err)
	}
	if v, _ := leaked.Load().(string); v != "" {
		t.Errorf("the token reached the other host: %q", v)
	}
	var v struct {
		Username string `json:"username"`
	}
	if _, err := c.r.do(context.Background(), "GET", "/user?same=1", nil, &v); err != nil || v.Username != "gitlab-bot" {
		t.Errorf("a redirect on the same host still works: %v %+v", err, v)
	}
}

func TestSameHostRedirects(t *testing.T) {
	req := func(u string) *http.Request { r, _ := http.NewRequest("GET", u, nil); return r }
	from := []*http.Request{req("https://gitlab.example.com/api/v4/x")}
	for u, ok := range map[string]bool{
		"https://gitlab.example.com/api/v4/y": true,
		"https://GITLAB.example.com/api/v4/y": true,
		"https://evil.example/api/v4/y":       false,
		"http://gitlab.example.com/api/v4/y":  false,
	} {
		if err := sameHostRedirects(req(u), from); (err == nil) != ok {
			t.Errorf("%s: %v", u, err)
		}
	}
}
