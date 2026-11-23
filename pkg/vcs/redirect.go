package vcs

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// sameHostRedirects refuses a redirect to another host, or from https to
// http: credentials are set per request and Go would carry custom headers
// such as GitLab's PRIVATE-TOKEN along (it drops only Authorization), so
// a redirecting proxy or server could otherwise collect the token (issue
// #23). Redirects within the host (a renamed repository) still work.
func sameHostRedirects(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	from := via[0].URL
	switch {
	case !strings.EqualFold(req.URL.Host, from.Host):
		return fmt.Errorf("refusing a redirect from %s to another host (%s)", from.Host, req.URL.Host)
	case from.Scheme == "https" && req.URL.Scheme != "https":
		return fmt.Errorf("refusing a redirect from https to %s on %s", req.URL.Scheme, req.URL.Host)
	}
	return nil
}
