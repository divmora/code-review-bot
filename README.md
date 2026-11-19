# Code Review Bot

[![Latest Release](https://img.shields.io/github/v/release/divmora/code-review-bot?logo=github)](https://github.com/divmora/code-review-bot/releases)
[![License: Apache 2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
[![CI/CD](https://github.com/divmora/code-review-bot/actions/workflows/ci.yml/badge.svg)](https://github.com/divmora/code-review-bot/actions)
[![Go Version](https://img.shields.io/github/go-mod/go-version/divmora/code-review-bot)](go.mod)
[![Ask DeepWiki](https://deepwiki.com/badge.svg)](https://deepwiki.com/divmora/code-review-bot)
[![Security Policy](https://img.shields.io/badge/Security-Policy-green.svg)](SECURITY.md)

AI code review for **GitHub** and **GitLab** that works with **any LLM provider**: OpenAI, Anthropic, Google Gemini, AWS Bedrock, Azure OpenAI, Ollama, OpenRouter, Groq, DeepSeek, Mistral, xAI, Together, LiteLLM or any OpenAI-compatible server. Switching provider is a one-line config change.

Each run reviews **one** pull/merge request, or your local changes:

- **CI/CD**: a GitHub Action or GitLab CI job reviews every push to a PR. There's no server to host.
- **CLI**: review a PR/MR by URL, or your uncommitted changes before you push.

For webhook-driven reviews across many repositories, with a queue, chat commands (`@codereview ...`), SSO, a repository allowlist, history and dashboards, use **Divmora Code Review Console**, which runs this CLI as its review engine through local, Docker or Kubernetes executors.

## What a review contains

- **Inline comments** on the changed lines, with severity (🔴 critical · 🟠 major · 🟡 minor · 🔵 nitpick), an explanation, and a **one-click committable suggestion** in GitHub or GitLab syntax.
- **A walkthrough comment**: summary, a *Changes* table grouping related files, estimated review effort, plus details on what was reviewed, skipped and filtered. It is edited in place on every run, not re-posted.
- **Incremental reviews**: after a new push only files changed since the last reviewed commit go to the model. Comments already on the PR are recognised and never posted twice.
- **Ask**: `codereview ask <PR_URL> "question"` answers questions about a PR using its diff.

## Guardrails

- **Only changed code**: every model comment is checked against the real diff. Comments on untouched lines, unknown files or ranges that span hunks are dropped before posting.
- **Built-in secret scanner**: a deterministic check for AWS, GitHub, GitLab, Slack, Stripe, Google, OpenAI and Anthropic keys, private keys and JWTs in added lines. It works even if the model call fails. Put `codereview:allow` on a line to silence an intentional match.
- **Noise control**: review profiles, confidence and severity thresholds, a per-PR comment budget (overflow goes into the summary), and automatic skipping of lockfiles, generated, vendored, minified and binary files.
- **Prompt-injection resistant**: PR content is passed to the model as untrusted data, and the repo's `.codereview.yaml` is read from the **base** commit, so a PR cannot rewrite its own review rules.
- **Resilient**: provider retries with backoff and `Retry-After`, ordered **fallback models**, large PRs split into token-budgeted batches reviewed in parallel, and partial results when one batch fails.

## Quick start (CLI)

```bash
go install github.com/divmora/code-review-bot/cmd/codereview@latest

# Pick any provider; the matching API key env var is read automatically.
export CODEREVIEW_MODEL=anthropic:claude-sonnet-5-5   ANTHROPIC_API_KEY=...
# export CODEREVIEW_MODEL=openai:<model>              OPENAI_API_KEY=...
# export CODEREVIEW_MODEL=gemini:<model>              GEMINI_API_KEY=...
# export CODEREVIEW_MODEL=bedrock:<model-id>          AWS_REGION=us-east-1 (+ any AWS credential source)
# export CODEREVIEW_MODEL=ollama:qwen2.5-coder:14b    (local, no key)

codereview ping                                  # check the model and credentials
codereview review --local                        # uncommitted changes
codereview review --local --base origin/main     # your branch vs main
codereview review https://github.com/org/repo/pull/123             # print only (dry run)
codereview review https://gitlab.com/group/proj/-/merge_requests/9 --post
codereview ask https://github.com/org/repo/pull/123 "Is the retry loop safe under cancellation?"
```

Nothing is written to a PR unless you pass `--post`.

## Switching providers

Choose the model in order of precedence: `--model` flag, `CODEREVIEW_MODEL` env var, then `model:` in `codereview.yaml`. The value is a **profile name** from the config or a direct **`provider:model`** spec. Only the first colon separates the two, so `bedrock:anthropic.x-v1:0` and `ollama:qwen2.5-coder:14b` work.

```yaml
# codereview.yaml
model: bedrock            # change this line to switch provider
fallback: [openai, local] # tried in order if the active model fails

models:
  bedrock:  { provider: bedrock,   model: <model-or-inference-profile-id>, region: us-east-1 }
  openai:   { provider: openai,    model: <model>, api_key: "${OPENAI_API_KEY}" }
  claude:   { provider: anthropic, model: claude-sonnet-5-5 }
  gemini:   { provider: gemini,    model: <model> }
  local:    { provider: ollama,    model: "qwen2.5-coder:14b", timeout: 10m }
  gateway:  { provider: litellm,   model: <name>, base_url: "https://llm.internal/v1" }
```

| Provider | `provider:` | Credentials | Structured output via |
|---|---|---|---|
| OpenAI | `openai` | `OPENAI_API_KEY` | `response_format: json_schema` |
| Anthropic | `anthropic` | `ANTHROPIC_API_KEY` | forced tool use |
| Google Gemini | `gemini` | `GEMINI_API_KEY` / `GOOGLE_API_KEY` | `responseJsonSchema` |
| AWS Bedrock | `bedrock` | AWS default chain (env, SSO, IRSA, instance role), `region` | Converse forced tool (Anthropic, Nova, Mistral Large); schema prompt otherwise |
| Azure OpenAI | `azure` | `AZURE_OPENAI_API_KEY` + `base_url` | `json_schema` |
| Ollama | `ollama` | none (`OLLAMA_BASE_URL`) | `json_object` |
| OpenRouter, Groq, DeepSeek, Mistral, xAI, Together | `openrouter`, `groq`, `deepseek`, `mistral`, `xai`, `together` | `<NAME>_API_KEY` | `json_object` |
| LiteLLM proxy | `litellm` | `LITELLM_API_KEY`, `LITELLM_BASE_URL` | `json_object` |
| Anything OpenAI-compatible (vLLM, LM Studio, TGI, ...) | `openai-compatible` | `base_url`, optional key | schema prompt (set `structured_output` to upgrade) |

Every response is also parsed leniently (fenced or prose-wrapped JSON), so weaker models still work. `codereview models` prints the active chain; `codereview ping` tests every model in it.

**Adding a provider** means implementing `llm.Client` (`Name`, `Complete`) and calling `llm.Register("name", factory)`. See [`internal/llm`](internal/llm).

## Run in CI

**GitHub Actions**: see [`examples/github-workflow.yml`](examples/github-workflow.yml).

```yaml
- uses: divmora/code-review-bot@v0.1.0
  with:
    model: anthropic:claude-sonnet-5-5
    fail-on: critical          # optional gate
  env:
    ANTHROPIC_API_KEY: ${{ secrets.ANTHROPIC_API_KEY }}
```

**GitLab CI**: see [`examples/gitlab-ci.yml`](examples/gitlab-ci.yml). The MR is detected from `CI_*` variables; set `GITLAB_TOKEN` (`api` scope) and your provider key as masked variables.

`--auto` applies the repo's `auto_review` rules (drafts, title keywords, base branches). `--fail-on <severity>` exits with code 3 so you can gate merges. `--format sarif` produces output for code-scanning dashboards.

## Use from another program

The CLI is designed to be driven by schedulers such as Divmora Code Review Console:

| Input | How |
|---|---|
| Which PR | the URL argument, or detected from GitHub Actions / GitLab CI variables |
| Engine config | `--config codereview.yaml` (model profiles, limits; reference keys with `api_key_env`) |
| Secrets | environment only: provider keys, `GITHUB_TOKEN`, `GITLAB_TOKEN` |
| Per-repo admin settings | `CODEREVIEW_OVERRIDES` JSON: `profile`, `max_comments`, `min_severity`, `model`, `auto_review`, `drafts`, `instructions` |
| Output | `--format json`: the full result (findings, summary, usage), plus `posted`, `rejected` and `pr_url`; `{"skipped": true, "skip_reason": ...}` when skipped |
| Logs | stderr |
| Exit code | `0` success, `1` error, `3` a finding met `--fail-on` |

## Scores and verification

Every review rates the change from **1 to 100** on **Quality**, **Security** and **Maintainability**. The scores appear in the summary, the CLI output and the JSON result. They're capped by the findings: a critical security finding caps Security at 40, a major one at 70.

With `--post --score-labels` the scores are also set as labels on the PR/MR, replacing earlier scores:
- GitLab: scoped labels, `Quality Score::85`;
- GitHub: `Quality Score: 85`.
Colours follow the score: green 80+, orange 50–79, red below 50.

A second, skeptical **verification pass** (`review.verify`, on by default; one extra model call) checks every finding against the code and the PR description. A different model catches more of the reviewer's mistakes: set `review.verify_model` or pass `--verify-model`. By default (`review.verify_can_drop: minor`) the verifier may remove only minor findings; a major or critical finding it disputes is kept as a question with its reason, and every removal is listed in the summary. The verifier doesn't add findings of its own unless `review.verify_can_add: true`: its additions would go unverified, and in testing one was a false "command injection". The pass:
- it removes false positives;
- it turns findings that depend on data or configuration into **❓ questions** for the author;
- with `verify_can_add`, it may add a clearly missed major issue, which still has to pass the same diff validation.
The summary reports what the pass changed.

## New commits and repeat runs

The bot keeps its state **on the PR itself**, so a fresh CI job needs no database:

- **The summary comment records the reviewed commit,** visibly ("Commit: `a2ebf75e`") and in a hidden marker (`<!-- codereview:reviewed_sha=... -->`), together with the per-file summaries.
- **On a new push** only files changed since that commit are reviewed; a force-push falls back to a full review. With `--auto`, a commit that was already reviewed is skipped.
- **Findings already on the PR are never posted twice,** even when the model words them differently on a later run. The bot matches each new finding against its existing comments by file, nearby line and topic, not just an exact fingerprint. Repeats still count towards the scores, so a re-run doesn't inflate them. Two phrasings of the same problem within one run are merged. The summary comment, the description block and the score labels are all edited in place.
- **With `--commit-status`,** each reviewed commit also gets a `codereview` status in the MR/PR commit list and checks: *running* during the review, then *success* ("Reviewed: 2 comments · Quality 60 · Security 100 · Maintainability 72") or *failed*. GitHub Actions needs `statuses: write` for this.

Commit messages are never changed: changing one rewrites history (a new SHA and a force-push).

## PR description and title

The bot uses the same conventions and `.codereview.yaml` keys as CodeRabbit, and it never rewrites the author's text:

| Key | Default | Effect (with `--post`) |
|---|---|---|
| `high_level_summary` | `true` | Appends a "Summary by codereview" block (release-notes style highlights) to the PR description and replaces it on later runs. |
| `high_level_summary_placeholder` | `@codereview summary` | Put it in the description to choose where the block goes; it works even when `high_level_summary` is false. |
| `high_level_summary_instructions` | | How to write the summary. |
| `auto_title_placeholder` | `@codereview` | Put it in the PR title to have it replaced with a generated Conventional Commits title. Titles are never changed otherwise. |
| `auto_title_instructions` | | How to write the title. |

When the title or description doesn't match the code, the summary comment shows a **📝 Description check** with what's wrong or missing, plus a suggested title. The PR is read again just before editing, so edits made during the review are kept.

## Repository guidelines (`AGENTS.md`)

The reviewer reads your project's guideline files **from the target branch** and checks changes against them, naming the rule a comment relies on:

- `AGENTS.md`, `CLAUDE.md`, `.github/copilot-instructions.md` and `.cursorrules` at the repository root;
- nested `AGENTS.md` files in the changed files' folders. Each applies only to files below it, nearest folder first.

They're size-capped (about 6k tokens), and reading them from the target branch means a PR can't rewrite the rules it's reviewed against. Configure or disable them in `.codereview.yaml` under `guidelines:`.

## Context for large files

Files up to `review.full_file_max_lines` (800) are sent whole. Larger files are sent as numbered excerpts of `review.context_lines` (60) lines around each change, so the model sees nearby definitions instead of a bare hunk.

## Per-repository config

Commit [`.codereview.yaml`](examples/.codereview.yaml) to the default branch:

```yaml
profile: balanced              # chill | balanced | assertive
instructions: |
  Flag fmt.Println outside tests; we use log/slog.
path_instructions:
  - path: "migrations/**"
    instructions: Migrations must be reversible.
path_filters: ["!docs/**"]
max_comments: 20
auto_review:
  drafts: false
  base_branches: ["main", "release/*"]
  ignore_title_keywords: ["WIP", "[skip review]"]
```

## CLI reference

| Command | Purpose |
|---|---|
| `review [URL]` | Review one PR/MR. Flags: `--local`, `--dir`, `--base`, `--post`, `--full`, `--auto`, `--format text\|markdown\|json\|sarif`, `--output`, `--fail-on`, `--profile`, `--max-comments`, `--min-severity`, `--commands`, `--score-labels`, `--commit-status`, `--verify-model` |
| `ask URL "question"` | Answer a question about a PR (`--post` to reply on it) |
| `models` | List providers, profiles and the active chain |
| `ping` | Test each model in the chain |
| `version` | Print version |

Common flags: `--config`, `--model`, `--verbose`.

## Development

```bash
make build    # bin/codereview
make test     # go test -race ./...
make lint
```

Architecture and contributor notes are in [AGENTS.md](AGENTS.md); planned work is in [ROADMAP.md](ROADMAP.md).

## License

Licensed under the **Apache License, Version 2.0**. It is free to use, modify and run in any environment, including commercial and production use, with no commercial license required. See [LICENSE](LICENSE).

Divmora Code Review Console (webhooks, queue, admin UI) is a separate product under the Business Source License 1.1.
