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
