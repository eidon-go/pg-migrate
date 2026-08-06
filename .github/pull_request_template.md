<!--
The PR title becomes the squashed commit message, so it must follow
Conventional Commits — e.g. "fix(db): release the lock on context cancellation".
-->

## What this changes

<!-- One or two sentences. Link the issue it closes: "Closes #123". -->

## Why

<!-- The problem being solved. Skip if the title already says it. -->

## How it was verified

- [ ] `make check` passes locally
- [ ] Unit tests added or updated
- [ ] Integration tests added or updated (required for anything touching the
      advisory lock, the bookkeeping table, or `notransaction` scripts)

## Notes for the reviewer

<!-- Anything non-obvious: a trade-off you made, an approach you rejected. -->

---

- [ ] This is a breaking change (the title carries `!` or a `BREAKING CHANGE:` footer)
- [ ] Documentation updated (`README.md`, `docs/`, or doc comments)
