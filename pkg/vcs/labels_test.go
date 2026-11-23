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

var testScores = []ScoreLabel{{"Quality", 85, "2da44e"}, {"Security", 100, "2da44e"}, {"Maintainability", 70, "fb8c00"}}

func TestGitLabScoreLabels(t *testing.T) {
	var mu sync.Mutex
	var created []map[string]string
	var put map[string]string
	var recolored []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		body, _ := io.ReadAll(r.Body)
		switch {
		case r.Method == "GET" && r.URL.EscapedPath() == "/api/v4/projects/g%2Fp/labels/Security%20Score::100":
			_, _ = io.WriteString(w, `{"name":"Security Score::100","color":"#6699cc","is_project_label":true}`) // made earlier, not green
		case r.Method == "PUT" && strings.Contains(r.URL.Path, "/labels/"):
			recolored = append(recolored, r.URL.Path+" "+string(body))
			_, _ = io.WriteString(w, `{}`)
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/merge_requests/7"):
			_, _ = io.WriteString(w, `{"labels":["bug","Quality Score::60","Security Score::100","Maintainability Score::90"]}`)
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/labels"):
			var m map[string]string
			_ = json.Unmarshal(body, &m)
			created = append(created, m)
			if m["name"] == "Security Score::100" {
				w.WriteHeader(409) // already exists
				return
			}
			_, _ = io.WriteString(w, `{}`)
		case r.Method == "PUT" && strings.HasSuffix(r.URL.Path, "/merge_requests/7"):
			_ = json.Unmarshal(body, &put)
			_, _ = io.WriteString(w, `{}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
		}
	}))
	defer srv.Close()
	c := NewGitLab(srv.URL, "t", false)
	pr := &PR{Ref: Ref{Platform: GitLab, Repo: "g/p", Number: 7}}
	if err := c.SetScoreLabels(context.Background(), pr, testScores); err != nil {
		t.Fatal(err)
	}
	if put["add_labels"] != "Quality Score::85,Security Score::100,Maintainability Score::70" {
		t.Errorf("add = %q", put["add_labels"])
	}
	if put["remove_labels"] != "Quality Score::60,Maintainability Score::90" {
		t.Errorf("remove = %q (unchanged and unrelated labels must stay)", put["remove_labels"])
	}
	if len(created) != 3 || created[2]["color"] != "#fb8c00" {
		t.Errorf("created = %v", created)
	}
	if len(recolored) != 1 || !strings.Contains(recolored[0], "Security Score::100") || !strings.Contains(recolored[0], `"#2da44e"`) {
		t.Errorf("an existing label in another colour gets the band colour: %v", recolored)
	}
}

func TestGitHubScoreLabels(t *testing.T) {
	var mu sync.Mutex
	var deleted []string
	var added []string
	var patched []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		body, _ := io.ReadAll(r.Body)
		switch {
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/repos/o/r/labels/"):
			color := "2DA44E" // already green (case aside)
			if strings.HasSuffix(r.URL.Path, "Maintainability Score: 70") {
				color = "ededed"
			}
			_, _ = io.WriteString(w, `{"color":"`+color+`"}`)
		case r.Method == "PATCH" && strings.HasPrefix(r.URL.Path, "/repos/o/r/labels/"):
			patched = append(patched, r.URL.Path+" "+string(body))
			_, _ = io.WriteString(w, `{}`)
		case r.Method == "GET" && r.URL.Path == "/repos/o/r/issues/7/labels":
			_, _ = io.WriteString(w, `[{"name":"bug"},{"name":"Quality Score: 60"},{"name":"Security Score: 100"}]`)
		case r.Method == "DELETE":
			deleted = append(deleted, r.URL.Path)
			_, _ = io.WriteString(w, `[]`)
		case r.Method == "POST" && r.URL.Path == "/repos/o/r/labels":
			w.WriteHeader(422) // exists
		case r.Method == "POST" && r.URL.Path == "/repos/o/r/issues/7/labels":
			var m struct{ Labels []string }
			_ = json.Unmarshal(body, &m)
			added = m.Labels
			_, _ = io.WriteString(w, `[]`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
		}
	}))
	defer srv.Close()
	c := NewGitHub(srv.URL, StaticToken("t"))
	if err := c.SetScoreLabels(context.Background(), &PR{Ref: Ref{Repo: "o/r", Number: 7}}, testScores); err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 1 || deleted[0] != "/repos/o/r/issues/7/labels/Quality Score: 60" {
		t.Errorf("deleted = %v", deleted)
	}
	if strings.Join(added, "|") != "Quality Score: 85|Security Score: 100|Maintainability Score: 70" {
		t.Errorf("added = %v", added)
	}
	if len(patched) != 1 || !strings.Contains(patched[0], "Maintainability Score: 70") || !strings.Contains(patched[0], `"fb8c00"`) {
		t.Errorf("only the label in another colour is recoloured: %v", patched)
	}
}

func TestCommitStatus(t *testing.T) {
	var mu sync.Mutex
	var got []map[string]string
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var m map[string]string
		_ = json.NewDecoder(r.Body).Decode(&m)
		got = append(got, m)
		paths = append(paths, r.URL.Path)
		_, _ = io.WriteString(w, `{}`)
	}))
	defer srv.Close()
	long := strings.Repeat("x", 200)
	gl := NewGitLab(srv.URL, "t", false)
	_ = gl.SetCommitStatus(context.Background(), &PR{Ref: Ref{Repo: "g/p", Number: 1}}, "abc", CommitStatus{State: StatusRunning, Description: long, TargetURL: "https://x/mr/1"})
	gh := NewGitHub(srv.URL, StaticToken("t"))
	_ = gh.SetCommitStatus(context.Background(), &PR{Ref: Ref{Repo: "o/r", Number: 1}}, "abc", CommitStatus{State: StatusFailed, Description: "Review failed"})
	if paths[0] != "/api/v4/projects/g/p/statuses/abc" || got[0]["state"] != "running" || got[0]["name"] != "codereview" || len(got[0]["description"]) != 140 || got[0]["target_url"] != "https://x/mr/1" {
		t.Errorf("gitlab status = %s %v", paths[0], got[0])
	}
	if paths[1] != "/repos/o/r/statuses/abc" || got[1]["state"] != "failure" || got[1]["context"] != "codereview" {
		t.Errorf("github status = %s %v", paths[1], got[1])
	}
	if err := gh.SetCommitStatus(context.Background(), &PR{}, "abc", CommitStatus{State: "bogus"}); err == nil {
		t.Error("unknown state should fail")
	}
}

func TestBotStateReadsPostedComments(t *testing.T) {
	body := "_⚠️ Potential issue_ | _🟠 Major_\n\n**Enforce the credit-limit rule**\n\nDetails.\n\n<!-- codereview:fp=abc123 -->"
	qbody := "_❓ Question_ | _🟡 Minor_\n\n**Keep badges off content**\n\n<!-- codereview:fp=def456 -->"
	jb, _ := json.Marshal(body)
	jq, _ := json.Marshal(qbody)
	gl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `[{"notes":[{"id":1,"body":`+string(jb)+`,"position":{"new_path":"a/E.php","new_line":289}},
			{"id":2,"body":`+string(jq)+`,"position":{"new_path":"a/P.php","new_line":261}},
			{"id":3,"body":"a human comment","position":{"new_path":"a/E.php","new_line":10}}]}]`)
	}))
	defer gl.Close()
	st, err := NewGitLab(gl.URL, "t", false).BotState(context.Background(), &PR{Ref: Ref{Repo: "g/p", Number: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Comments) != 2 || st.Comments[0].Title != "Enforce the credit-limit rule" || st.Comments[0].Line != 289 || st.Comments[0].Question || !st.Comments[1].Question {
		t.Errorf("gitlab comments = %+v", st.Comments)
	}

	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/issues/") {
			_, _ = io.WriteString(w, `[]`)
			return
		}
		// An outdated comment has line null but original_line set.
		_, _ = io.WriteString(w, `[{"body":`+string(jb)+`,"path":"E.php","line":null,"original_line":288}]`)
	}))
	defer gh.Close()
	st, err = NewGitHub(gh.URL, StaticToken("t")).BotState(context.Background(), &PR{Ref: Ref{Repo: "o/r", Number: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Comments) != 1 || st.Comments[0].Line != 288 || st.Comments[0].Path != "E.php" {
		t.Errorf("github comments = %+v", st.Comments)
	}
}
