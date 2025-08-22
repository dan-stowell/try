# Trybook

A single-file, stdlib-only Go web app. Open http://localhost:8080 to paste a GitHub URL into one large input field.

Run:
- PORT=8080 go run ./main.go
- or:
  - go build -o trybook ./main.go
  - ./trybook

Health:
- GET /healthz returns 200 OK

Notes:
- No external dependencies; stdlib only.
- Page is fully self-contained; no static files.

Usage notes:
- Enter a GitHub URL or org/repo (for example, golang/go).
- Trybook stores clones under <clone-dir>/<org>/<repo>.
- By default, clone-dir is /tmp/clone. Override with:
  - go run ./main.go -clone-dir=/path/to/dir
- Cloning is shallow: --single-branch --depth 1. It attempts branch main, then master, then the default branch.
- Requires git to be available in PATH.
- Requires gemini CLI in PATH (used via: gemini --prompt). The output is streamed to the page; click Stop to cancel a running request.
