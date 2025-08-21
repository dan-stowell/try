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
        <div class="actions">
          <button id="stopBtn" type="button">Stop</button>
          <span id="status">Running...</span>
        </div>
      </section>
      <pre id="out" class="llm-out"></pre>
      <form id="runForm" method="post" action="/run" style="display:none">
        <input type="hidden" name="org" value="{{.Org}}">
        <input type="hidden" name="repo" value="{{.Repo}}">
        <input type="hidden" name="prompt" value="{{.Prompt}}">
      </form>
      <script>
        (function(){
          var runForm = document.getElementById('runForm');
          var outEl = document.getElementById('out');
          var statusEl = document.getElementById('status');
          var stopBtn = document.getElementById('stopBtn');
          if (!runForm || !outEl) return;
          var controller = new AbortController();
          stopBtn.addEventListener('click', function(){
            stopBtn.disabled = true;
            statusEl.textContent = 'Stopping...';
            controller.abort();
          });
          statusEl.textContent = 'Running...';
          fetch('/run', { method: 'POST', body: new FormData(runForm), signal: controller.signal })
            .then(function(res){
              var reader = res.body.getReader();
              var dec = new TextDecoder();
              function read() {
                return reader.read().then(function(result){
                  if (result.done) return;
                  outEl.textContent += dec.decode(result.value, {stream:true});
                  outEl.scrollTop = outEl.scrollHeight;
                  return read();
                });
              }
              return read();
            })
            .catch(function(err){
              outEl.textContent += '\n[stream error] ' + err + '\n';
            })
            .finally(function(){
              statusEl.textContent = 'Done';
              stopBtn.disabled = true;
            });
        })();
      </script>
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
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline' 'self'; connect-src 'self'; form-action 'self'; base-uri 'none'")
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
	log.Printf("ensureRepoCloned: org=%s repo=%s dest=%s", org, repo, dest)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if pathExists(filepath.Join(dest, ".git")) {
		log.Printf("ensureRepoCloned: already cloned: %s", dest)
		return nil
	}
	if pathExists(dest) {
		log.Printf("ensureRepoCloned: removing existing path: %s", dest)
		_ = os.RemoveAll(dest)
	}
	return cloneRepo(ctx, org, repo)
}

func cloneRepo(ctx context.Context, org, repo string) error {
	log.Printf("cloneRepo: org=%s repo=%s", org, repo)
	dest := repoDirPath(org, repo)
	src := fmt.Sprintf("https://github.com/%s/%s.git", org, repo)
	attempts := [][]string{
		{"git", "clone", "--depth", "1", "--single-branch", "--branch", "main", src, dest},
		{"git", "clone", "--depth", "1", "--single-branch", "--branch", "master", src, dest},
		{"git", "clone", "--depth", "1", "--single-branch", src, dest},
	}
	for i, args := range attempts {
		log.Printf("cloneRepo: attempt %d: %v", i+1, args)
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		out, err := cmd.CombinedOutput()
		if err == nil {
			log.Printf("cloneRepo: success to %s", dest)
			return nil
		}
		_ = os.RemoveAll(dest)
		if i == len(attempts)-1 {
			log.Printf("cloneRepo: all attempts failed for %s/%s", org, repo)
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
	log.Printf("indexHandler: %s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)
	if r.Method != http.MethodGet {
		log.Printf("indexHandler: non-GET; redirecting to /")
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	setHTMLHeaders(w)
	_ = tpl.Execute(w, viewModel{Title: "Trybook"})
}

func tryHandler(w http.ResponseWriter, r *http.Request) {
	log.Printf("tryHandler: %s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)
	if r.Method != http.MethodPost {
		log.Printf("tryHandler: non-POST; redirecting to /")
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		log.Printf("tryHandler: ParseForm error: %v", err)
		setHTMLHeaders(w)
		_ = tpl.Execute(w, viewModel{Title: "Trybook", Message: "Invalid form submission.", MsgClass: "error"})
		return
	}
	input := strings.TrimSpace(r.FormValue("url"))
	log.Printf("tryHandler: input=%q", input)
	org, repo, err := parseRepoInput(input)
	if err != nil {
		log.Printf("tryHandler: parseRepoInput error: %v", err)
		setHTMLHeaders(w)
		_ = tpl.Execute(w, viewModel{Title: "Trybook", Message: err.Error(), MsgClass: "error"})
		return
	}
	if err := os.MkdirAll(*cloneDir, 0o755); err != nil {
		log.Printf("tryHandler: MkdirAll(%q) error: %v", *cloneDir, err)
		setHTMLHeaders(w)
		_ = tpl.Execute(w, viewModel{Title: "Trybook", Message: "Server cannot create clone dir.", MsgClass: "error"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	log.Printf("tryHandler: ensuring clone at %s", repoDirPath(org, repo))
	if err := ensureRepoCloned(ctx, org, repo); err != nil {
		log.Printf("tryHandler: ensureRepoCloned error: %v", err)
		setHTMLHeaders(w)
		_ = tpl.Execute(w, viewModel{Title: "Trybook", Message: "Clone failed: " + err.Error(), MsgClass: "error"})
		return
	}
	log.Printf("tryHandler: clone ready; redirecting to /r/%s/%s", org, repo)
	http.Redirect(w, r, "/r/"+org+"/"+repo, http.StatusSeeOther)
}

func repoHandler(w http.ResponseWriter, r *http.Request) {
	log.Printf("repoHandler: %s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)
	if r.Method != http.MethodGet {
		log.Printf("repoHandler: non-GET; redirecting to /")
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/r/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || !isSafeToken(parts[0]) || !isSafeToken(parts[1]) {
		log.Printf("repoHandler: invalid path %q; redirecting", r.URL.Path)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	vm := viewModel{
		Title: "Trybook - " + parts[0] + "/" + parts[1],
		Org:   parts[0],
		Repo:  parts[1],
	}
	setHTMLHeaders(w)
	log.Printf("repoHandler: render %s/%s", parts[0], parts[1])
	_ = repoTpl.Execute(w, vm)
}

func promptHandler(w http.ResponseWriter, r *http.Request) {
	log.Printf("promptHandler: %s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)
	if r.Method != http.MethodPost {
		log.Printf("promptHandler: non-POST; redirecting to /")
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		log.Printf("promptHandler: ParseForm error: %v", err)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	org := strings.TrimSpace(r.FormValue("org"))
	repo := strings.TrimSpace(r.FormValue("repo"))
	if !isSafeToken(org) || !isSafeToken(repo) {
		log.Printf("promptHandler: invalid org/repo: org=%q repo=%q", org, repo)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	log.Printf("promptHandler: org=%s repo=%s", org, repo)
	prompt := strings.TrimSpace(r.FormValue("prompt"))
	log.Printf("promptHandler: promptLen=%d", len(prompt))
	if prompt == "" {
		log.Printf("promptHandler: empty prompt")
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

	log.Printf("promptHandler: starting streaming for org=%s repo=%s", org, repo)
	vm := viewModel{
		Title:    "Trybook - " + org + "/" + repo,
		Org:      org,
		Repo:     repo,
		Prompt:   prompt,
		MsgClass: "ok",
	}
	setHTMLHeaders(w)
	_ = repoTpl.Execute(w, vm)
}

func runHandler(w http.ResponseWriter, r *http.Request) {
	log.Printf("runHandler: %s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)
	if r.Method != http.MethodPost {
		log.Printf("runHandler: non-POST; rejecting")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		log.Printf("runHandler: ParseForm error: %v", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	org := strings.TrimSpace(r.FormValue("org"))
	repo := strings.TrimSpace(r.FormValue("repo"))
	prompt := strings.TrimSpace(r.FormValue("prompt"))
	if !isSafeToken(org) || !isSafeToken(repo) || prompt == "" {
		log.Printf("runHandler: invalid input org=%q repo=%q promptLen=%d", org, repo, len(prompt))
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Prepare streaming response
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	f, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	// Inform client immediately
	_, _ = w.Write([]byte("Starting gemini...\n\n"))
	f.Flush()

	ctx := r.Context() // canceled when client aborts (Stop button)
	cmd := exec.CommandContext(ctx, "gemini", "--prompt", prompt)
	cmd.Dir = repoDirPath(org, repo)
	fw := flushWriter{w: w, f: f}
	cmd.Stdout = fw
	cmd.Stderr = fw

	log.Printf("runHandler: running `gemini --prompt` in %s", cmd.Dir)
	if err := cmd.Start(); err != nil {
		log.Printf("runHandler: start error: %v", err)
		_, _ = w.Write([]byte("error: failed to start gemini: " + err.Error() + "\n"))
		f.Flush()
		return
	}
	if err := cmd.Wait(); err != nil {
		log.Printf("runHandler: gemini exited with error: %v", err)
		_, _ = w.Write([]byte("\n[gemini exited with error: " + err.Error() + "]\n"))
		f.Flush()
		return
	}
	log.Printf("runHandler: gemini complete")
	_, _ = w.Write([]byte("\n[done]\n"))
	f.Flush()
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	log.Printf("healthHandler: %s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)
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
	mux.HandleFunc("/run", runHandler)
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
