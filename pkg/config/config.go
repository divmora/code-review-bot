// Package config loads the engine config (models, code host credentials,
// review limits) and the per-repository review config (.codereview.yaml).
package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/divmora/code-review-bot/pkg/llm"
)

// Config is the operator config shared by the CLI and the server.
type Config struct {
	// Model is a profile name from Models or a "provider:model" spec.
	Model string `yaml:"model"`
	// Fallback models tried in order when Model fails.
	Fallback []string `yaml:"fallback"`
	// Models are named profiles. Switch provider by changing Model.
	Models map[string]llm.ModelConfig `yaml:"models"`
	// AllowRepoModel lets a repository's .codereview.yaml pick one of the
	// named profiles. Specs (provider:model) from repos are never honoured.
	AllowRepoModel bool `yaml:"allow_repo_model"`

	Review Review   `yaml:"review"`
	GitHub GitHub   `yaml:"github"`
	GitLab []GitLab `yaml:"gitlab"`
	// BotName is the mention handle for chat commands (@<bot_name> review).
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
	// VerifyCanDrop limits what the verifier may remove: "minor" (default)
	// lets it drop only minor and nitpick findings and turns disputed major or
	// critical findings into questions; "all" lets it drop anything.
	VerifyCanDrop string `yaml:"verify_can_drop"`
	// VerifyCanAdd lets the verifier add findings it thinks the reviewer
	// missed. Off by default: additions skip verification themselves, and a
	// real run posted a false "command injection" that way.
	VerifyCanAdd bool `yaml:"verify_can_add"`
	// RepoConfigFile is the per-repo config path at the PR head.
	RepoConfigFile string `yaml:"repo_config_file"`
}

// GitHub configures github.com or GitHub Enterprise Server.
type GitHub struct {
	APIURL string `yaml:"api_url"` // default https://api.github.com
	// Token is a PAT or fine-grained token (CLI / simple server setups).
	Token string `yaml:"token"`
	// GitHub App credentials (recommended for the server).
	AppID          int64  `yaml:"app_id"`
	PrivateKeyFile string `yaml:"private_key_file"`
	PrivateKey     string `yaml:"private_key"`
	WebhookSecret  string `yaml:"webhook_secret"`
}

// GitLab configures one GitLab instance (gitlab.com or self-managed).
type GitLab struct {
	BaseURL       string `yaml:"base_url"` // e.g. https://gitlab.example.com
	Token         string `yaml:"token"`
	WebhookSecret string `yaml:"webhook_secret"`
	SkipTLSVerify bool   `yaml:"skip_tls_verify"`
}

var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// Load reads a config file. An empty path or a missing default file
// yields defaults, so the CLI works with env vars alone.
func Load(path string) (*Config, error) {
	cfg := &Config{}
	explicit := path != ""
	if path == "" {
		path = os.Getenv("CODEREVIEW_CONFIG")
		explicit = path != ""
	}
	if path == "" {
		for _, p := range []string{"codereview.yaml", "codereview.yml"} {
			if _, err := os.Stat(p); err == nil {
				path = p
				break
			}
		}
	}
	if path == "" && os.Getenv("CODEREVIEW_CONFIG_YAML") != "" {
		// Inline config, used by executors (containers, Kubernetes Jobs).
		if err := yaml.Unmarshal(ExpandEnv([]byte(os.Getenv("CODEREVIEW_CONFIG_YAML"))), cfg); err != nil {
			return nil, fmt.Errorf("parse CODEREVIEW_CONFIG_YAML: %w", err)
		}
		cfg.Finalize()
		return cfg, nil
	}
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			if explicit || !os.IsNotExist(err) {
				return nil, fmt.Errorf("read config: %w", err)
			}
		} else {
			if err := yaml.Unmarshal(ExpandEnv(data), cfg); err != nil {
				return nil, fmt.Errorf("parse config %s: %w", path, err)
			}
		}
	}
	cfg.Finalize()
	return cfg, nil
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
	if v := os.Getenv("CODEREVIEW_MODEL"); v != "" {
		c.Model = v
	}
	if v := os.Getenv("CODEREVIEW_FALLBACK"); v != "" {
		c.Fallback = splitList(v)
	}
	if c.GitHub.Token == "" {
		c.GitHub.Token = os.Getenv("GITHUB_TOKEN")
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
	if c.BotName == "" {
		c.BotName = "codereview"
	}
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
		if hostOf(g.BaseURL) == strings.ToLower(host) {
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
