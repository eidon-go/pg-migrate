# Versioning and compatibility

This project follows [Semantic Versioning](https://semver.org/). It is currently
in the `0.x` series.

## What 0.x means here

Under SemVer, major version zero is for initial development: the public API may
change. In practice this project treats it as follows.

| Change | Version bump | Example |
|---|---|---|
| Breaking change to the public API | **minor** — `0.1.0` → `0.2.0` | a function signature changes, an option is removed, an error stops being returned |
| New feature, backwards compatible | **minor** — `0.1.0` → `0.2.0` | a new `With…` option, a new entry point |
| Bug fix, no API change | **patch** — `0.1.0` → `0.1.1` | a rollback ordering fix, a parser fix |

The minor version carries both new features and breaking changes while in `0.x`
— that is what SemVer reserves it for below 1.0. Read the
[changelog](changelog.md) before a minor bump; breaking changes are always
called out there.

Pin a minor version if you want to opt into upgrades deliberately:

```
require github.com/eidon-go/pg-migrate v0.1.0
```

## What counts as the public API

Covered by the compatibility promise above:

- Every exported identifier in the root package: `Up`, `Reconcile`, `Down`,
  `DownAll`, `Plan`, `Status`, `Forget`, the `With…` options, `Result`,
  `Analysis`, `AppliedMigration`, and the sentinel errors.
- The **migration file format** — naming, the directive lines, statement
  markers. A change here would invalidate migrations already written.
- The **bookkeeping table schema**, in the sense that an older library version
  keeps working against a table a newer one created.
- The CLI's commands, flags and environment variables.

Not covered, and free to change in a patch release:

- Everything under `internal/`. It is unimportable by design.
- The exact wording of error messages. Match with `errors.Is` against the
  sentinels, never with string comparison.
- Log output — its content, level and structure.
- The `examples/` directory.

## Bookkeeping table compatibility

The table is created once and read by every later version. Columns will be added
over time, never removed or repurposed, so a database migrated by a newer
version stays readable by an older one. `seq` remains the ordering key.

If a future release ever needs a change that breaks that, it will ship as its
own migration step with a release note explaining it — never as a silent
`ALTER`.

## Toward 1.0

`1.0.0` will be tagged when the API has gone a few releases without needing a
breaking change and the feature set has settled. After that, breaking changes
require a major version, and — per the Go module rules — a `/v2` import path.

There is no timeline. A `0.x` that is honest about being `0.x` is more useful
than a `1.0` that has to break immediately.

## Supported versions

Security fixes go to the latest released minor version only. See
[SECURITY.md](https://github.com/eidon-go/pg-migrate/blob/main/SECURITY.md).

## Go and PostgreSQL

- **Go**: the two most recent stable releases, matching the Go team's own
  support window. Raising the minimum is a minor bump.
- **PostgreSQL**: 12 through 18, each exercised by the integration suite on
  every change. Dropping a version is a minor bump.
