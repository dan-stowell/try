package main

import (
	"context"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

var cloneDir = flag.String("clone-dir", "/tmp/clone", "directory to store clones")

const pageTpl = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>{{.Title}}</title>
  <style>
    :root { color-scheme: light; }
    body { margin:0; font-family: system-ui, -apple-system, Segoe UI, Roboto, Arial, sans-serif; display:flex; min-height:100vh; }
    main { margin:auto; width: min(90vw, 900px); }
    h1 { text-align:center; font-weight:600; }
    form { display:flex; gap:12px; flex-wrap:wrap; justify-content:center; }
    .url-input { flex: 1 1 700px; max-width: 800px; height:56px; font-size:1.1rem; padding:12px 14px; border-radius:8px; }
    button { height:56px; padding:0 20px; font-size:1rem; border-radius:8px; cursor:pointer; }
    .msg { margin-top:16px; text-align:center; }
  </style>
</head>
<body>
  <main>
    <h1>Trybook</h1>
    <form method="post" action="/try" novalidate>
      <input type="url" name="url" class="url-input" placeholder="Paste a GitHub URL..." required autofocus>
      <button type="submit">Open</button>
    </form>
    {{if .Message}}<p class="msg {{.MsgClass}}">{{.Message}}</p>{{end}}
  </main>
</body>
</html>`

var tpl = template.Must(template.New("page").Parse(pageTpl))

const repoPageTpl = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>{{.Title}}</title>
  <style>
    :root { color-scheme: light; }
    body { margin:0; font-family: system-ui, -apple-system, Segoe UI, Roboto, Arial, sans-serif; display:flex; min-height:100vh; }
    main { margin:auto; width: min(90vw, 900px); }
    h1 { text-align:center; font-weight:700; font-size: clamp(1.5rem, 5vw, 2.5rem); margin-bottom: 16px; }
    form { display:flex; flex-direction:column; gap:12px; }
    .prompt-input { width:100%; font-size:1rem; padding:12px 14px; border-radius:8px; resize: vertical; }
    .llm-out { white-space: pre-wrap; font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace; padding:12px 14px; border-radius:8px; overflow:auto; }
    .actions { display:flex; gap:12px; align-items:center; }
    button { height:44px; padding:0 20px; font-size:1rem; border-radius:8px; cursor:pointer; }
    a.link { text-decoration: none; padding: 10px 12px; border-radius: 8px; }
    .msg { margin-top:8px; text-align:left; }
  </style>
</head>
<body>
  <main>
    <h1>{{.Org}}/{{.Repo}}</h1>
    {{if .Prompt}}
      <section class="prompt-view">
        <textarea class="prompt-input" readonly rows="2">{{.Prompt}}</textarea>
      </section>
      {{if .ClaudeOut}}<pre class="llm-out">{{.ClaudeOut}}</pre>{{end}}
    {{end}}
    <form method="post" action="/prompt" novalidate>
      <input type="hidden" name="org" value="{{.Org}}">
      <input type="hidden" name="repo" value="{{.Repo}}">
      <textarea name="prompt" class="prompt-input" placeholder="Enter a prompt..." rows="2"></textarea>
      <div class="actions">
        <button type="submit">Run</button>
        <a class="link" href="/">Back</a>
      </div>
    </form>
    {{if .Message}}<p class="msg {{.MsgClass}}">{{.Message}}</p>{{end}}
  </main>
</body>
</html>`
var repoTpl = template.Must(template.New("repo").Parse(repoPageTpl))

type viewModel struct {
	Title     string
	Message   string
	MsgClass  string
	Org       string
	Repo      string
	Prompt    string
	ClaudeOut string
}

func setHTMLHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'")
}

func isSafeToken(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r == '-' || r == '_' || r == '.' ||
			(r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}

func parseRepoInput(s string) (string, string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", "", fmt.Errorf("empty input")
	}
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		u, err := url.Parse(s)
		if err != nil {
			return "", "", fmt.Errorf("invalid URL")
		}
		host := strings.ToLower(u.Host)
		if host != "github.com" {
			return "", "", fmt.Errorf("only github.com is supported")
		}
		p := strings.Trim(u.Path, "/")
		parts := strings.Split(p, "/")
		if len(parts) < 2 {
			return "", "", fmt.Errorf("URL must be like https://github.com/org/repo")
		}
		org := parts[0]
		repo := strings.TrimSuffix(parts[1], ".git")
		if !isSafeToken(org) || !isSafeToken(repo) {
			return "", "", fmt.Errorf("invalid org or repo")
		}
		return org, repo, nil
	}
	parts := strings.Split(s, "/")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("input must be org/repo or a full GitHub URL")
	}
	org := strings.TrimSpace(parts[0])
	repo := strings.TrimSpace(parts[1])
	if !isSafeToken(org) || !isSafeToken(repo) {
		return "", "", fmt.Errorf("invalid org or repo")
	}
	return org, repo, nil
}

func repoDirPath(org, repo string) string {
	return filepath.Join(*cloneDir, org, repo)
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func ensureRepoCloned(ctx context.Context, org, repo string) error {
	dest := repoDirPath(org, repo)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if pathExists(filepath.Join(dest, ".git")) {
		return nil
	}
	if pathExists(dest) {
		_ = os.RemoveAll(dest)
	}
	return cloneRepo(ctx, org, repo)
}

func cloneRepo(ctx context.Context, org, repo string) error {
	dest := repoDirPath(org, repo)
	src := fmt.Sprintf("https://github.com/%s/%s.git", org, repo)
	attempts := [][]string{
		{"git", "clone", "--depth", "1", "--single-branch", "--branch", "main", src, dest},
		{"git", "clone", "--depth", "1", "--single-branch", "--branch", "master", src, dest},
		{"git", "clone", "--depth", "1", "--single-branch", src, dest},
	}
	for i, args := range attempts {
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		out, err := cmd.CombinedOutput()
		if err == nil {
			return nil
		}
		_ = os.RemoveAll(dest)
		if i == len(attempts)-1 {
			return fmt.Errorf("git clone failed: %v\n%s", err, string(out))
		}
	}
	return nil
}

func isLikelyGitHubURL(s string) bool {
	u, err := url.Parse(s)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return false
	}
	host := strings.ToLower(u.Host)
	if host == "github.com" || strings.HasSuffix(host, ".github.com") {
		return true
	}
	return false
}

func indexHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	setHTMLHeaders(w)
	_ = tpl.Execute(w, viewModel{Title: "Trybook"})
}

func tryHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		setHTMLHeaders(w)
		_ = tpl.Execute(w, viewModel{Title: "Trybook", Message: "Invalid form submission.", MsgClass: "error"})
		return
	}
	input := strings.TrimSpace(r.FormValue("url"))
	org, repo, err := parseRepoInput(input)
	if err != nil {
		setHTMLHeaders(w)
		_ = tpl.Execute(w, viewModel{Title: "Trybook", Message: err.Error(), MsgClass: "error"})
		return
	}
	if err := os.MkdirAll(*cloneDir, 0o755); err != nil {
		setHTMLHeaders(w)
		_ = tpl.Execute(w, viewModel{Title: "Trybook", Message: "Server cannot create clone dir.", MsgClass: "error"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	if err := ensureRepoCloned(ctx, org, repo); err != nil {
		setHTMLHeaders(w)
		_ = tpl.Execute(w, viewModel{Title: "Trybook", Message: "Clone failed: " + err.Error(), MsgClass: "error"})
		return
	}
	http.Redirect(w, r, "/r/"+org+"/"+repo, http.StatusSeeOther)
}

func repoHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/r/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || !isSafeToken(parts[0]) || !isSafeToken(parts[1]) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	vm := viewModel{
		Title: "Trybook - " + parts[0] + "/" + parts[1],
		Org:   parts[0],
		Repo:  parts[1],
	}
	setHTMLHeaders(w)
	_ = repoTpl.Execute(w, vm)
}

func promptHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	org := strings.TrimSpace(r.FormValue("org"))
	repo := strings.TrimSpace(r.FormValue("repo"))
	if !isSafeToken(org) || !isSafeToken(repo) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	prompt := strings.TrimSpace(r.FormValue("prompt"))
	if prompt == "" {
		vm := viewModel{
			Title:    "Trybook - " + org + "/" + repo,
			Org:      org,
			Repo:     repo,
			Message:  "Please enter a prompt.",
			MsgClass: "error",
		}
		setHTMLHeaders(w)
		_ = repoTpl.Execute(w, vm)
		return
	}

	// Run `claude --print` in the repo directory, feeding the prompt on stdin.
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "claude", "--print")
	cmd.Dir = repoDirPath(org, repo)
	cmd.Stdin = strings.NewReader(prompt)
	out, err := cmd.CombinedOutput()

	vm := viewModel{
		Title:     "Trybook - " + org + "/" + repo,
		Org:       org,
		Repo:      repo,
		ClaudeOut: string(out),
		Prompt:    prompt,
		MsgClass:  "ok",
	}
	if err != nil {
		vm.Message = "claude failed: " + err.Error()
		vm.MsgClass = "error"
	}

	setHTMLHeaders(w)
	_ = repoTpl.Execute(w, vm)
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func newMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", indexHandler)
	mux.HandleFunc("/try", tryHandler)
	mux.HandleFunc("/r/", repoHandler)
	mux.HandleFunc("/prompt", promptHandler)
	mux.HandleFunc("/healthz", healthHandler)
	return mux
}

func main() {
	flag.Parse()
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	addr := ":" + port
	srv := &http.Server{
		Addr:         addr,
		Handler:      newMux(),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		log.Printf("Trybook listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	select {
	case sig := <-sigCh:
		log.Printf("signal received: %s; shutting down...", sig)
	case err := <-errCh:
		log.Printf("server error: %v; shutting down...", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("graceful shutdown failed: %v", err)
	}
	log.Println("bye")
}
