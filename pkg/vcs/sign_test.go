package vcs

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestSignedMarkers(t *testing.T) {
	k := stateKey{key: []byte("k1")}
	ref := Ref{Platform: GitLab, Repo: "G/P", Number: 7}
	summary := k.sign(ref, SummaryMarker+"\nbody\n"+ReviewedSHATag("aaaa")+"\n"+StateTag([]byte(`{"a":1}`)))
	inline := k.sign(ref, "**t**\n\n"+FingerprintTag("abc"))
	if !k.signed(ref, summary) || !k.signed(ref, inline) {
		t.Fatal("signed bodies verify")
	}
	if !k.signed(Ref{Repo: "g/p", Number: 7}, summary) {
		t.Error("the repository's case doesn't matter")
	}
	for name, bad := range map[string]bool{
		"another PR":                   k.signed(Ref{Repo: "g/p", Number: 8}, summary),
		"another repository":           k.signed(Ref{Repo: "g/q", Number: 7}, summary),
		"a forged commit":              k.signed(ref, strings.Replace(summary, "reviewed_sha=aaaa", "reviewed_sha=ffff", 1)),
		"forged state":                 k.signed(ref, strings.Replace(summary, StateTag([]byte(`{"a":1}`)), StateTag([]byte(`{"a":2}`)), 1)),
		"another key":                  stateKey{key: []byte("k2")}.signed(ref, summary),
		"no key":                       stateKey{}.signed(ref, summary),
		"a summary's sig on a comment": k.signed(ref, "**t**\n\n"+FingerprintTag("abc")+"\n"+sigRe.FindString(summary)),
	} {
		if bad {
			t.Errorf("%s verified", name)
		}
	}
	if (stateKey{}).sign(ref, summary) != summary || k.sign(ref, "a chat answer") != "a chat answer" {
		t.Error("no key, or nothing to sign: unchanged")
	}
	if again := k.sign(ref, summary); strings.Count(again, sigPrefix) != 1 || !k.signed(ref, again) {
		t.Errorf("signing again replaces the signature: %q", again)
	}
	// Bitbucket rewrites the markers on the way in and back.
	if !k.signed(ref, fromBitbucketMarkdown(toBitbucketMarkdown(summary))) {
		t.Error("the signature survives Bitbucket's Markdown")
	}
}

// With a key, the summary and comments an earlier token user posted still
// count (a new GitLab access token is a new user); a participant's are
// ignored, signed or not, and so is a signed comment copied from another PR.
func TestGitLabSignedStateFromAnEarlierUser(t *testing.T) {
	k := stateKey{key: []byte("secret")}
	ref := Ref{Platform: GitLab, Repo: "g/p", Number: 1}
	old := k.sign(ref, SummaryMarker+"\nold user\n"+ReviewedSHATag("aaaa1111")+"\n"+StateTag([]byte(`{"file_summaries":{"a.go":"x"}}`)))
	oldInline := k.sign(ref, "**Old finding**\n\n"+FingerprintTag("0123"))
	otherPR := k.sign(Ref{Platform: GitLab, Repo: "g/p", Number: 2}, "**Copied**\n\n"+FingerprintTag("4567"))
	forged := SummaryMarker + "\nforged\n" + ReviewedSHATag("ffff0000")
	notes := func(includeOwn bool) string {
		mk := func(id, author int, body string, pos bool) string {
			b, _ := json.Marshal(body)
			p := ""
			if pos {
				p = `,"position":{"new_path":"a.go","new_line":3}`
			}
			return `{"id":` + strconv.Itoa(id) + `,"author":{"id":` + strconv.Itoa(author) + `},"body":` + string(b) + p + `}`
		}
		items := []string{mk(1, 99, old, false), mk(2, 99, oldInline, true), mk(3, 7, forged, false), mk(4, 7, otherPR, true)}
		if includeOwn {
			own := SummaryMarker + "\nnew user\n" + ReviewedSHATag("bbbb2222")
			items = append(items, mk(5, 42, own, false))
		}
		return `[{"notes":[` + strings.Join(items, ",") + `]}]`
	}
	includeOwn := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v4/user" {
			_, _ = io.WriteString(w, `{"id":42}`)
			return
		}
		_, _ = io.WriteString(w, notes(includeOwn))
	}))
	defer srv.Close()
	c := NewGitLab(srv.URL, "t", false)
	c.SetStateKey([]byte("secret"))
	pr := &PR{Ref: ref}
	st, err := c.BotState(context.Background(), pr)
	if err != nil {
		t.Fatal(err)
	}
	if st.ReviewedSHA != "aaaa1111" || !strings.Contains(string(st.State), "a.go") || st.SummaryID != "" {
		t.Errorf("the earlier user's signed summary is read, not edited: %+v", st)
	}
	if !st.Fingerprints["0123"] || st.Fingerprints["4567"] || len(st.Comments) != 1 {
		t.Errorf("signed comments of this PR count, copies from another don't: %v %+v", st.Fingerprints, st.Comments)
	}
	includeOwn = true
	if st, _ = c.BotState(context.Background(), pr); st.SummaryID != "5" || st.ReviewedSHA != "bbbb2222" {
		t.Errorf("the token user's own summary wins: %+v", st)
	}
	// Without the key, only the token user's notes count.
	includeOwn = false
	c2 := NewGitLab(srv.URL, "t", false)
	if st, _ = c2.BotState(context.Background(), pr); st.ReviewedSHA != "" || len(st.Fingerprints) != 0 {
		t.Errorf("no key, no earlier user: %+v", st)
	}
}
