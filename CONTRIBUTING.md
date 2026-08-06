# Contributing

Thanks for taking the time. Bug reports, questions and pull requests are all
welcome.

## Getting set up

```bash
git clone https://github.com/eidon-go/pg-migrate.git
cd pg-migrate
go mod download
```

Integration tests need PostgreSQL. A `docker-compose.yml` is included:

```bash
make test-integration   # brings the container up, runs the tests, tears it down
```

## The loop

```bash
make lint               # golangci-lint, including the integration build tag
make test               # unit tests, no Docker needed
make test-race          # unit tests under the race detector
make test-integration   # integration tests against PostgreSQL
make fuzz               # fuzz the SQL and directive parsers
make check              # everything CI runs, locally
```

`make check` is the one to run before opening a PR — it mirrors the CI lint job
(golangci-lint, `go mod tidy -diff`, govulncheck, betteralign, nilaway) plus the
tests.

### Fuzzing

The statement scanner, the directive parser and identifier quoting are fuzzed,
because all three decide what SQL actually reaches the database. If you touch
`splitSQLStatements`, `ParseDirectiveHeader` or `quoteIdentifier`, run
`make fuzz` — `FUZZTIME=2m make fuzz` for a longer pass.

A failure writes the input to `internal/migrator/testdata/fuzz/`. **Commit that
file**: it becomes a regression case replayed by every later `make test`.

### PostgreSQL versions

CI runs the integration suite against PostgreSQL 12 through 18. To check one
locally, point the test variables at it:

```bash
docker run -d --name pg13 -e POSTGRES_PASSWORD=postgres -p 5434:5432 postgres:13-alpine
TEST_POSTGRES_HOST=localhost TEST_POSTGRES_PORT=5434 \
TEST_POSTGRES_USER=postgres TEST_POSTGRES_PASSWORD=postgres \
TEST_POSTGRES_ADMIN_DB=postgres \
  go test -tags=integration ./test/...
```

## Commit messages

This project uses [Conventional Commits](https://www.conventionalcommits.org/).
The changelog is generated from them by [git-cliff](https://git-cliff.org/), so a
commit that does not parse is silently left out of the release notes.

```
feat(migrator): support statement-level rollback markers
fix(db): release the advisory lock when the context is cancelled
docs: document the irreversible directive
```

Types in use: `feat`, `fix`, `perf`, `refactor`, `docs`, `test`, `build`, `ci`,
`chore`, `style`. A breaking change gets a `!` (`feat(api)!: ...`) or a
`BREAKING CHANGE:` footer.

## Code style

The linter is the style guide — `.golangci.yml` enables essentially every
upstream linter, with the exceptions documented inline. Notably:

- `t.Parallel()` at the top of every test, and `t.Context()` instead of
  `context.Background()`.
- Errors are wrapped with `fmt.Errorf("...: %w", err)`, and anything a caller
  might branch on gets a sentinel.
- Struct fields are laid out by `betteralign`; run `make align` if CI complains.

Formatting is `gofumpt` + `goimports` + `gci`, all driven by `make fmt`.

## Pull requests

- One logical change per PR.
- New behaviour comes with tests. Anything touching the lock, the bookkeeping
  table or `notransaction` scripts needs an integration test.
- CI must be green. The `CI OK` job is the one that gates merging.
- The PR title becomes the squashed commit message, so it also follows
  Conventional Commits.

## Reporting bugs

Open an issue with the version, the PostgreSQL version, and the smallest
migration pair that reproduces it. If the database ended up in a state you did
not expect, the output of `migrate status --json` is the most useful thing you
can attach.

## Security

Do not open a public issue for a vulnerability — see
[SECURITY.md](https://github.com/eidon-go/pg-migrate/blob/main/SECURITY.md).
(An absolute link, because this file is also rendered on the documentation site,
where `SECURITY.md` is not a page.)
