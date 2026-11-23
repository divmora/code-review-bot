# Changelog

## [0.5.0](https://github.com/divmora/code-review-bot/compare/v0.4.1...v0.5.0) (2026-11-23)


### Features

* anchor scores to the previous review so they don't swing between runs ([#12](https://github.com/divmora/code-review-bot/issues/12)) ([f22ebdc](https://github.com/divmora/code-review-bot/commit/f22ebdcb3d8ffd62159ede5abcf4d3846b0a60bc)), closes [#9](https://github.com/divmora/code-review-bot/issues/9)
* approve pull requests when the review passes (--approve) ([#10](https://github.com/divmora/code-review-bot/issues/10)) ([da02e8f](https://github.com/divmora/code-review-bot/commit/da02e8f7a494485c584e2b60331977f06450caae))

## [0.4.1](https://github.com/divmora/code-review-bot/compare/v0.4.0...v0.4.1) (2026-11-22)


### Bug Fixes

* write logs to stdout when CODEREVIEW_RESULT_MARKER is set ([#7](https://github.com/divmora/code-review-bot/issues/7)) ([b10ed4d](https://github.com/divmora/code-review-bot/commit/b10ed4d36a46604b3fd2f19f55b5239b8e17d0fb))

## [0.4.0](https://github.com/divmora/code-review-bot/compare/v0.3.0...v0.4.0) (2026-11-19)


### ⚠ BREAKING CHANGES

* github.com/divmora/code-review-bot/pkg/{llm,config,review,app} are no longer importable. Run the CLI instead; pkg/vcs and pkg/diff remain.

### Features

* drop speculative findings, ask about loosened checks, back scores with findings ([bbda5dc](https://github.com/divmora/code-review-bot/commit/bbda5dc6123afa8f4291e88a3757193dc90742dc))
* make the CLI the only interface and add config validate ([ae5b0f0](https://github.com/divmora/code-review-bot/commit/ae5b0f09a805c994377dfa751751aa3b8137bed1))
* name the bot after the token's user by default ([a2a7467](https://github.com/divmora/code-review-bot/commit/a2a7467554112c20da0248e4fda9c0ffb000f112))
* optional multi-pass reviews; verify repeated findings ([a14704a](https://github.com/divmora/code-review-bot/commit/a14704aa1de5f22c3eb4ed6b299fc8134e782b74))
* team learnings and the learn command ([0af54b4](https://github.com/divmora/code-review-bot/commit/0af54b4e8c4bae6f64ca6ab7adefeb67663482b4))


### Bug Fixes

* skip closed and merged pull requests, keep the description when nothing was reviewed ([eaef244](https://github.com/divmora/code-review-bot/commit/eaef24442c4690d92c10b58fcc20ff7ed2fe8eeb))

## [0.3.0](https://github.com/divmora/code-review-bot/compare/v0.2.0...v0.3.0) (2026-11-19)


### Features

* support Bitbucket Cloud ([1c28e19](https://github.com/divmora/code-review-bot/commit/1c28e1964406fc7dee80ce75659844069c794ce6))

## [0.2.0](https://github.com/divmora/code-review-bot/compare/v0.1.0...v0.2.0) (2026-11-19)


### Features

* initial release of code-review-bot ([78a1cd7](https://github.com/divmora/code-review-bot/commit/78a1cd7a5e2ed9f81f3b803f79ae9d20315efb65))


### Bug Fixes

* satisfy golangci-lint v2 and pin examples to the first release ([30cd3ea](https://github.com/divmora/code-review-bot/commit/30cd3ea3d36a1917071680b5f0aaeae76dd467fc))
