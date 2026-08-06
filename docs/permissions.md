# Database privileges

Migrations usually run as a superuser, and for most projects that is a
reasonable choice — the migration scripts themselves can need almost anything.
If you want a dedicated, less-privileged role, this is what the library itself
requires, separate from what your scripts do.

## What the library needs

| Operation | Privilege | Used by |
|---|---|---|
| Create the bookkeeping table | `CREATE` on the schema | first run of `Up`, `Reconcile`, `Down` |
| Read, write and delete its rows | `SELECT`, `INSERT`, `UPDATE`, `DELETE` on the table | every write operation |
| Resolve the table's existence | none beyond schema `USAGE` | `Plan`, `Status`, `TableExists` |
| Take the advisory lock | none — `pg_advisory_lock` is unrestricted | every write operation |

Advisory locks are deliberately unprivileged in PostgreSQL: any role that can
connect can take one. Nothing extra is needed to make the locking work.

## A dedicated migration role

```sql
CREATE ROLE app_migrator LOGIN PASSWORD '…';

-- Connect, and see the schema the bookkeeping table lives in.
GRANT CONNECT ON DATABASE app TO app_migrator;
GRANT USAGE  ON SCHEMA public TO app_migrator;

-- Create the bookkeeping table on the first run.
GRANT CREATE ON SCHEMA public TO app_migrator;
```

The migration scripts need whatever they need on top of that — `CREATE TABLE`,
`ALTER`, ownership of the objects they modify. There is no way around that: a
migration that adds a column has to be allowed to add it.

If the bookkeeping table already exists and was created by a different role,
grant access to it explicitly:

```sql
GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE migrations TO app_migrator;
```

## A read-only role for Plan and Status

`Plan` and `Status` never write, never take the lock, and never create
anything — a missing bookkeeping table simply means nothing has been applied
yet. That makes them safe to run from CI against production:

```sql
CREATE ROLE app_migration_reader LOGIN PASSWORD '…';

GRANT CONNECT ON DATABASE app TO app_migration_reader;
GRANT USAGE   ON SCHEMA public TO app_migration_reader;
GRANT SELECT  ON TABLE migrations TO app_migration_reader;
```

See [Recipes](recipes.md#gate-a-deployment-on-the-plan) for gating a deployment
on that.

!!! warning "Grant SELECT before it exists and it will fail"

    `GRANT ... ON TABLE migrations` errors if the table has not been created
    yet. Run the first migration with the writing role, then grant to the
    reader.

## Schema placement

By default the bookkeeping table is **unqualified** and resolved through the
connection's `search_path`. If the migration role and the application role have
different `search_path` settings, they may resolve it to different schemas —
one of which will look empty.

Pin it when more than one role is involved:

```go
migrate.WithSchema("public")
```

The library creates its table, not your schema. `WithSchema("infra")` requires
`infra` to exist already, with `CREATE` granted on it.

## What is never needed

- **Superuser.** Nothing the library does requires it.
- **`pg_advisory_lock` grants.** They do not exist; the function is open to all.
- **Ownership of the database.** Only `CREATE` on the one schema.
