# Contributing

Thanks for helping improve Code Review Bot. Bug reports, provider additions and docs fixes are all welcome.

## Prerequisites

- Go 1.27+
- git (used by `review --local` and its tests)
- Docker (optional, for image builds)

## Workflow

```bash
git clone https://github.com/divmora/code-review-bot.git
cd code-review-bot
make build        # bin/codereview
make test         # go test -race ./...  (no network or credentials needed)
make lint
```

Try a real review against your own provider:

```bash
export CODEREVIEW_MODEL=ollama:qwen2.5-coder:14b   # or any provider:model
./bin/codereview ping
./bin/codereview review --local
```

## Pull requests

1. Branch from `main`.
2. Keep changes focused; add or update tests (see the `httptest` patterns in `pkg/llm` and `pkg/app`).
3. Update `README.md`, `examples/` and `AGENTS.md` when behaviour, flags or config keys change.
4. Use a [Conventional Commits](https://www.conventionalcommits.org/) PR title (`feat:`, `fix:`, `docs:` ...). Release Please builds the changelog from it.

Read [AGENTS.md](AGENTS.md) for the architecture and the invariants every change must keep.
