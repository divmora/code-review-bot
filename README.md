# Code Review Bot

[![Latest Release](https://img.shields.io/github/v/release/divmora/code-review-bot?logo=github)](https://github.com/divmora/code-review-bot/releases)
[![License: Apache 2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
[![CI/CD](https://github.com/divmora/code-review-bot/actions/workflows/ci.yml/badge.svg)](https://github.com/divmora/code-review-bot/actions)
[![Go Version](https://img.shields.io/github/go-mod/go-version/divmora/code-review-bot)](go.mod)
[![Documentation](https://img.shields.io/badge/docs-GitHub%20Pages-blue.svg)](https://divmora.github.io/code-review-bot/)
[![Ask DeepWiki](https://deepwiki.com/badge.svg)](https://deepwiki.com/divmora/code-review-bot)
[![Security Policy](https://img.shields.io/badge/Security-Policy-green.svg)](SECURITY.md)

AI code review for **GitHub**, **GitLab** and **Bitbucket Cloud** that works with **any LLM provider**: OpenAI, Anthropic, Google Gemini, AWS Bedrock, Azure OpenAI, Ollama, OpenRouter, Groq, DeepSeek, Mistral, xAI, Together, LiteLLM or any OpenAI-compatible server. Switching provider is a one-line config change.

Each run reviews **one** pull/merge request, or your local changes:

- **CI/CD**: a GitHub Action, GitLab CI job or Bitbucket Pipelines step reviews every push to a PR. There's no server to host.
- **CLI**: review a PR/MR by URL, or your uncommitted changes before you push.

For webhook-driven reviews across many repositories, with a queue, chat commands (`@codereview ...`), SSO, a repository allowlist, history and dashboards, use **[Divmora Code Review Console](https://divmora.github.io/code-review-console/)**, which runs this CLI as its review engine through local, Docker or Kubernetes executors.

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

codereview config validate                       # check the config without calling a model
codereview ping                                  # check the model and credentials
codereview review --local                        # uncommitted changes
codereview review --local --base origin/main     # your branch vs main
codereview review https://github.com/org/repo/pull/123             # print only (dry run)
codereview review https://gitlab.com/group/proj/-/merge_requests/9 --post
codereview ask https://github.com/org/repo/pull/123 "Is the retry loop safe under cancellation?"
```

Nothing is written to a PR unless you pass `--post`.

Prebuilt binaries for Linux, macOS and Windows are on the [Releases](https://github.com/divmora/code-review-bot/releases) page. There's also a Docker image whose entrypoint is the `codereview` binary:

<!-- x-release-please-start-version -->
```bash
docker run --rm -e CODEREVIEW_MODEL -e ANTHROPIC_API_KEY -e GITHUB_TOKEN \
  ghcr.io/divmora/code-review-bot:0.4.1 review https://github.com/org/repo/pull/123

# local changes: mount the repository at /workspace
docker run --rm -v "$PWD:/workspace" -e CODEREVIEW_MODEL -e ANTHROPIC_API_KEY \
  ghcr.io/divmora/code-review-bot:0.4.1 review --local
```
<!-- x-release-please-end -->

## How it works

```
 PR diff + description + .codereview.yaml (from the target branch)
   │
   ├─ 1. pick up where the last run stopped (state is kept in the bot's own PR comment)
   ├─ 2. skip lockfiles, generated, vendored and binary files; scan added lines for secrets
   ├─ 3. build token-budgeted batches: small files whole, large ones as excerpts around changes
   ├─ 4. review the batches in parallel (structured output, retries, fallback models)
   ├─ 5. drop every comment that isn't on a line the PR changed
   ├─ 6. verification pass: remove false positives, turn uncertain findings into questions
   └─ 7. with --post: inline comments, walkthrough summary, scores, labels, commit status
```

The model only proposes findings; the deterministic checks in steps 2 and 5 decide what can be posted.

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

<!-- x-release-please-start-version -->
```yaml
- uses: divmora/code-review-bot@v0.4.1
  with:
    model: anthropic:claude-sonnet-5-5
    fail-on: critical          # optional gate
  env:
    ANTHROPIC_API_KEY: ${{ secrets.ANTHROPIC_API_KEY }}
```
<!-- x-release-please-end -->

**GitLab CI**: see [`examples/gitlab-ci.yml`](examples/gitlab-ci.yml). The MR is detected from `CI_*` variables; set `GITLAB_TOKEN` (`api` scope) and your provider key as masked variables.

**Bitbucket Pipelines**: see [`examples/bitbucket-pipelines.yml`](examples/bitbucket-pipelines.yml). The PR is detected from `BITBUCKET_PR_ID`, `BITBUCKET_WORKSPACE` and `BITBUCKET_REPO_SLUG`.

`--auto` applies the repo's `auto_review` rules (drafts, title keywords, base branches). `--fail-on <severity>` exits with code 3 so you can gate merges. `--format sarif` produces output for code-scanning dashboards.

Tips for CI:
- **Keep it advisory.** The GitLab example sets `allow_failure: true` so a provider outage never blocks a pipeline. Add `--fail-on critical` only when you want the review to gate merges.
- **Drafts are skipped** with `--auto` unless `auto_review.drafts: true`. So are closed PRs and commits that were already reviewed.
- **Self-managed GitLab** is detected from `CI_SERVER_URL` (or `GITLAB_URL`). If it uses a private CA, add the CA to the image or point `SSL_CERT_FILE` at it. As a last resort, set `skip_tls_verify` for that instance in `codereview.yaml`:

  ```yaml
  gitlab:
    - base_url: https://gitlab.internal.example.com
      token: ${GITLAB_TOKEN}
      skip_tls_verify: true   # only for self-signed certificates you can't install
  ```

## Use from another program

The CLI is designed to be driven by schedulers such as Divmora Code Review Console. Its flags, environment variables, JSON output and exit codes are the interface; the Go packages are not, apart from `pkg/vcs` and `pkg/diff`:

| Input | How |
|---|---|
| Which PR | the URL argument, or detected from GitHub Actions / GitLab CI variables |
| Engine config | `--config codereview.yaml`, or the YAML itself in `CODEREVIEW_CONFIG_YAML` (model profiles, limits; reference keys with `api_key_env`) |
| Checking input | `codereview config validate [--format json]`: rejects unknown keys, unknown providers, unresolvable models and invalid `CODEREVIEW_OVERRIDES`, without calling a model. Exit `1` when invalid |
| Secrets | environment only: provider keys, `GITHUB_TOKEN`, `GITLAB_TOKEN` |
| Per-repo admin settings | `CODEREVIEW_OVERRIDES` JSON: `profile`, `max_comments`, `min_severity`, `model`, `auto_review`, `drafts`, `instructions`, `learnings` |
| Output | `--format json`: the full result (findings, summary, usage), plus `posted`, `rejected` and `pr_url`; `{"skipped": true, "skip_reason": ...}` when skipped |
| Logs | stderr |
| Exit code | `0` success, `1` error, `3` a finding met `--fail-on` |

## Scores and verification

Every review rates the change from **1 to 100** on **Quality**, **Security** and **Maintainability**. The scores appear in the summary, the CLI output and the JSON result. They're capped by the findings: a critical security finding caps Security at 40, a major one at 70.

With `--post --score-labels` the scores are also set as labels on the PR/MR, replacing earlier scores:
- GitLab: scoped labels, `Quality Score::85`;
- GitHub: `Quality Score: 85`.
Colours follow the score: green 80+, orange 50–79, red below 50.

A second, skeptical **verification pass** (`review.verify`, on by default; one extra model call) checks every finding against the code and the PR description. A different model catches more of the reviewer's mistakes: set `review.verify_model` or pass `--verify-model`. The verifier classifies each finding as confirmed, false positive, **speculative** (only a problem if the code changed later, e.g. "injection" through a constant) or a question for the author. By default (`review.verify_can_drop: confident`) it removes minor findings it disputes, and major or critical ones that are speculative or that it is at least 80% sure are false; other disputed findings are posted as questions with its reason. `minor` keeps every disputed major finding as a question; `all` drops anything disputed. Every removal is listed in the summary. The verifier doesn't add findings of its own unless `review.verify_can_add: true`: its additions would go unverified, and in testing one was a false "command injection". The pass:
- it removes false positives;
- it turns findings that depend on data or configuration into **❓ questions** for the author;
- with `verify_can_add`, it may add a clearly missed major issue, which still has to pass the same diff validation.
The summary reports what the pass changed.


Models vary from run to run. `review.passes: 2` (or 3) reviews each batch independently that many times and keeps the union of the findings, merged and then verified, at the cost of one more reviewer call per batch per pass. Findings already posted by an earlier run go through verification again too, so a speculative one cannot keep lowering the scores. Scores follow the remaining findings: a dimension with a critical, major or minor issue is capped at 40, 70 or 88; a dimension with no remaining issue of at least minor severity scores at least 85, so questions and findings the verifier removed never pull a score down. The reviewer is told to ask (not accuse) when a change loosens a check, saying what now passes that used to fail, and to back every security finding with input an attacker controls in the current code.
## Bitbucket Cloud

Review `https://bitbucket.org/<workspace>/<repo>/pull-requests/<id>` like any other PR. Two kinds of credential work:
- a **repository, project or workspace access token** in `BITBUCKET_TOKEN`, with Pull requests: Write and Repositories: Read;
- an **Atlassian API token** in `BITBUCKET_API_TOKEN`, with your account email in `BITBUCKET_EMAIL`. (Atlassian is retiring app passwords.)

The differences from GitHub and GitLab:
- Bitbucket has no PR labels, so `--score-labels` is skipped. The scores still appear in the summary and in the build status.
- There are no comment reactions.
- Suggestions are shown as a "Suggested change" code block, since there's no one-click apply.
- `--commit-status` sets a Bitbucket **build status** (`codereview`).
- Bitbucket's Markdown has no HTML, so the bot's hidden markers are written as invisible link references and collapsible sections as bold headings.

Bitbucket Data Center (self-hosted) uses a different API and isn't supported yet.

## New commits and repeat runs

The bot keeps its state **on the PR itself**, so a fresh CI job needs no database:

- **The summary comment records the reviewed commit,** visibly ("Commit: `a2ebf75e`") and in a hidden marker (`<!-- codereview:reviewed_sha=... -->`), together with the per-file summaries.
- **On a new push** only files changed since that commit are reviewed; a force-push falls back to a full review. With `--auto`, a commit that was already reviewed is skipped.
- **Closed or merged PRs are not reviewed with `--auto`** (a pipeline can run after the MR was closed). Run without `--auto` to review one anyway.
- **When nothing was reviewed** (too many files, or no new reviewable changes), the summary comment says why and the PR description is left alone.
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

The bot's name in these placeholders and in the summary's command hints is `bot_name` from `codereview.yaml`. When it is not set, the bot uses the user its token belongs to (for example `@gitlab-bot`), so the code host autocompletes and notifies the real account. It falls back to `@codereview` when the token has no user (GitHub App installation tokens, Bitbucket repository access tokens): set `bot_name` there, for a GitHub App to its slug. Placeholders follow the name, e.g. `@gitlab-bot summary`.
| `auto_title_instructions` | | How to write the title. |

When the title or description doesn't match the code, the summary comment shows a **📝 Description check** with what's wrong or missing, plus a suggested title. The PR is read again just before editing, so edits made during the review are kept.

## Repository guidelines (`AGENTS.md`)

The reviewer reads your project's guideline files **from the target branch** and checks changes against them, naming the rule a comment relies on:

- `AGENTS.md`, `CLAUDE.md`, `.github/copilot-instructions.md` and `.cursorrules` at the repository root;
- nested `AGENTS.md` files in the changed files' folders. Each applies only to files below it, nearest folder first.

They're size-capped (about 6k tokens), and reading them from the target branch means a PR can't rewrite the rules it's reviewed against. Configure or disable them in `.codereview.yaml` under `guidelines:`.

## Team learnings

Learnings are decisions your team made in earlier reviews ("account numbers are compared without leading zeros on purpose"). The reviewer follows them and the verifier treats a finding that contradicts one as a false positive. Put them in `.codereview.yaml` (target branch):

```yaml
learnings:
  - text: Account numbers are compared without leading zeros on purpose.
  - text: Controllers under Legacy are frozen; do not suggest refactors.
    path: "api/app/Http/Controllers/Legacy/**"
```

or pass them in `CODEREVIEW_OVERRIDES` (`"learnings": [{"text": "...", "path": "..."}]`, at most 100, 600 characters each), which is how Divmora Code Review Console shares learnings across a group's repositories. `codereview learn` turns a human reply to a review comment into a learning: it reads `{"repo","path","finding","reply","author","existing"}` as JSON on stdin and prints `{"learning": true|false, "text", "path", "reason"}`.

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

### Review profiles

| Profile | Reports |
|---|---|
| `chill` | Likely bugs, security problems, data loss and serious performance issues only. No style or naming points. |
| `balanced` (default) | Bugs, security and performance, plus maintainability problems that will clearly cause trouble. Skips pure style preferences. |
| `assertive` | Everything above, plus readability, naming, missing tests and documentation gaps. Small polish items are marked as nitpicks. |

Set it in `.codereview.yaml` or with `--profile`. The profile also sets the default minimum severity and confidence for posted comments.

## CLI reference

| Command | Purpose |
|---|---|
| `review [URL]` | Review one PR/MR. Flags: `--local`, `--dir`, `--base`, `--post`, `--full`, `--auto`, `--format text\|markdown\|json\|sarif`, `--output`, `--fail-on`, `--profile`, `--max-comments`, `--min-severity`, `--commands`, `--score-labels`, `--commit-status`, `--verify-model` |
| `ask URL "question"` | Answer a question about a PR (`--post` to reply on it) |
| `models` | List providers, profiles and the active chain |
| `ping` | Test each model in the chain |
| `learn` | Turn a reply to a review comment into a team learning (JSON on stdin) |
| `config validate` | Check the config strictly (unknown keys, providers, model chain, verify model) and `CODEREVIEW_OVERRIDES`; `--format json` for programs |
| `version` | Print version |

Common flags: `--config`, `--model`, `--verbose`.

## Development

```bash
make build    # bin/codereview
make test     # go test -race ./...
make lint
```

Architecture and contributor notes are in [AGENTS.md](AGENTS.md); planned work is in [ROADMAP.md](ROADMAP.md).

## Contributing

- **Contributions:** see [CONTRIBUTING.md](CONTRIBUTING.md) for pull requests and issues.
- **Code of Conduct:** [Divmora Code of Conduct](https://github.com/divmora/.github/blob/main/CODE_OF_CONDUCT.md), inherited from the organisation.
- **Security:** report vulnerabilities privately as described in [SECURITY.md](SECURITY.md).

## License

Licensed under the **Apache License, Version 2.0**. It is free to use, modify and run in any environment, including commercial and production use, with no commercial license required. See [LICENSE](LICENSE).

[Divmora Code Review Console](https://divmora.github.io/code-review-console/) (webhooks, queue, admin UI) is a separate product under the Elastic License 2.0, with a free core and paid features unlocked by a license key.
