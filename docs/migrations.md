# Migration files

## Creating a migration

```bash
pg-migrate new add_users_table
```

```
migrations/20260806143022_add_users_table.up.sql
migrations/20260806143022_add_users_table.down.sql
```

The name is slugified — lowercased, with anything that is not an ASCII letter or
digit folded to a single underscore — and prefixed with a **UTC** timestamp.
UTC rather than local time so that a team spread across time zones cannot
generate IDs that sort in an order nobody intended.

The pair is written to `--migration-path` (or `MIGRATION_PATH`), the same
setting the other commands read from. The directory is created if it does not
exist, and an existing file is never overwritten.

## Naming

Each migration is a **pair** of files sharing a base name. That base name is the
migration ID.

```
migrations/
  20260115103000_create_users.up.sql
  20260115103000_create_users.down.sql
  20260116084500_add_email_index.up.sql
  20260116084500_add_email_index.down.sql
```

Rules the loader enforces:

- **Both halves are required.** A missing `.down.sql` is an error, not an empty
  rollback. If a migration genuinely cannot be undone, say so explicitly with the
  [`irreversible`](#irreversible) directive.
- **IDs sort lexicographically**, never numerically. `pg-migrate new` handles this
  for you; see [Why timestamps](#why-timestamps) if you name files by hand.
- **Non-`.sql` files are ignored.** A `.sql` file that is neither `*.up.sql` nor
  `*.down.sql` is an error rather than a silent skip.
- **Subdirectories are not traversed.** One flat directory.

## Why timestamps

IDs are opaque strings compared **byte by byte**, never numerically. That single
fact decides the naming scheme.

Sequence numbers (`0001_`, `0002_`) are readable and still work — the library
does not care — but they have two problems. Two people branching in parallel
pick the same number, and someone has to renumber. And the padding is a ceiling:

```
sorted: 10000_x  10001_x  9998_x  9999_x
```

After `9999`, the next ID sorts **before** everything already applied. Nothing is
corrupted — the migration is reported as
[interleaved](concepts.md#interleaved-migrations) rather than silently applied in
the wrong place — but every migration from then on stays permanently flagged, and
the warning stops carrying information.

A 14-digit timestamp has neither problem: it is always the same width, and two
people cannot collide unless they run `new` in the same second.

!!! tip "Migrating from sequence numbers needs no rewrite"

    A timestamp starts with `2`; a zero-padded number starts with `0`. Timestamps
    therefore sort after every existing sequence ID:

    ```
    sorted: 0001_seq  0002_seq  20260806120000_ts
    ```

    Start using `pg-migrate new` whenever you like. Already applied migrations keep
    their IDs, their stored rollback scripts, and their place in the order.

Timestamps do not avoid [interleaved migrations](concepts.md#interleaved-migrations)
— a branch created earlier but merged later still sorts before what is already
applied. No naming scheme fixes that, which is why the library reports it
instead.

## Directives

A file may begin with a single directive line. It must be the **very first
line** — a directive further down is an error, not a comment.

```sql
-- +migrate notransaction
```

Unknown, misspelled or wrongly-cased directives are rejected. `-- +migrate
NoTransaction` will not silently do nothing.

### notransaction

Runs the script statement-by-statement **outside** a transaction, for statements
PostgreSQL refuses inside one.

```sql title="20260210091500_email_index.up.sql"
-- +migrate notransaction
CREATE INDEX CONCURRENTLY idx_users_email ON users(email);
```

!!! danger "A partial failure stays partial"

    Without a transaction there is nothing to roll back. If statement 3 of 5
    fails, statements 1 and 2 are committed and the schema matches no migration.
    The failure is recorded on the migration's row, and every subsequent
    operation refuses until a human resolves it — see
    [Troubleshooting](troubleshooting.md#a-migration-is-recorded-as-failed).

All statements of one script run on a **single dedicated connection**, so
session state carries across them:

```sql
-- +migrate notransaction
SET statement_timeout = 0;
CREATE INDEX CONCURRENTLY idx_big_table ON big_table(col);
```

That connection's session is destroyed afterwards rather than returned to your
pool, so nothing a script sets can contaminate the application.

### irreversible

Belongs in a `.down.sql` that is deliberately empty:

```sql title="20260304140000_drop_legacy_table.down.sql"
-- +migrate irreversible
```

A rollback that would touch this migration is refused with `ErrIrreversible`
**before anything runs** — the check is a pre-flight pass over every candidate,
so you never end up half-rolled-back.

This exists to distinguish a migration that genuinely cannot be undone (a dropped
table, a destructive backfill) from a rollback script somebody forgot to write.
An empty `.down.sql` without the directive is an error.

## Statement splitting

`notransaction` scripts are split into statements before running. The scanner
understands:

- single-quoted literals, including `''` escapes
- `E'...'` escape strings, where `\` escapes the next character
- double-quoted identifiers, including `""` escapes
- dollar quoting — `$$...$$` and `$tag$...$tag$`
- line comments (`--`) and nestable block comments (`/* ... */`)

A semicolon inside any of those is not a boundary.

!!! note

    The scanner assumes `standard_conforming_strings` is on, which has been the
    default since PostgreSQL 9.1. `U&'...'` literals are not treated specially.

### Forcing a boundary

Where a boundary still cannot be inferred, mark the block explicitly:

```sql
-- +migrate notransaction
-- +migrate StatementBegin
CREATE OR REPLACE FUNCTION audit_users() RETURNS trigger AS $$
BEGIN
    INSERT INTO audit(table_name, op) VALUES ('users', TG_OP);
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +migrate StatementEnd
```

Everything between the markers becomes one statement regardless of what the
scanner would otherwise infer. The markers are matched against the whole trimmed
line, never as a substring, so a line merely mentioning them inside a string
literal is left alone.

## Rollback scripts live in the database

When a migration is applied, **both** scripts are stored in the bookkeeping
table. A rollback executes the stored `.down.sql`, not the file.

```mermaid
sequenceDiagram
    participant F as migrations/
    participant L as pg-migrate
    participant D as PostgreSQL

    F->>L: 0003.up.sql + 0003.down.sql
    L->>D: run up script
    L->>D: INSERT (id, up_script, down_script)
    Note over D: both scripts now stored

    Note over F: branch switched — files gone
    L->>D: SELECT down_script WHERE id = '0003'
    D-->>L: the script as stored at apply time
    L->>D: run it, then DELETE the row
```

Two consequences worth internalising:

- Rollback works when the files are gone. This is the point.
- **Editing an applied migration's file changes nothing.** Not the schema, not
  the stored rollback. To change applied schema, write a new migration.
