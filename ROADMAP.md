# Roadmap

Living backlog. Add items as they come up; remove them once implemented, tested and merged.
Active, assigned work belongs in GitHub Issues instead.

## CLI

- [ ] **Answer in review threads from CI**: run `ask` from a GitHub Actions `issue_comment` /
  `pull_request_review_comment` trigger.
- [ ] **Resolve fixed comments**: on new pushes, check earlier findings against the new code and
  resolve threads whose issue is fixed.

## Review quality

- [ ] **Cross-file context**: resolve symbols touched by the diff (callers/callees, interfaces)
  with tree-sitter or LSP and include them in the prompt.
- [ ] **Linter and SAST integration**: run configured tools (golangci-lint, eslint, semgrep,
  gitleaks) and let the model triage and explain their findings.
- [ ] **Two-pass verification**: an optional second model call that challenges each finding before
  posting, to cut false positives further.
- [ ] **Docstring and unit-test generation** commands.
- [ ] **Prompt caching** for providers that support it (Anthropic, Bedrock, Gemini, OpenAI) to cut
  the cost of shared system prompts across batches.

## Platforms

- [ ] **Bitbucket Cloud and Data Center**.
- [ ] **Azure DevOps** pull requests.
- [ ] **Google Vertex AI** (Gemini and partner models with service-account auth).
- [ ] **Native GitHub Action release binaries** instead of `go install` in the action.

## Operations

- [ ] **Cost guardrails**: a max-tokens-per-run setting and a cost estimate in the JSON output.

Webhooks, queues, admin UI, SSO, dashboards and metrics are tracked in Divmora Code Review Console.
