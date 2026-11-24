# Changelog

## [0.7.0](https://github.com/divmora/code-review-bot/compare/v0.6.1...v0.7.0) (2026-11-24)


### Features

* optional flow diagram of the changed flow in the walkthrough ([#36](https://github.com/divmora/code-review-bot/issues/36)) ([7c49ca9](https://github.com/divmora/code-review-bot/commit/7c49ca963c3e501c8afadbf3c546afaa7427b5b8)), closes [#34](https://github.com/divmora/code-review-bot/issues/34)

## [0.6.1](https://github.com/divmora/code-review-bot/compare/v0.6.0...v0.6.1) (2026-11-23)


### Bug Fixes

* name the web host when the API is reached under another name ([#31](https://github.com/divmora/code-review-bot/issues/31)) ([d0c9d96](https://github.com/divmora/code-review-bot/commit/d0c9d9698202ae2f88b5788120e43d113e30e8b6))

## [0.6.0](https://github.com/divmora/code-review-bot/compare/v0.5.0...v0.6.0) (2026-11-23)


### Features

* only fixed findings raise scores; tell fixed from dismissed ([#24](https://github.com/divmora/code-review-bot/issues/24)) ([dfda305](https://github.com/divmora/code-review-bot/commit/dfda3051e425014229db5b0e326b9480a8eb2632)), closes [#15](https://github.com/divmora/code-review-bot/issues/15)


### Bug Fixes

* give existing score labels their band colour ([#18](https://github.com/divmora/code-review-bot/issues/18)) ([1d97934](https://github.com/divmora/code-review-bot/commit/1d979349ebe634a81ac13abc6e183ff98cde2e22))
* read the walkthrough leniently and don't guess scores when it fails ([#16](https://github.com/divmora/code-review-bot/issues/16)) ([e5a3f06](https://github.com/divmora/code-review-bot/commit/e5a3f06739565901ccd13ad971a0588e404f990d)), closes [#14](https://github.com/divmora/code-review-bot/issues/14)
* **security:** files the review skipped keep the bot from approving ([#27](https://github.com/divmora/code-review-bot/issues/27)) ([7bef50a](https://github.com/divmora/code-review-bot/commit/7bef50ae8cbd768ab85d06faade89ef49d8a4387)), closes [#20](https://github.com/divmora/code-review-bot/issues/20)
* **security:** neutralise GitLab quick actions and mentions in posted text ([#25](https://github.com/divmora/code-review-bot/issues/25)) ([ed3455f](https://github.com/divmora/code-review-bot/commit/ed3455f1175498152a7bc9687ae93f8fe8776caa))
* **security:** read back only the bot's own (or signed) comments as its state ([#28](https://github.com/divmora/code-review-bot/issues/28)) ([d26071f](https://github.com/divmora/code-review-bot/commit/d26071f48fb295d585c32368942e6239af67fc80))
* **security:** send tokens only to their configured host; refuse cross-host redirects ([#29](https://github.com/divmora/code-review-bot/issues/29)) ([4932c78](https://github.com/divmora/code-review-bot/commit/4932c7864952247d3414326008caaf44a67375ba)), closes [#23](https://github.com/divmora/code-review-bot/issues/23)
* **security:** withdraw the bot's approval when a new commit can't be judged ([#26](https://github.com/divmora/code-review-bot/issues/26)) ([0de525e](https://github.com/divmora/code-review-bot/commit/0de525e0e9693f8bcfadee641d9819e3abf9b8e5)), closes [#22](https://github.com/divmora/code-review-bot/issues/22)

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
