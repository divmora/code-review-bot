# AGENTS.md — Code Review Bot

Guidance for AI coding agents and contributors. Org-wide rules live in
[divmora/.github AGENTS.md](https://github.com/divmora/.github/blob/main/AGENTS.md).

## Layout

```
cmd/codereview/   CLI: review (one PR per run), ask, learn, models, ping, config validate, version
pkg/diff/         unified diff parser, commentable-line logic, annotated rendering
pkg/vcs/          code hosts: GitHub (token or App), GitLab, Bitbucket Cloud, local git; bot-state
                  markers (Bitbucket gets them as Markdown link references: no HTML there)
internal/llm/     provider-neutral LLM client + providers (openai*, anthropic, gemini, bedrock)
internal/config/  engine config (models, code hosts, limits), .codereview.yaml, Overrides, globs,
                  strict validation (config validate)
internal/review/  engine: filtering, secret scan, batching, prompts, validation, ranking, rendering
internal/app/     Runner: the review/ask flow
internal/version  build version
examples/         config examples and CI templates (kept loadable by tests)
action.yml        GitHub Action (composite; installs the CLI at the action ref)
```

The CLI contract is the interface for other programs: flags, `CODEREVIEW_CONFIG_YAML`,
`CODEREVIEW_OVERRIDES`, JSON output, `config validate` and exit codes (see README). Keep it
stable. Divmora Code Review Console (Elastic License 2.0, separate repo) builds config YAML itself and runs this
CLI through executors; the only Go packages it imports are `pkg/vcs` (and `pkg/diff` through it),
so those are the only public packages. Keep them backward compatible; everything else is
`internal/` and free to change.

**Scope:** this repo is the CLI and engine only. Webhooks, queues, dashboards, admin, SSO and
databases belong in the console. Don't add a long-running server here.

## Review flow (`app.Runner.Review`)

1. `GetPR` → `.codereview.yaml` read at the **base** SHA (a PR must not weaken its own review).
2. With `Auto`: apply `auto_review` skip rules and skip an already-reviewed head SHA.
3. `BotState` from the bot's summary comment: last reviewed SHA, stored file summaries (base64 JSON),
   fingerprints of existing inline comments.
4. Incremental: `ChangedPaths(lastSHA, head)` limits which files are sent; on failure (force push)
   fall back to a full review.
5. `review.Engine.Run`: filter → secret scan → fetch small files for context → token-budgeted
   batches (large files split by hunk) → parallel LLM calls with `reviewSchema` → **validate every
   comment against the diff** → thresholds by profile → dedupe by fingerprint → rank → cap.
6. Walkthrough call (`summarySchema`); on failure a basic summary is generated.
7. With `Post`: inline comments (GitHub: one review; on 422, one by one and rejected comments go in
   the summary), then the summary comment is upserted.

## Invariants (keep them)

- **Dry run by default**: the CLI never writes to a PR without `--post`.
- **Only changed lines**: `review.validate` drops comments outside a hunk, spanning hunks, or not
  touching an added line (pure-deletion hunks excepted). Never relax this without a profile switch.
- **PR content is untrusted**: prompts say so. Never let repo config choose arbitrary model
  endpoints; `allow_repo_model` only selects operator-defined profiles.
- **Operator-only settings**: `review.approve` and `review.commands` come from the operator config
  only; a repository's `.codereview.yaml` can't set them, and its `fail_on` can only be stricter.
- **Flags win**: precedence is built-in defaults < `review.defaults` < `.codereview.yaml` <
  `CODEREVIEW_OVERRIDES` < CLI flags (`Runner.RepoConfig` applies them in that order).
- **Provider parity**: each provider supports `Request.Schema`. Add a test with an `httptest`
  server for every provider change. Retries live in `llm.WithRetry`, not in providers (Bedrock
  SDK retries are disabled on purpose).

## Adding things

- **Provider**: new file in `internal/llm`, `Register` in `init`, test request/response shape. If
  it speaks the OpenAI API, add an entry to `compatProviders` instead.
- **Code host**: implement `vcs.Client` and add URL/CI detection.
- **Config key**: update `examples/`, `README.md` and the `TestExamplesParse` expectations.
  `config validate` rejects unknown keys, so the console must learn a new key before it sends it.

## Conventions

- Go 1.27, stdlib first (only deps: AWS SDK v2 for Bedrock, yaml.v3). `log/slog` everywhere.
- Wrap errors with context. Propagate `context.Context`.
- `make test` runs everything with `-race`; tests must not need network or credentials.
- Version lives in `internal/version` as `0.0.0-dev`, injected by GoReleaser.
- Conventional Commits for commits and PR titles.
