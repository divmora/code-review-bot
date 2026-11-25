// Package config loads the engine config (models, code host credentials,
// review limits) and the per-repository review config (.codereview.yaml).
package config

import (
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/divmora/code-review-bot/internal/llm"
)

// Config is the operator config shared by the CLI and the server.
type Config struct {
	// Model is a profile name from Models or a "provider:model" spec.
	Model string `yaml:"model"`
	// StateKey (env CODEREVIEW_STATE_KEY only, never the file) signs the
	// markers the bot reads back from its comments, so its comments from
	// an earlier token user still count. Empty: only the token user's do.
	StateKey string `yaml:"-"`
	// Fallback models tried in order when Model fails.
	Fallback []string `yaml:"fallback"`
	// Models are named profiles. Switch provider by changing Model.
	Models map[string]llm.ModelConfig `yaml:"models"`
	// AllowRepoModel lets a repository's .codereview.yaml pick one of the
	// named profiles. Specs (provider:model) from repos are never honoured.
	AllowRepoModel bool `yaml:"allow_repo_model"`

	Review    Review    `yaml:"review"`
	GitHub    GitHub    `yaml:"github"`
	GitLab    []GitLab  `yaml:"gitlab"`
	Bitbucket Bitbucket `yaml:"bitbucket"`
	// BotName is the mention handle for chat commands (@<bot_name> review).
	// Empty: the user the code host token belongs to (e.g. @gitlab-bot),
	// or "codereview" when that cannot be read.
	BotName string `yaml:"bot_name"`
}

// Review holds engine limits and the defaults for repository config.
type Review struct {
	Defaults RepoConfig `yaml:"defaults"`
	// Concurrency is the number of parallel LLM calls per review.
	Concurrency int `yaml:"concurrency"`
	// BatchTokens caps the estimated prompt tokens per LLM call.
	BatchTokens int `yaml:"batch_tokens"`
	// MaxFiles skips reviews of PRs with more reviewable files than this.
	MaxFiles int `yaml:"max_files"`
	// FullFileMaxLines: files up to this size are sent whole for context;
	// larger files are sent as hunks only.
	FullFileMaxLines int `yaml:"full_file_max_lines"`
	// ContextLines: for larger files, this many lines around each change
	// are sent as numbered context excerpts.
	ContextLines int `yaml:"context_lines"`
	// Verify runs a second, skeptical model pass over the findings that drops
	// false positives, turns data-dependent ones into questions and may add
	// clearly missed major issues. Default true; costs one extra call.
	Verify *bool `yaml:"verify"`
	// VerifyModel is the model for the verification pass: a profile name or
	// provider:model. Empty uses the review model. A different model (or
	// provider) catches more of the reviewer's mistakes.
	VerifyModel string `yaml:"verify_model"`
	// VerifyCanDrop limits what the verifier may remove. "confident"
	// (default): minor and nitpick findings, plus major or critical ones it
	// calls speculative (not a problem in the current code) or is at least
	// 80% sure are false; other disputed ones become questions. "minor":
	// only minor and nitpick. "all": anything it disputes.
	VerifyCanDrop string `yaml:"verify_can_drop"`
	// VerifyCanAdd lets the verifier add findings it thinks the reviewer
	// missed. Off by default: additions skip verification themselves, and a
	// real run posted a false "command injection" that way.
	VerifyCanAdd bool `yaml:"verify_can_add"`
	// Passes runs the reviewer this many times per batch (1 to 3) and keeps
	// the union of the findings, merged and then verified. Models vary from
	// run to run; a second pass catches issues one run misses, at the cost
	// of one more reviewer call per batch. Default 1.
	Passes int `yaml:"passes"`
	// AnchorScores starts each review's scores from the previous review's,
	// so they change only when the change does (default on; --full always
	// scores from scratch).
	AnchorScores *bool `yaml:"anchor_scores"`
	// RepoConfigFile is the per-repo config path, read from the PR's base
	// commit so a PR cannot change the rules it is reviewed under.
	RepoConfigFile string `yaml:"repo_config_file"`
	// Approve sets --approve and its flags from the config; flags win.
	// Operator config only (issue #40).
	Approve Approve `yaml:"approve"`
	// Commands lists the @bot chat commands in the summary footer. Set it
	// only when a webhook service (such as Divmora Code Review Console)
	// answers them: the CLI runs once and can't. Operator config only
	// (issue #47).
	Commands bool `yaml:"commands"`
}

// GitHub configures github.com or GitHub Enterprise Server.
type GitHub struct {
	APIURL string `yaml:"api_url"` // default https://api.github.com
	// Host is the web host of the PR URLs this API serves, when it can't
	// be told from api_url (an API reached through a proxy or an internal
	// name). Default: github.com for api.github.com, <name>.ghe.com for
	// api.<name>.ghe.com, else api_url's host (Enterprise Server's
	// https://<host>/api/v3). The token is only used for PRs on this host.
	Host string `yaml:"host"`
	// Token is a PAT or fine-grained token (CLI / simple server setups).
	Token string `yaml:"token"`
	// GitHub App credentials (recommended for the server).
	AppID          int64  `yaml:"app_id"`
	PrivateKeyFile string `yaml:"private_key_file"`
	PrivateKey     string `yaml:"private_key"`
	WebhookSecret  string `yaml:"webhook_secret"`
}

// Bitbucket configures Bitbucket Cloud (bitbucket.org). Use an access token
// (repository, project or workspace; Bearer) or an Atlassian API token with
// the account email (Basic). App passwords are being retired by Atlassian.
type Bitbucket struct {
	APIURL   string `yaml:"api_url"` // default https://api.bitbucket.org/2.0
	Token    string `yaml:"token"`
	Email    string `yaml:"email"`
	APIToken string `yaml:"api_token"`
}

// GitLab configures one GitLab instance (gitlab.com or self-managed).
type GitLab struct {
	BaseURL string `yaml:"base_url"` // e.g. https://gitlab.example.com
	// Host is the web host of the MR URLs, when the API is reached under
	// another name (an internal URL); default base_url's host. The token
	// is only used for MRs on base_url's host or this one.
	Host          string `yaml:"host"`
	Token         string `yaml:"token"`
	WebhookSecret string `yaml:"webhook_secret"`
	SkipTLSVerify bool   `yaml:"skip_tls_verify"`
}

var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// Load reads a config file. An empty path or a missing default file
// yields defaults, so the CLI works with env vars alone.
func Load(path string) (*Config, error) {
	name, data, err := Source(path)
	if err != nil {
		return nil, err
	}
	cfg := &Config{}
	if len(data) > 0 {
		if err := yaml.Unmarshal(ExpandEnv(data), cfg); err != nil {
			return nil, fmt.Errorf("parse %s: %w", sourceLabel(name), err)
		}
	}
	cfg.Finalize()
	return cfg, nil
}

// InlineEnv holds the whole config as YAML. Executors (containers,
// Kubernetes Jobs) use it instead of a file.
const InlineEnv = "CODEREVIEW_CONFIG_YAML"

// Source returns the raw config Load reads and where it came from, the
// first of: path (--config), $CODEREVIEW_CONFIG (a path),
// $CODEREVIEW_CONFIG_YAML (the YAML itself), then ./codereview.yaml or
// ./codereview.yml. The explicit sources come first: in CI the working
// directory can be the PR's checkout, whose codereview.yaml must not
// replace the operator's config (issue #49). No config at all returns
// empty data and no error.
func Source(path string) (string, []byte, error) {
	explicit := path != ""
	if path == "" {
		path = os.Getenv("CODEREVIEW_CONFIG")
		explicit = path != ""
	}
	if path == "" {
		if v := os.Getenv(InlineEnv); v != "" {
			if f := cwdConfig(); f != "" {
				slog.Debug("using the config in "+InlineEnv+", not the file in the working directory", "skipped", f)
			}
			return InlineEnv, []byte(v), nil
		}
		if path = cwdConfig(); path == "" {
			return "", nil, nil
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if explicit || !os.IsNotExist(err) {
			return path, nil, fmt.Errorf("read config: %w", err)
		}
		return "", nil, nil
	}
	return path, data, nil
}

// cwdConfig is ./codereview.yaml or ./codereview.yml when one exists.
func cwdConfig() string {
	for _, p := range []string{"codereview.yaml", "codereview.yml"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func sourceLabel(name string) string {
	if name == InlineEnv {
		return name
	}
	return "config " + name
}

// ExpandEnv replaces ${VAR} references with environment values. Only the
// ${VAR} form is expanded, so "$" elsewhere stays literal.
func ExpandEnv(data []byte) []byte {
	return []byte(envRef.ReplaceAllStringFunc(string(data), func(m string) string {
		return os.Getenv(envRef.FindStringSubmatch(m)[1])
	}))
}

// Finalize applies environment overrides and defaults. Load calls it;
// programs that embed Config in their own config file call it themselves.
func (c *Config) Finalize() {
	c.applyEnv()
	c.applyDefaults()
}

func (c *Config) applyEnv() {
	c.StateKey = os.Getenv("CODEREVIEW_STATE_KEY")
	if v := os.Getenv("CODEREVIEW_MODEL"); v != "" {
		c.Model = v
	}
	if v := os.Getenv("CODEREVIEW_FALLBACK"); v != "" {
		c.Fallback = splitList(v)
	}
	if c.GitHub.Token == "" {
		c.GitHub.Token = os.Getenv("GITHUB_TOKEN")
	}
	if c.Bitbucket.Token == "" {
		c.Bitbucket.Token = os.Getenv("BITBUCKET_TOKEN")
	}
	if c.Bitbucket.Email == "" {
		c.Bitbucket.Email = os.Getenv("BITBUCKET_EMAIL")
	}
	if c.Bitbucket.APIToken == "" {
		c.Bitbucket.APIToken = os.Getenv("BITBUCKET_API_TOKEN")
	}
	if c.GitHub.WebhookSecret == "" {
		c.GitHub.WebhookSecret = os.Getenv("GITHUB_WEBHOOK_SECRET")
	}
	if c.GitHub.APIURL == "" {
		c.GitHub.APIURL = os.Getenv("GITHUB_API_URL") // set inside GitHub Actions
	}
	if len(c.GitLab) == 0 && (os.Getenv("GITLAB_TOKEN") != "" || os.Getenv("CI_JOB_TOKEN") != "") {
		c.GitLab = []GitLab{{
			BaseURL:       firstNonEmpty(os.Getenv("GITLAB_URL"), os.Getenv("CI_SERVER_URL"), "https://gitlab.com"),
			Token:         os.Getenv("GITLAB_TOKEN"),
			WebhookSecret: os.Getenv("GITLAB_WEBHOOK_SECRET"),
		}}
	}
}

func (c *Config) applyDefaults() {
	if c.GitHub.APIURL == "" {
		c.GitHub.APIURL = "https://api.github.com"
	}
	for i := range c.GitLab {
		if c.GitLab[i].BaseURL == "" {
			c.GitLab[i].BaseURL = "https://gitlab.com"
		}
		c.GitLab[i].BaseURL = strings.TrimRight(c.GitLab[i].BaseURL, "/")
	}
	r := &c.Review
	if r.Concurrency <= 0 {
		r.Concurrency = 4
	}
	if r.BatchTokens <= 0 {
		r.BatchTokens = 40000
	}
	if r.MaxFiles <= 0 {
		r.MaxFiles = 150
	}
	if r.FullFileMaxLines <= 0 {
		r.FullFileMaxLines = 800
	}
	if r.ContextLines <= 0 {
		r.ContextLines = 60
	}
	if r.RepoConfigFile == "" {
		r.RepoConfigFile = ".codereview.yaml"
	}
	r.Defaults.fillDefaults()
}

// ResolveModel turns a profile name or spec into a model config.
func (c *Config) ResolveModel(ref string) (llm.ModelConfig, error) {
	if m, ok := c.Models[ref]; ok {
		if m.Provider == "" || m.Model == "" {
			return m, fmt.Errorf("model profile %q needs provider and model", ref)
		}
		return m, nil
	}
	m, err := llm.ParseSpec(ref)
	if err != nil {
		return m, fmt.Errorf("%q is neither a model profile (%s) nor a provider:model spec: %w", ref, strings.Join(c.profileNames(), ", "), err)
	}
	return m, nil
}

// ModelChain returns the primary model (override wins over config) and
// the fallbacks, ready for llm.NewChain.
func (c *Config) ModelChain(override string) ([]llm.ModelConfig, error) {
	primary := firstNonEmpty(override, c.Model)
	if primary == "" {
		if len(c.Models) == 1 {
			for name := range c.Models {
				primary = name
			}
		} else {
			return nil, fmt.Errorf("no model selected: pass --model, set CODEREVIEW_MODEL, or set `model:` in the config (e.g. openai:<model>, anthropic:<model>, gemini:<model>, bedrock:<model-id>)")
		}
	}
	var out []llm.ModelConfig
	for _, ref := range append([]string{primary}, c.Fallback...) {
		m, err := c.ResolveModel(ref)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

func (c *Config) profileNames() []string {
	var out []string
	for k := range c.Models {
		out = append(out, k)
	}
	return out
}

// GitLabFor finds the instance config whose host matches.
func (c *Config) GitLabFor(host string) (GitLab, bool) {
	for _, g := range c.GitLab {
		if hostOf(g.BaseURL) == strings.ToLower(host) || g.Host != "" && strings.EqualFold(g.Host, host) {
			return g, true
		}
	}
	return GitLab{}, false
}

func hostOf(u string) string {
	u = strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	if i := strings.IndexByte(u, '/'); i >= 0 {
		u = u[:i]
	}
	return strings.ToLower(u)
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
