# Contributing to SelfDeploy

Thanks for helping! Bug reports, docs fixes and pull requests are all welcome.

## Development

```bash
go test ./...
go run ./cmd/selfdeploy   # needs Docker; dashboard on http://localhost:8000
```

- The backend is Go (`internal/`), with SQLite storage and the Docker CLI for everything container related.
- The dashboard is plain HTML/CSS/JS in `web/static`, embedded into the binary. There is no Node build step.
- Run `gofmt`, `go vet ./...` and `go test ./...` before opening a pull request. CI runs the same checks.

## Pull requests

- Keep changes focused, and explain the problem and how you tested the fix.
- Add or update tests for behaviour changes.
- New one-click apps go in `internal/services/templates.go`. Use the official image and pin a major version.
