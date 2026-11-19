// Command codereview reviews one pull request (or local changes) per run
// with any LLM provider, from the command line or in CI/CD.
//
// For webhook-driven reviews across many repositories, a queue, SSO and an
// admin UI, see Divmora Code Review Console, which uses this engine.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/divmora/code-review-bot/internal/version"
	"github.com/divmora/code-review-bot/pkg/app"
	"github.com/divmora/code-review-bot/pkg/config"
	"github.com/divmora/code-review-bot/pkg/llm"
	"github.com/divmora/code-review-bot/pkg/review"
	"github.com/divmora/code-review-bot/pkg/vcs"
)

const usage = `codereview — AI code review for GitHub, GitLab and Bitbucket with any LLM provider

Usage:
  codereview review [PR_URL] [flags]   Review a pull/merge request (or local changes with --local)
  codereview ask PR_URL "question"     Ask a question about a pull/merge request
  codereview models [flags]            Show providers, model profiles and the active model chain
  codereview ping [flags]              Send a tiny request to check model credentials
  codereview version

Pick a model with --model, CODEREVIEW_MODEL or "model:" in codereview.yaml:
  openai:<model>  anthropic:<model>  gemini:<model>  bedrock:<model-id>  azure:<deployment>
  ollama:<model>  openrouter:<model>  groq:<model>  litellm:<model>  ... or a profile name

Run "codereview <command> -h" for flags.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch os.Args[1] {
	case "review":
		err = cmdReview(ctx, os.Args[2:])
	case "ask":
		err = cmdAsk(ctx, os.Args[2:])
	case "models":
		err = cmdModels(os.Args[2:])
	case "ping":
		err = cmdPing(ctx, os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println(version.Version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	var ec exitCode
	if errors.As(err, &ec) {
		os.Exit(int(ec))
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

type exitCode int

func (e exitCode) Error() string { return "exit " + strconv.Itoa(int(e)) }

type common struct {
	config  string
	model   string
	verbose bool
	json    bool
}

func (c *common) register(fs *flag.FlagSet) {
	fs.StringVar(&c.config, "config", "", "config file (default ./codereview.yaml or $CODEREVIEW_CONFIG)")
	fs.StringVar(&c.model, "model", "", "model profile or provider:model (overrides config and CODEREVIEW_MODEL)")
	fs.BoolVar(&c.verbose, "verbose", false, "debug logging")
}

func (c *common) setup() (*config.Config, *slog.Logger, error) {
	level := slog.LevelInfo
	if c.verbose {
		level = slog.LevelDebug
	}
	var h slog.Handler
	if c.json {
		h = slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	} else {
		h = slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	}
	logger := slog.New(h)
	slog.SetDefault(logger)
	cfg, err := config.Load(c.config)
	return cfg, logger, err
}

func cmdReview(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("review", flag.ExitOnError)
	var c common
	c.register(fs)
	local := fs.Bool("local", false, "review the local git checkout instead of a PR")
	dir := fs.String("dir", ".", "repository directory for --local")
	base := fs.String("base", "", "with --local: diff against this ref (e.g. origin/main); default reviews uncommitted changes")
	post := fs.Bool("post", false, "publish comments and the summary to the PR (default: print only)")
	full := fs.Bool("full", false, "ignore previous reviews and review every file")
	auto := fs.Bool("auto", false, "apply the repo's auto_review rules (drafts, title keywords, base branches), as in CI")
	format := fs.String("format", "text", "output: text, markdown, json, sarif")
	output := fs.String("output", "", "write the report to a file instead of stdout")
	failOn := fs.String("fail-on", "", "exit 3 if a finding is at least this severe: critical, major, minor, nitpick")
	profile := fs.String("profile", "", "override review profile: chill, balanced, assertive")
	maxComments := fs.Int("max-comments", 0, "override the inline comment limit")
	minSeverity := fs.String("min-severity", "", "drop findings below this severity: critical, major, minor, nitpick")
	commands := fs.Bool("commands", false, "advertise @bot chat commands in the summary (when a webhook service answers them)")
	scoreLabels := fs.Bool("score-labels", false, "with --post: set Quality/Security/Maintainability score labels on the PR/MR")
	commitStatus := fs.Bool("commit-status", false, "with --post: set a \"codereview\" status on the reviewed commit (running, then success/failed)")
	verifyModel := fs.String("verify-model", "", "model for the verification pass (profile or provider:model); default review.verify_model or the review model")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(fs.Output(), "Usage: codereview review [PR_URL] [flags]\n\nWithout PR_URL, the PR is detected from GitHub Actions, GitLab CI or Bitbucket Pipelines, or use --local.\n\n")
		fs.PrintDefaults()
	}
	_ = fs.Parse(reorder(args))
	cfg, logger, err := c.setup()
	if err != nil {
		return err
	}
	runner := &app.Runner{Cfg: cfg, Logger: logger, ModelOverride: c.model, VerifyModelOverride: *verifyModel}
	if (*scoreLabels || *commitStatus) && !*post {
		return errors.New("--score-labels and --commit-status need --post")
	}
	// CODEREVIEW_OVERRIDES carries per-repository settings as JSON (set by
	// Divmora Code Review Console); flags override it.
	ov, err := overridesFromEnv(cfg)
	if err != nil {
		return err
	}
	if *profile != "" || *maxComments > 0 || *minSeverity != "" {
		runner.Override = func(rc *config.RepoConfig) {
			if *profile != "" {
				rc.Profile = config.Profile(*profile)
			}
			if *maxComments > 0 {
				rc.MaxComments = *maxComments
			}
			if *minSeverity != "" {
				rc.MinSeverity = *minSeverity
			}
		}
	}

	var client vcs.Client
	var ref vcs.Ref
	switch {
	case *local:
		if *post {
			return errors.New("--post cannot be used with --local")
		}
		client = &vcs.LocalClient{Dir: *dir, Base: *base}
		ref = vcs.Ref{Platform: vcs.Local, Host: "local", Repo: *dir}
	case fs.NArg() > 0:
		if ref, err = vcs.ParseURL(fs.Arg(0)); err != nil {
			return err
		}
	default:
		var ok bool
		if ref, ok = refFromCI(); !ok {
			fs.Usage()
			return errors.New("no PR given: pass a PR/MR URL, use --local, or run inside a GitHub Actions pull_request job, a GitLab merge request pipeline or a Bitbucket pull-request pipeline")
		}
		logger.Info("detected pull request from CI", "pr", ref.String())
	}
	if client == nil {
		if client, err = runner.ClientFor(ref); err != nil {
			return err
		}
	}

	out, err := runner.Review(ctx, client, ref, app.Options{Auto: *auto, Full: *full, Post: *post, Overrides: ov, Commands: *commands, ScoreLabels: *scoreLabels, CommitStatus: *commitStatus})
	if err != nil {
		return err
	}
	if out.SkipReason != "" {
		logger.Info("review skipped", "reason", out.SkipReason)
		if *format == "json" {
			b, _ := json.Marshal(map[string]any{"skipped": true, "skip_reason": out.SkipReason})
			return writeReport(*output, string(b)+"\n")
		}
		return nil
	}
	res := out.Result
	var text string
	switch *format {
	case "text":
		text = review.Text(res, *output == "" && isTTY(os.Stdout))
	case "markdown", "md":
		text = review.Markdown(res)
	case "json":
		b, _ := json.MarshalIndent(res, "", "  ")
		text = string(b) + "\n"
	case "sarif":
		b, err := review.SARIF(res)
		if err != nil {
			return err
		}
		text = string(b) + "\n"
	default:
		return fmt.Errorf("unknown --format %q", *format)
	}
	if *format == "json" {
		// Add publish details for callers such as the console.
		var m map[string]any
		if json.Unmarshal([]byte(text), &m) == nil {
			m["posted"] = out.Posted
			m["rejected"] = len(out.Rejected)
			m["pr_url"] = out.PR.URL
			b, _ := json.MarshalIndent(m, "", "  ")
			text = string(b) + "\n"
		}
	}
	if err := writeReport(*output, text); err != nil {
		return err
	}
	if *post {
		logger.Info("published review", "pr", ref.String(), "inline_comments", out.Posted, "not_inline", len(out.Rejected)+len(res.Overflow))
	}
	if *failOn != "" {
		min := review.ParseSeverity(*failOn)
		for _, f := range append(append([]review.Finding{}, res.Findings...), res.Overflow...) {
			if f.Severity.AtLeast(min) {
				logger.Warn("failing: finding at or above --fail-on", "severity", f.Severity, "path", f.Path, "line", f.EndLine)
				return exitCode(3)
			}
		}
	}
	return nil
}

func cmdAsk(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("ask", flag.ExitOnError)
	var c common
	c.register(fs)
	post := fs.Bool("post", false, "post the answer as a PR comment")
	_ = fs.Parse(reorder(args))
	if fs.NArg() < 2 {
		return errors.New(`usage: codereview ask PR_URL "question" [--post]`)
	}
	cfg, logger, err := c.setup()
	if err != nil {
		return err
	}
	ref, err := vcs.ParseURL(fs.Arg(0))
	if err != nil {
		return err
	}
	runner := &app.Runner{Cfg: cfg, Logger: logger, ModelOverride: c.model}
	client, err := runner.ClientFor(ref)
	if err != nil {
		return err
	}
	ov, err := overridesFromEnv(cfg)
	if err != nil {
		return err
	}
	ans, err := runner.Answer(ctx, client, ref, strings.Join(fs.Args()[1:], " "), *post, ov)
	if err != nil {
		return err
	}
	return writeReport("", ans+"\n")
}

func cmdModels(args []string) error {
	fs := flag.NewFlagSet("models", flag.ExitOnError)
	var c common
	c.register(fs)
	_ = fs.Parse(args)
	cfg, _, err := c.setup()
	if err != nil {
		return err
	}
	fmt.Println("Providers:", strings.Join(llm.Providers(), ", "))
	if len(cfg.Models) > 0 {
		fmt.Println("\nProfiles:")
		names := make([]string, 0, len(cfg.Models))
		for n := range cfg.Models {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			m := cfg.Models[n]
			extra := ""
			if m.Region != "" {
				extra += " region=" + m.Region
			}
			if m.BaseURL != "" {
				extra += " base_url=" + m.BaseURL
			}
			fmt.Printf("  %-14s %s%s\n", n, m.String(), extra)
		}
	}
	chain, err := cfg.ModelChain(c.model)
	if err != nil {
		fmt.Println("\nActive chain: none —", err)
		return nil
	}
	parts := make([]string, len(chain))
	for i, m := range chain {
		parts[i] = m.String()
	}
	fmt.Println("\nActive chain:", strings.Join(parts, " -> "))
	return nil
}

func cmdPing(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("ping", flag.ExitOnError)
	var c common
	c.register(fs)
	_ = fs.Parse(args)
	cfg, logger, err := c.setup()
	if err != nil {
		return err
	}
	chain, err := cfg.ModelChain(c.model)
	if err != nil {
		return err
	}
	failed := 0
	for _, m := range chain {
		m.MaxRetries = -1
		cl, err := llm.New(m, logger)
		if err != nil {
			fmt.Printf("✗ %s: %v\n", m.String(), err)
			failed++
			continue
		}
		start := time.Now()
		cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		resp, err := cl.Complete(cctx, llm.Request{
			Messages:  []llm.Message{{Role: llm.User, Content: `Reply with the JSON {"ok": true}.`}},
			MaxTokens: 256,
			Schema: &llm.Schema{Name: "ping", JSON: map[string]any{
				"type": "object", "required": []string{"ok"},
				"properties": map[string]any{"ok": map[string]any{"type": "boolean"}},
			}},
		})
		cancel()
		if err != nil {
			fmt.Printf("✗ %s: %v\n", m.String(), err)
			failed++
			continue
		}
		fmt.Printf("✓ %s  %s  structured=%s  %d in / %d out tokens\n", m.String(), time.Since(start).Round(time.Millisecond), strings.TrimSpace(string(resp.JSON)), resp.Usage.InputTokens, resp.Usage.OutputTokens)
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d models failed", failed, len(chain))
	}
	return nil
}

func writeReport(path, text string) error {
	if path == "" {
		// Executors whose logs merge stdout and stderr (Kubernetes) set a
		// marker so they can find where the result starts.
		if m := os.Getenv("CODEREVIEW_RESULT_MARKER"); m != "" {
			text = "\n" + m + "\n" + text + "\n" + m + "-END\n"
		}
		_, err := fmt.Print(text)
		return err
	}
	return os.WriteFile(path, []byte(text), 0o644)
}

func overridesFromEnv(cfg *config.Config) (*config.Overrides, error) {
	raw := os.Getenv("CODEREVIEW_OVERRIDES")
	if raw == "" {
		return nil, nil
	}
	var ov config.Overrides
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ov); err != nil {
		return nil, fmt.Errorf("CODEREVIEW_OVERRIDES: %w", err)
	}
	profiles := map[string]bool{}
	for name := range cfg.Models {
		profiles[name] = true
	}
	if err := ov.Validate(profiles); err != nil {
		return nil, fmt.Errorf("CODEREVIEW_OVERRIDES: %w", err)
	}
	return &ov, nil
}

// refFromCI detects the PR in GitHub Actions, GitLab CI or Bitbucket Pipelines.
func refFromCI() (vcs.Ref, bool) {
	if os.Getenv("GITHUB_ACTIONS") == "true" {
		if p := os.Getenv("GITHUB_EVENT_PATH"); p != "" {
			data, err := os.ReadFile(p)
			if err == nil {
				var ev struct {
					Number      int `json:"number"`
					PullRequest struct {
						Number int `json:"number"`
					} `json:"pull_request"`
					Issue struct {
						Number int `json:"number"`
					} `json:"issue"`
				}
				if json.Unmarshal(data, &ev) == nil {
					n := ev.PullRequest.Number
					if n == 0 {
						n = ev.Number
					}
					if n == 0 {
						n = ev.Issue.Number
					}
					host := strings.TrimPrefix(strings.TrimPrefix(os.Getenv("GITHUB_SERVER_URL"), "https://"), "http://")
					if host == "" {
						host = "github.com"
					}
					if n > 0 {
						return vcs.Ref{Platform: vcs.GitHub, Host: host, Repo: os.Getenv("GITHUB_REPOSITORY"), Number: n}, true
					}
				}
			}
		}
	}
	// Bitbucket Pipelines (pull-request pipelines set BITBUCKET_PR_ID).
	if id := os.Getenv("BITBUCKET_PR_ID"); id != "" {
		n, err := strconv.Atoi(id)
		ws, slug := os.Getenv("BITBUCKET_WORKSPACE"), os.Getenv("BITBUCKET_REPO_SLUG")
		if err == nil && ws != "" && slug != "" {
			return vcs.Ref{Platform: vcs.Bitbucket, Host: "bitbucket.org", Repo: ws + "/" + slug, Number: n}, true
		}
	}
	if iid := os.Getenv("CI_MERGE_REQUEST_IID"); iid != "" {
		n, err := strconv.Atoi(iid)
		if err == nil {
			return vcs.Ref{Platform: vcs.GitLab, Host: os.Getenv("CI_SERVER_HOST"), Repo: os.Getenv("CI_PROJECT_PATH"), Number: n}, true
		}
	}
	return vcs.Ref{}, false
}

// reorder lets flags follow positional args: "review URL --post".
func reorder(args []string) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
			// Flags with separate values: anything not boolean and without "=".
			if !strings.Contains(a, "=") && !boolFlags[strings.TrimLeft(a, "-")] && i+1 < len(args) {
				flags = append(flags, args[i+1])
				i++
			}
			continue
		}
		pos = append(pos, a)
	}
	return append(flags, pos...)
}

var boolFlags = map[string]bool{"local": true, "post": true, "full": true, "auto": true, "commands": true, "score-labels": true, "commit-status": true, "verbose": true, "h": true, "help": true}

func isTTY(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}
