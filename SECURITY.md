# Security Policy

## Supported versions

Only the latest release and the `main` branch receive security fixes. If you
run a pinned image (`UPLOADER_TAG`), update to the newest one before reporting.

## Reporting a vulnerability

**Please do not open a public issue for security problems.**

Report vulnerabilities privately through GitHub's
[private vulnerability reporting](https://github.com/ariazonaa/uploader/security/advisories/new).

Include as much as you can:

- the affected version or commit
- a description of the issue and its impact
- steps to reproduce, or a proof of concept
- whether it needs an account (and which role) or works unauthenticated

You can expect a first response within a few days. Once the issue is confirmed,
a fix is prepared privately and released together with an advisory. Reporters
are credited unless they prefer to stay anonymous.

## Scope

In scope: the Go backend, the React dashboard, the server-rendered public pages
(`/s`, `/raw`, `/thumb`, `/hls`, `/p`, `/oembed`, `/d`), the Docker image and the
example deployment files in this repository.

Out of scope: vulnerabilities that require a compromised admin account or host,
missing hardening in a reverse proxy you configured yourself, and denial of
service through sheer request volume (rate limiting belongs in front of the app).
