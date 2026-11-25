# Security Policy

## Supported versions

Security fixes go into the latest minor release ([see Releases](https://github.com/divmora/code-review-bot/releases/latest)). Upgrade to it to get them; older releases are not patched.

## Published advisories

All fixed in **0.6.0**; versions before 0.6.0 are affected.

| Advisory | Severity | Summary |
|---|---|---|
| [GHSA-cvff-22gv-8fvr](https://github.com/divmora/code-review-bot/security/advisories/GHSA-cvff-22gv-8fvr) | High | Prompt injection can make the bot run GitLab quick actions (`/approve`, `/merge`, `/close`) as its user |
| [GHSA-jjwx-c8c6-f7pq](https://github.com/divmora/code-review-bot/security/advisories/GHSA-jjwx-c8c6-f7pq) | Medium | Any MR participant can forge the bot's state comment to block reviews, skip them, or hide findings |
| [GHSA-j7gf-g74c-wjq4](https://github.com/divmora/code-review-bot/security/advisories/GHSA-j7gf-g74c-wjq4) | Medium | An earlier bot approval stays when a later review can't decide |
| [GHSA-5554-qjg2-rm4f](https://github.com/divmora/code-review-bot/security/advisories/GHSA-5554-qjg2-rm4f) | Medium | Files the review skips don't stop `--approve` |
| [GHSA-jr6c-wqc7-cvjq](https://github.com/divmora/code-review-bot/security/advisories/GHSA-jr6c-wqc7-cvjq) | Low | Code host tokens could be sent to a host taken from the PR URL, or along a cross-host redirect |

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
- **Untrusted PRs**: PR content is passed to the model as data, and repo review rules are read from the base commit. Treat model output as advisory: the CLI comments, and never merges or pushes. It approves only when you pass `--approve` or set `review.approve.enabled` in the operator config, under the rule in `review.approve` (`block`, `min_score`); a repository's `.codereview.yaml` can't turn approvals on. Don't let the bot's approval alone satisfy required approvals on sensitive branches, and give its token no more rights than approving needs.
- **State key**: set `CODEREVIEW_STATE_KEY` (a long random secret, the same on every run; the console sets one per code host) so the bot signs what it posts and still recognises its own comments after the token changes to another user. Without it, only the token user's comments count.
- **Tokens stay on their host**: `GITLAB_TOKEN` is sent only to `GITLAB_URL`, `CI_SERVER_URL` or gitlab.com and `GITHUB_TOKEN` only to `GITHUB_API_URL`; configure every other host explicitly.
- **Forks**: don't expose provider keys to workflows triggered by forked PRs (`pull_request_target` with checkout of fork code is unsafe). The example workflow skips forks.
- **TLS**: `skip_tls_verify` exists only for self-managed GitLab with private CAs; prefer adding the CA to the image trust store.
- **Secrets in config**: use `${ENV_VAR}` references or `api_key_env` instead of literal keys, and keep `codereview.yaml` out of git (it is in `.gitignore`).
