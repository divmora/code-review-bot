package review

import (
	"regexp"
	"strings"

	"github.com/divmora/code-review-bot/pkg/diff"
)

// secretRules are high-precision patterns for credentials. They run on
// added lines without any LLM call, so they work even if the model fails.
var secretRules = []struct {
	name string
	re   *regexp.Regexp
}{
	{"AWS access key ID", regexp.MustCompile(`\b(?:AKIA|ASIA|ABIA|ACCA)[0-9A-Z]{16}\b`)},
	{"GitHub token", regexp.MustCompile(`\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{36,}\b|\bgithub_pat_[A-Za-z0-9_]{60,}\b`)},
	{"GitLab token", regexp.MustCompile(`\bglpat-[A-Za-z0-9_\-]{20,}\b`)},
	{"Slack token", regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}\b`)},
	{"Stripe live key", regexp.MustCompile(`\b[rs]k_live_[A-Za-z0-9]{20,}\b`)},
	{"Google API key", regexp.MustCompile(`\bAIza[0-9A-Za-z_\-]{35}\b`)},
	{"OpenAI API key", regexp.MustCompile(`\bsk-(?:proj-|svcacct-)?[A-Za-z0-9_\-]{32,}\b`)},
	{"Anthropic API key", regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_\-]{32,}\b`)},
	{"private key", regexp.MustCompile(`-----BEGIN (?:RSA |EC |DSA |OPENSSH |PGP |ENCRYPTED )?PRIVATE KEY( BLOCK)?-----`)},
	{"JWT", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`)},
}

func scanSecrets(f *diff.File) []Finding {
	var out []Finding
	for _, h := range f.Hunks {
		for _, l := range h.Lines {
			if l.Kind != diff.Add || strings.Contains(l.Text, "codereview:allow") {
				continue
			}
			for _, r := range secretRules {
				if r.re.MatchString(l.Text) {
					out = append(out, Finding{
						Path: f.Path, OldPath: f.OldPath, StartLine: l.NewNo, EndLine: l.NewNo,
						Severity: Critical, Category: "security", Confidence: 1, Source: "secret-scan",
						Title: "Possible hard-coded " + r.name,
						Body: "This line appears to contain a credential (" + r.name + "). Committed secrets stay in git history even after removal. " +
							"Revoke and rotate the credential, then load it from a secret manager or environment variable.",
						lineText: l.Text,
					})
					break
				}
			}
		}
	}
	return out
}
