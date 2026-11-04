# Contributing

**Languages:** English | [Русский](CONTRIBUTING.ru.md)

## Build

Follow the quick start in [README.md](README.md). You need Go 1.25 or newer,
PostgreSQL 16 with PostGIS, and Node.js 20 or newer when you build the
dashboard.

## Tests

Run `go test ./...` before a pull request. Tests that need a database skip
themselves unless `PB_TEST_DSN` points at a disposable PostgreSQL instance.

For dashboard changes, run `make ui` when PostgreSQL is available. If you could
not run it, say so in the pull request.

## Branches and pull requests

- Branch from `main`.
- Do not commit `config/config.yaml`, `config/secrets.yaml`, `.env`, anything
  under `artifacts/`, or anything under `.a/`.
- Do not add machine hostnames, home-directory paths, or addresses from a
  private, link-local, or overlay network. Examples belong on `127.0.0.1` or
  documentation addresses from RFC 5737 (`192.0.2.0/24`).
- Use `127.0.0.1`, `localhost`, or `example.com` in examples.
- Update [README.md](README.md) and [README.ru.md](README.ru.md) together when
  user-visible behavior changes.
- Prefer [Conventional Commits](https://www.conventionalcommits.org/):
  `feat:`, `fix:`, `docs:`, `chore:`. Commit messages must not contain secrets
  or private hostnames.

## Code of conduct

See [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).
