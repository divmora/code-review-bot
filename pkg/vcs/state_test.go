package vcs

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPRStateGitLab(t *testing.T) {
	for in, want := range map[string]string{"opened": StateOpen, "locked": StateOpen, "closed": StateClosed, "merged": StateMerged} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, `{"title":"t","state":"`+in+`","sha":"abc","diff_refs":{"base_sha":"b","head_sha":"abc","start_sha":"b"}}`)
		}))
		pr, err := NewGitLab(srv.URL, "t", false).GetPR(context.Background(), Ref{Platform: GitLab, Host: "h", Repo: "g/p", Number: 1})
		srv.Close()
		if err != nil || pr.State != want {
			t.Errorf("gitlab %s: got %q %v, want %q", in, pr.State, err, want)
		}
	}
}

func TestPRStateBitbucket(t *testing.T) {
	for in, want := range map[string]string{"OPEN": StateOpen, "MERGED": StateMerged, "DECLINED": StateClosed, "SUPERSEDED": StateClosed} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "/commit/") {
				_, _ = io.WriteString(w, `{"hash":"0123456789abcdef0123456789abcdef01234567"}`)
				return
			}
			_, _ = io.WriteString(w, `{"title":"t","state":"`+in+`","source":{"branch":{"name":"f"},"commit":{"hash":"0123456"}},"destination":{"branch":{"name":"main"},"commit":{"hash":"0123456"}}}`)
		}))
		pr, err := NewBitbucket(srv.URL, BitbucketAuth{Token: "t"}).GetPR(context.Background(), Ref{Platform: Bitbucket, Host: "bitbucket.org", Repo: "ws/r", Number: 1})
		srv.Close()
		if err != nil || pr.State != want {
			t.Errorf("bitbucket %s: got %+v %v, want %q", in, pr, err, want)
		}
	}
}
