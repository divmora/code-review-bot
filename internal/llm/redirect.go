package llm

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// sameHostRedirects refuses a redirect to another host, or from https to
// http. API keys go in custom headers (x-api-key, x-goog-api-key), which
// Go carries along on redirects, so a redirect elsewhere could collect
// the key (code-review-bot#23).
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
