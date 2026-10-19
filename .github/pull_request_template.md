## What does this change?

<!-- A short description, and the issue it fixes (e.g. "Fixes #123"). -->

## How was it tested?

<!-- Commands you ran, manual steps, screenshots for UI changes. -->

## Checklist

- [ ] `gofmt -l .`, `go vet ./...` and `go test ./...` pass (backend)
- [ ] `GOOS=linux GOARCH=amd64 golangci-lint run` passes (backend)
- [ ] `npx prettier --check src` and `npm run build` pass (frontend)
- [ ] SQL or migration changes come with an integration test (`-tags integration`)
- [ ] New UI strings are in both `en.ts` and `de.ts`
