package main

import (
	"context"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const pageTpl = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>{{.Title}}</title>
  <style>
    :root { color-scheme: light dark; }
    body { margin:0; font-family: system-ui, -apple-system, Segoe UI, Roboto, Arial, sans-serif; display:flex; min-height:100vh; }
    main { margin:auto; width: min(90vw, 900px); }
    h1 { text-align:center; font-weight:600; }
    form { display:flex; gap:12px; flex-wrap:wrap; justify-content:center; }
    .url-input { flex: 1 1 700px; max-width: 800px; height:56px; font-size:1.1rem; padding:12px 14px; border-radius:8px; border:1px solid #bbb; }
    button { height:56px; padding:0 20px; font-size:1rem; border-radius:8px; border:1px solid #888; cursor:pointer; }
    .msg { margin-top:16px; text-align:center; }
    .msg.error { color: #b00020; }
    .msg.ok { color: #0b7a0b; }
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

type viewModel struct {
	Title    string
	Message  string
	MsgClass string
}

func setHTMLHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'")
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
	urlStr := strings.TrimSpace(r.FormValue("url"))
	if urlStr == "" || !isLikelyGitHubURL(urlStr) {
		setHTMLHeaders(w)
		_ = tpl.Execute(w, viewModel{Title: "Trybook", Message: "Please enter a valid GitHub URL.", MsgClass: "error"})
		return
	}
	setHTMLHeaders(w)
	_ = tpl.Execute(w, viewModel{Title: "Trybook", Message: "Got URL: " + urlStr, MsgClass: "ok"})
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
	mux.HandleFunc("/healthz", healthHandler)
	return mux
}

func main() {
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
