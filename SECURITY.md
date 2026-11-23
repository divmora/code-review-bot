# Security Policy

## Supported versions

Security fixes go into the latest minor release (currently **0.5.x**). Upgrade to it to get them; older releases are not patched.

## Reporting a vulnerability

Please **do not open a public issue**. Report privately:

- Email `security@divmora.com`, or
- open a private advisory at [github.com/divmora/code-review-bot/security/advisories/new](https://github.com/divmora/code-review-bot/security/advisories/new).

Include a description, impact, reproduction steps and any suggested fix. We acknowledge within 48 hours, triage within 5 business days, and coordinate disclosure with you.

## Operating the bot safely

- **What leaves your network**: changed files, their diffs and (for files up to `full_file_max_lines`) their full content are sent to the configured model provider. Pick a provider and data-retention terms that fit your code's sensitivity, or run a local model (Ollama, vLLM).
- **Least-privilege tokens**:
  - GitHub token in Actions: `contents: read`, `pull-requests: write`.
  - GitLab: a dedicated bot user or project/group access token with `api` scope, Developer role.
  - Bitbucket Cloud: a repository access token with Pull requests: Write and Repositories: Read (add Pipelines/build status write for `--commit-status`).
- **Untrusted PRs**: PR content is passed to the model as data, and repo review rules are read from the base commit. Treat model output as advisory: the CLI comments, and never merges or pushes. It approves only when you pass `--approve` (0.5.0 and later), under the rule you set with `--approve-block` and `--approve-min-score`. Don't let the bot's approval alone satisfy required approvals on sensitive branches, and give its token no more rights than approving needs.
- **Forks**: don't expose provider keys to workflows triggered by forked PRs (`pull_request_target` with checkout of fork code is unsafe). The example workflow skips forks.
- **TLS**: `skip_tls_verify` exists only for self-managed GitLab with private CAs; prefer adding the CA to the image trust store.
- **Secrets in config**: use `${ENV_VAR}` references or `api_key_env` instead of literal keys, and keep `codereview.yaml` out of git (it is in `.gitignore`).
