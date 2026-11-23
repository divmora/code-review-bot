package vcs

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"
)

// Signed markers: with a state key, the bot signs the markers it reads
// back (the summary's reviewed commit and state, an inline comment's
// fingerprint), bound to the repository and PR. A signed comment counts
// as the bot's even when an earlier token user wrote it, so switching the
// token to a new user (a new GitLab project access token, say) keeps the
// context. Nobody without the key can sign, and a signature is only valid
// on its own PR. Without a key, only the token user's comments count.

const sigPrefix = "<!-- codereview:sig="

var (
	sigRe      = regexp.MustCompile(`<!-- codereview:sig=([a-f0-9]{64}) -->`)
	stateRawRe = regexp.MustCompile(`<!-- codereview:state=([A-Za-z0-9+/=]+) -->`)
)

// StateKeyer is a client that can sign the bot's markers.
type StateKeyer interface {
	// SetStateKey sets the key (nil or empty: don't sign, trust only the
	// token user's comments).
	SetStateKey(key []byte)
}

// stateKey is embedded in each client.
type stateKey struct{ key []byte }

func (k *stateKey) SetStateKey(key []byte) { k.key = append([]byte(nil), key...) }

// signedParts are the marker values a body's signature covers, and its
// kind; ok is false for a body with nothing to sign.
func signedParts(body string) (kind string, parts []string, ok bool) {
	if isSummary(body) {
		state := ""
		if m := stateRawRe.FindStringSubmatch(body); m != nil {
			state = m[1]
		}
		return "summary", []string{reviewedSHA(body), state}, true
	}
	if m := fpRe.FindStringSubmatch(body); m != nil {
		return "fp", []string{m[1]}, true
	}
	return "", nil, false
}

func (k stateKey) mac(ref Ref, kind string, parts []string) string {
	h := hmac.New(sha256.New, k.key)
	h.Write([]byte("codereview-markers-v1"))
	for _, p := range append([]string{strings.ToLower(ref.Repo), strconv.Itoa(ref.Number), kind}, parts...) {
		h.Write([]byte{0})
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// sign appends the signature of body's markers (no key or nothing to
// sign: body as is).
func (k stateKey) sign(ref Ref, body string) string {
	if len(k.key) == 0 {
		return body
	}
	kind, parts, ok := signedParts(body)
	if !ok {
		return body
	}
	body = sigRe.ReplaceAllString(body, "")
	return strings.TrimRight(body, "\n") + "\n" + sigPrefix + k.mac(ref, kind, parts) + " -->\n"
}

// signed reports whether body carries a valid signature for ref.
func (k stateKey) signed(ref Ref, body string) bool {
	if len(k.key) == 0 {
		return false
	}
	m := sigRe.FindStringSubmatch(body)
	kind, parts, ok := signedParts(body)
	if m == nil || !ok {
		return false
	}
	return hmac.Equal([]byte(m[1]), []byte(k.mac(ref, kind, parts)))
}

// summaryPick chooses the bot's summary among the candidates: the latest
// the token user wrote (it can edit it), else the latest signed one (its
// state is read, and a new summary is posted).
type summaryPick struct {
	id, body string
	own      bool
	found    bool
}

func (p *summaryPick) offer(id, body string, own bool) {
	if own || !p.own {
		*p = summaryPick{id: id, body: body, own: own, found: true}
	}
}

func (p summaryPick) apply(st *BotState) {
	if !p.found {
		return
	}
	parseSummary(p.id, p.body, st)
	if !p.own {
		st.SummaryID = "" // an earlier user's note: read it, don't edit it
	}
}
