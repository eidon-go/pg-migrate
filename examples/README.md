# Examples

Runnable programs showing the library in context. Each is a `package main` you
can build and run against a real PostgreSQL.

| Example | Shows |
|---|---|
| [`basic/`](basic) | Migrations from a directory on disk, with `os.DirFS`. |
| [`embedded/`](embedded) | Migrations compiled into the binary with `embed.FS`. |
| [`deploy-gate/`](deploy-gate) | Using `Plan` to refuse a risky deployment before it starts. |

## Running them

Start a PostgreSQL — the repository's `docker-compose.yml` will do:

```bash
docker compose up -d --wait
# The compose file maps 5433 on the host, to stay out of the way of a local
# PostgreSQL on the default port.
export DATABASE_URL="postgres://postgres:postgres@localhost:5433/postgres?sslmode=disable"
```

Then:

```bash
go run ./examples/basic
go run ./examples/embedded
go run ./examples/deploy-gate
```

Each example creates its own bookkeeping table, so they do not interfere.
