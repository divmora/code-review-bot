package vcs

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// GitHubApp mints installation tokens for a GitHub App.
type GitHubApp struct {
	AppID  int64
	key    *rsa.PrivateKey
	apiURL string
	hc     *http.Client

	mu     sync.Mutex
	tokens map[int64]cachedToken
}

type cachedToken struct {
	token   string
	expires time.Time
}

// NewGitHubApp parses a PEM private key (PKCS#1 or PKCS#8).
func NewGitHubApp(appID int64, pemKey []byte, apiURL string) (*GitHubApp, error) {
	block, _ := pem.Decode(pemKey)
	if block == nil {
		return nil, errors.New("github app: private key is not PEM")
	}
	var key *rsa.PrivateKey
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		key = k
	} else if k8, err8 := x509.ParsePKCS8PrivateKey(block.Bytes); err8 == nil {
		rk, ok := k8.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("github app: private key is not RSA")
		}
		key = rk
	} else {
		return nil, fmt.Errorf("github app: parse private key: %w", err)
	}
	return &GitHubApp{
		AppID: appID, key: key, apiURL: strings.TrimRight(apiURL, "/"),
		hc: &http.Client{Timeout: 30 * time.Second, CheckRedirect: sameHostRedirects}, tokens: map[int64]cachedToken{},
	}, nil
}

// JWT returns an app JWT valid for ~9 minutes.
func (a *GitHubApp) JWT(now time.Time) (string, error) {
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, _ := json.Marshal(map[string]any{
		"iat": now.Add(-60 * time.Second).Unix(), // clock skew allowance
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": fmt.Sprint(a.AppID),
	})
	signing := header + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, a.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + enc.EncodeToString(sig), nil
}

// Installation returns a TokenSource for one installation.
func (a *GitHubApp) Installation(id int64) TokenSource { return installationSource{a, id} }

type installationSource struct {
	app *GitHubApp
	id  int64
}

func (s installationSource) Token(ctx context.Context) (string, error) {
	return s.app.installationToken(ctx, s.id)
}

func (a *GitHubApp) installationToken(ctx context.Context, id int64) (string, error) {
	a.mu.Lock()
	if t, ok := a.tokens[id]; ok && time.Until(t.expires) > 5*time.Minute {
		a.mu.Unlock()
		return t.token, nil
	}
	a.mu.Unlock()

	jwt, err := a.JWT(time.Now())
	if err != nil {
		return "", err
	}
	r := &rest{
		base: a.apiURL, hc: a.hc, name: "github-app", accept: "application/vnd.github+json",
		auth: func(_ context.Context, req *http.Request) error {
			req.Header.Set("Authorization", "Bearer "+jwt)
			return nil
		},
	}
	var v struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if _, err := r.do(ctx, "POST", fmt.Sprintf("/app/installations/%d/access_tokens", id), nil, &v); err != nil {
		return "", fmt.Errorf("installation token: %w", err)
	}
	a.mu.Lock()
	a.tokens[id] = cachedToken{v.Token, v.ExpiresAt}
	a.mu.Unlock()
	return v.Token, nil
}
