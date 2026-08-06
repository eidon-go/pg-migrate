# Security Policy

## Supported versions

While the project is pre-1.0, only the latest released minor version receives
security fixes.

| Version | Supported |
|---|---|
| latest `0.x` | ✅ |
| older `0.x` | ❌ |

## Reporting a vulnerability

**Please do not open a public issue.**

Report privately through GitHub's
[security advisories](https://github.com/eidon-go/pg-migrate/security/advisories/new),
which lets us discuss and fix the problem before it is public.

Useful to include: the affected version, what an attacker can achieve, and a
reproduction if you have one.

You can expect an acknowledgement within a few days. Once a fix is released, the
advisory is published with credit to the reporter unless you would rather stay
anonymous.

## Scope

This library executes SQL that the operator supplies, against a database the
operator controls. A migration script doing something destructive is not a
vulnerability in this project.

Things that **are** in scope:

- SQL injection through anything that is not a migration script — table names,
  schema names, migration IDs.
- The advisory lock failing to provide mutual exclusion, allowing two runs to
  apply migrations concurrently.
- A connection carrying session state back into the caller's pool after a
  `notransaction` script.
- Credentials leaking into logs or error messages.
