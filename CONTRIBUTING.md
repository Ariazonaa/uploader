# Contributing

Thanks for your interest in improving the uploader! Bug reports, fixes and
well-scoped features are welcome.

## Before you start

- **Bugs:** open an issue using the bug report template. For security issues,
  follow [SECURITY.md](SECURITY.md) instead.
- **Features:** open an issue first and describe the use case. The project is
  deliberately small (a single Go binary plus Postgres, no Redis, no cloud
  storage backends), so some ideas may be declined to keep it that way.

## Development setup

You need Go 1.27, Node.js 22 or newer and PostgreSQL 13+ (or Docker for the database).

```sh
# 1. Database
docker compose -f docker-compose.yml -f docker-compose.dev.yml up -d postgres

# 2. Backend (terminal 1) — see README.md for the required UPLOADER_* variables
cd backend
go run ./cmd/uploader serve

# 3. Frontend dev server (terminal 2), proxies the API to localhost:8080
cd frontend
npm ci
npm run dev
```

## Checks

CI runs the same commands; please run them before opening a pull request.

Backend (`cd backend`):

```sh
gofmt -l .                                  # must print nothing
go vet ./...
go test ./...
GOOS=linux GOARCH=amd64 go build ./...      # the container target
GOOS=linux GOARCH=amd64 golangci-lint run   # lint the Linux-only files too
```

Changes to SQL or migrations should come with an integration test. They run
against a disposable PostgreSQL server:

```sh
UPLOADER_TEST_DATABASE_URL=postgres://user:pass@localhost:5432/postgres \
  go test -tags integration ./...
```

Frontend (`cd frontend`):

```sh
npx prettier --check src
npm run build        # tsc -b && vite build
```

## Conventions

- Backend comments are in English. Keep `SELECT` column lists in sync with
  their `Scan` calls, and end paginated `ORDER BY`s with an `id` tie-breaker.
- Runtime settings are edited in the admin dashboard, not through environment
  variables. Only infrastructure and secrets are configured via `UPLOADER_*`.
- Frontend: React 19, TanStack Query (hooks live in `src/lib/api/hooks.ts`),
  Tailwind v4 with the design tokens in `src/index.css`, icons from
  `lucide-react`. User-facing strings go through the i18n catalog
  (`src/lib/i18n/en.ts` and `de.ts`).
- Format with Prettier (`npx prettier --write`) before committing.

## Pull requests

- Keep each PR focused on one change and describe what it fixes and how you
  tested it.
- Add or update tests where it makes sense.
- By contributing you agree that your contribution is licensed under the
  project's [MIT license](LICENSE).
