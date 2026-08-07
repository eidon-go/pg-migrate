# Changelog

All notable changes to this project are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.0](https://github.com/eidon-go/pg-migrate/releases/tag/v0.1.0) — 2026-08-07

### Features

- **cli**: Add a new command and rename the binary to pg-migrate by @sergeyslonimsky ([faddf7e](https://github.com/eidon-go/pg-migrate/commit/faddf7ec2d7cb6efa37579e00980d5831d7db599))

- PostgreSQL migration library and CLI by @sergeyslonimsky ([ef8be08](https://github.com/eidon-go/pg-migrate/commit/ef8be089b265f34149e44fe8693ac114d1791241))


### Documentation

- Explain migration ID schemes and the sequence width limit by @sergeyslonimsky ([0de1fdf](https://github.com/eidon-go/pg-migrate/commit/0de1fdffbd6d74471181ad068688af649d269305))

- Add documentation site, examples and project files by @sergeyslonimsky ([ccee470](https://github.com/eidon-go/pg-migrate/commit/ccee47009a263a0e6b3faf4b7a4b72cb0c128359))


### Testing

- Add integration suite and parser fuzzing by @sergeyslonimsky ([9ba4dcc](https://github.com/eidon-go/pg-migrate/commit/9ba4dcc8f6e04b38ed1dd05cfa7d3ea31dec2d22))


### Build System

- Pin actions by digest and sign releases with cosign by @sergeyslonimsky ([b2748ac](https://github.com/eidon-go/pg-migrate/commit/b2748acc56d183c0b5361b7ed077736cbb8ab8c7))

- Add goreleaser and git-cliff release pipeline by @sergeyslonimsky ([1161c46](https://github.com/eidon-go/pg-migrate/commit/1161c46615b77195cd86c2f2708c2aae946be1a0))


### CI

- Report integration coverage and test the config parser by @sergeyslonimsky ([7063441](https://github.com/eidon-go/pg-migrate/commit/70634419be53fd3b93b5aacad5b4643a0bd0f287))

- Allow CI to be started manually by @sergeyslonimsky ([f8ca028](https://github.com/eidon-go/pg-migrate/commit/f8ca0281e456927a6dad0b699141c194133269da))

- Generate the changelog before tagging, not from the release workflow by @sergeyslonimsky ([0327d99](https://github.com/eidon-go/pg-migrate/commit/0327d99f8563d2f2ea954916450978d42482e729))

- Add GitHub Actions workflows by @sergeyslonimsky ([a7cfe3a](https://github.com/eidon-go/pg-migrate/commit/a7cfe3a52d56d9e9254c162b2d507cb8957215f9))



