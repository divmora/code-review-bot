package vcs

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeHost records requests and answers with handle.
type fakeHost struct {
	mu    sync.Mutex
	calls []string
	srv   *httptest.Server
}

func newFakeHost(t *testing.T, handle func(w http.ResponseWriter, r *http.Request, body []byte)) *fakeHost {
	f := &fakeHost{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		f.mu.Unlock()
		handle(w, r, body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeHost) did(call string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == call {
			return true
		}
	}
	return false
}

func TestGitLabApprove(t *testing.T) {
	ctx := context.Background()
	approved := false
	var sha string
	f := newFakeHost(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		switch {
		case r.URL.Path == "/api/v4/user":
			_, _ = io.WriteString(w, `{"id":42,"username":"bot"}`)
		case strings.HasSuffix(r.URL.Path, "/merge_requests/7/approvals"):
			if approved {
				_, _ = io.WriteString(w, `{"approved_by":[{"user":{"id":7}},{"user":{"id":42}}]}`)
			} else {
				_, _ = io.WriteString(w, `{"approved_by":[{"user":{"id":7}}]}`)
			}
		case strings.HasSuffix(r.URL.Path, "/merge_requests/7/approve"):
			var m map[string]string
			_ = json.Unmarshal(body, &m)
			sha, approved = m["sha"], true
			_, _ = io.WriteString(w, `{}`)
		case strings.HasSuffix(r.URL.Path, "/merge_requests/7/unapprove"):
			approved = false
			_, _ = io.WriteString(w, `{}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
		}
	})
	c := NewGitLab(f.srv.URL, "t", false)
	pr := &PR{Ref: Ref{Platform: GitLab, Repo: "g/p", Number: 7}, HeadSHA: "abc"}
	if changed, err := c.Approve(ctx, pr, ""); err != nil || !changed || sha != "abc" {
		t.Fatalf("approve: %v %v sha %q", changed, err, sha)
	}
	if changed, err := c.Approve(ctx, pr, ""); err != nil || changed {
		t.Errorf("already approved by the token's user: %v %v", changed, err)
	}
	if changed, err := c.Unapprove(ctx, pr, ""); err != nil || !changed || approved {
		t.Errorf("unapprove: %v %v", changed, err)
	}
	if changed, err := c.Unapprove(ctx, pr, ""); err != nil || changed {
		t.Errorf("nothing to withdraw (someone else's approval stays): %v %v", changed, err)
	}
}

func TestGitHubApprove(t *testing.T) {
	ctx := context.Background()
	var reviews []map[string]any
	var dismissed []string
	head := "abc"
	f := newFakeHost(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/pulls/3/reviews"):
			b, _ := json.Marshal(append([]map[string]any{
				{"id": 1, "state": "APPROVED", "body": "LGTM", "commit_id": "abc"}, // a person's approval
				{"id": 2, "state": "COMMENTED", "body": ApprovalMarker, "commit_id": "abc"},
			}, reviews...))
			_, _ = w.Write(b)
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/pulls/3/reviews"):
			var m map[string]any
			_ = json.Unmarshal(body, &m)
			if m["event"] != "APPROVE" || !strings.Contains(m["body"].(string), ApprovalMarker) || m["commit_id"] != head {
				t.Errorf("approving review: %v", m)
			}
			reviews = append(reviews, map[string]any{"id": 10 + len(reviews), "state": "APPROVED", "body": m["body"], "commit_id": m["commit_id"]})
			_, _ = io.WriteString(w, `{}`)
		case r.Method == "PUT" && strings.Contains(r.URL.Path, "/dismissals"):
			dismissed = append(dismissed, r.URL.Path)
			for _, rv := range reviews {
				rv["state"] = "DISMISSED"
			}
			_, _ = io.WriteString(w, `{}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
		}
	})
	c := NewGitHub(f.srv.URL, StaticToken("t"))
	pr := &PR{Ref: Ref{Platform: GitHub, Repo: "o/r", Number: 3}, HeadSHA: "abc"}
	if changed, err := c.Unapprove(ctx, pr, "x"); err != nil || changed || len(dismissed) != 0 {
		t.Errorf("a person's approval is never dismissed: %v %v %v", changed, err, dismissed)
	}
	if changed, err := c.Approve(ctx, pr, "Approved by @bot: no findings."); err != nil || !changed {
		t.Fatalf("approve: %v %v", changed, err)
	}
	if changed, err := c.Approve(ctx, pr, "again"); err != nil || changed {
		t.Errorf("already approved at this head: %v %v", changed, err)
	}
	pr.HeadSHA, head = "def", "def" // a new push: approve the new head
	if changed, err := c.Approve(ctx, pr, "new head"); err != nil || !changed {
		t.Errorf("approve a new head: %v %v", changed, err)
	}
	if changed, err := c.Unapprove(ctx, pr, "withdrawn"); err != nil || !changed || len(dismissed) != 2 || !strings.HasSuffix(dismissed[0], "/reviews/10/dismissals") {
		t.Errorf("dismisses the bot's approvals only: %v %v %v", changed, err, dismissed)
	}
}

func TestBitbucketApprove(t *testing.T) {
	ctx := context.Background()
	approved := false
	f := newFakeHost(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if !strings.HasSuffix(r.URL.Path, "/repositories/ws/r/pullrequests/5/approve") {
			t.Errorf("unexpected %s %s", r.Method, r.URL)
			return
		}
		switch {
		case r.Method == "POST" && approved:
			w.WriteHeader(http.StatusConflict)
		case r.Method == "POST":
			approved = true
			_, _ = io.WriteString(w, `{}`)
		case r.Method == "DELETE" && !approved:
			w.WriteHeader(http.StatusNotFound)
		case r.Method == "DELETE":
			approved = false
			w.WriteHeader(http.StatusNoContent)
		}
	})
	c := NewBitbucket(f.srv.URL, BitbucketAuth{Token: "t"})
	pr := &PR{Ref: Ref{Platform: Bitbucket, Repo: "ws/r", Number: 5}}
	if changed, err := c.Approve(ctx, pr, ""); err != nil || !changed {
		t.Fatalf("approve: %v %v", changed, err)
	}
	if changed, err := c.Approve(ctx, pr, ""); err != nil || changed {
		t.Errorf("already approved: %v %v", changed, err)
	}
	if changed, err := c.Unapprove(ctx, pr, ""); err != nil || !changed {
		t.Errorf("unapprove: %v %v", changed, err)
	}
	if changed, err := c.Unapprove(ctx, pr, ""); err != nil || changed || !f.did("DELETE /repositories/ws/r/pullrequests/5/approve") {
		t.Errorf("nothing to withdraw: %v %v", changed, err)
	}
}
