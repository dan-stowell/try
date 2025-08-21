package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	_ "modernc.org/sqlite"
)

 // Base app directory; clones live under <dir>/clone.
func defaultAppDir() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return filepath.Join(h, ".trybook")
	}
	// Fallback if home not found
	return ".trybook"
}

var appDir = flag.String("dir", defaultAppDir(), "base directory for Trybook data")

func cloneBaseDir() string {
	return filepath.Join(*appDir, "clone")
}

func worktreeBaseDir() string {
	return filepath.Join(*appDir, "worktree")
}
func worktreeDirPath(org, repo, name string) string {
	return filepath.Join(worktreeBaseDir(), org, repo, name)
}

// trybook database lives under <dir>/trybook.db
func dbPath() string {
	return filepath.Join(*appDir, "trybook.db")
}

var db *sql.DB

func initDB() error {
	if err := os.MkdirAll(*appDir, 0o755); err != nil {
		return fmt.Errorf("create app dir: %w", err)
	}
	dsn := "file:" + dbPath() + "?cache=shared&_pragma=busy_timeout=5000&_journal_mode=WAL&_fk=1"
	var err error
	db, err = sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	if err := db.Ping(); err != nil {
		return fmt.Errorf("ping db: %w", err)
	}
	const schema = `
		CREATE TABLE IF NOT EXISTS clones (
			org        TEXT NOT NULL,
			repo       TEXT NOT NULL,
			branch     TEXT NOT NULL,
			commit_sha TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
			updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
			PRIMARY KEY (org, repo)
		);
		CREATE TABLE IF NOT EXISTS notebooks (
			id         TEXT PRIMARY KEY,
			org        TEXT NOT NULL,
			repo       TEXT NOT NULL,
			branch     TEXT NOT NULL,
			worktree   TEXT NOT NULL,
			commit_sha TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
		);
		CREATE TABLE IF NOT EXISTS notebook_entries (
			notebook_id TEXT NOT NULL,
			idx         INTEGER NOT NULL,
			prompt      TEXT NOT NULL,
			output      TEXT NOT NULL DEFAULT '',
			created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
			updated_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
			PRIMARY KEY (notebook_id, idx),
			FOREIGN KEY (notebook_id) REFERENCES notebooks(id) ON DELETE CASCADE
		);`
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("schema: %w", err)
	}
	_, _ = db.Exec(`ALTER TABLE notebook_entries ADD COLUMN output_claude TEXT NOT NULL DEFAULT ''`)
	_, _ = db.Exec(`ALTER TABLE notebook_entries ADD COLUMN intent TEXT NOT NULL DEFAULT ''`)
	return nil
}

func currentBranchAndCommit(ctx context.Context, dir string) (string, string, error) {
	bc := exec.CommandContext(ctx, "git", "rev-parse", "--abbrev-ref", "HEAD")
	bc.Dir = dir
	bOut, err := bc.Output()
	if err != nil {
		return "", "", fmt.Errorf("get branch: %w", err)
	}
	branch := strings.TrimSpace(string(bOut))
	cc := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	cc.Dir = dir
	cOut, err := cc.Output()
	if err != nil {
		return "", "", fmt.Errorf("get commit: %w", err)
	}
	sha := strings.TrimSpace(string(cOut))
	return branch, sha, nil
}

func genNotebookID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func createNotebook(ctx context.Context, org, repo string) (string, error) {
	cloneDir := repoDirPath(org, repo)

	id := genNotebookID()
	wtName := "nb-" + id
	wtDir := worktreeDirPath(org, repo, wtName)

	if err := os.MkdirAll(filepath.Dir(wtDir), 0o755); err != nil {
		return "", fmt.Errorf("create worktree parent dir: %w", err)
	}

	// git -C <clone> worktree add -b <wtName> <wtDir>
	cmd := exec.CommandContext(ctx, "git", "-C", cloneDir, "worktree", "add", "-b", wtName, wtDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("create worktree: %v\n%s", err, string(out))
	}

	branch, sha, err := currentBranchAndCommit(ctx, wtDir)
	if err != nil {
		return "", err
	}

	_, err = db.ExecContext(ctx, `
		INSERT INTO notebooks(id, org, repo, branch, worktree, commit_sha)
		VALUES(?, ?, ?, ?, ?, ?)
	`, id, org, repo, branch, wtName, sha)
	if err != nil {
		return "", fmt.Errorf("insert notebook: %w", err)
	}
	return id, nil
}

type nbListItem struct {
	ID          string
	Org         string
	Repo        string
	Branch      string
	CommitShort string
	CreatedAt   string
}

func listNotebooks(ctx context.Context) ([]nbListItem, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, org, repo, branch, commit_sha, created_at
		FROM notebooks
		ORDER BY created_at DESC
		LIMIT 100
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []nbListItem
	for rows.Next() {
		var it nbListItem
		var sha string
		if err := rows.Scan(&it.ID, &it.Org, &it.Repo, &it.Branch, &sha, &it.CreatedAt); err != nil {
			return nil, err
		}
		if len(sha) >= 7 {
			it.CommitShort = sha[:7]
		} else {
			it.CommitShort = sha
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

type notebookMeta struct {
	ID       string
	Org      string
	Repo     string
	Branch   string
	SHA      string
	Worktree string // new
}

func loadNotebook(ctx context.Context, id string) (notebookMeta, []entry, error) {
	var m notebookMeta
	err := db.QueryRowContext(ctx, `
		SELECT id, org, repo, branch, worktree, commit_sha
		FROM notebooks WHERE id = ?
	`, id).Scan(&m.ID, &m.Org, &m.Repo, &m.Branch, &m.Worktree, &m.SHA)
	if err != nil {
		return m, nil, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT idx, prompt, output, output_claude, intent
		FROM notebook_entries
		WHERE notebook_id = ?
		ORDER BY idx ASC
	`, id)
	if err != nil {
		return m, nil, err
	}
	defer rows.Close()
	var es []entry
	for rows.Next() {
		var idx int
		var e entry
		if err := rows.Scan(&idx, &e.Prompt, &e.Output, &e.OutputClaude, &e.Intent); err != nil {
			return m, nil, err
		}
		es = append(es, e)
	}
	return m, es, rows.Err()
}

func appendNotebookEntry(ctx context.Context, nbID, prompt string) (int, error) {
	var next int
	err := db.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(idx), -1) + 1 FROM notebook_entries WHERE notebook_id = ?
	`, nbID).Scan(&next)
	if err != nil {
		return -1, err
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO notebook_entries(notebook_id, idx, prompt)
		VALUES(?, ?, ?)
	`, nbID, next, prompt)
	if err != nil {
		return -1, err
	}
	return next, nil
}

func setNotebookEntryOutput(ctx context.Context, nbID string, idx int, out string) error {
	_, err := db.ExecContext(ctx, `
		UPDATE notebook_entries
		SET output = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%SZ','now')
		WHERE notebook_id = ? AND idx = ?
	`, out, nbID, idx)
	return err
}

func setNotebookEntryOutputForModel(ctx context.Context, nbID string, idx int, model, out string) error {
	col := "output"
	if strings.ToLower(model) == "claude" {
		col = "output_claude"
	}
	_, err := db.ExecContext(ctx, `
		UPDATE notebook_entries
		SET `+col+` = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%SZ','now')
		WHERE notebook_id = ? AND idx = ?
	`, out, nbID, idx)
	return err
}

func setNotebookEntryIntent(ctx context.Context, nbID string, idx int, intent string) error {
	intent = strings.ToLower(strings.TrimSpace(intent))
	if intent != "edit" && intent != "question" {
		intent = ""
	}
	_, err := db.ExecContext(ctx, `
		UPDATE notebook_entries
		SET intent = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%SZ','now')
		WHERE notebook_id = ? AND idx = ?
	`, intent, nbID, idx)
	return err
}

func recordClone(ctx context.Context, org, repo string) error {
	dir := repoDirPath(org, repo)
	branch, sha, err := currentBranchAndCommit(ctx, dir)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO clones(org, repo, branch, commit_sha)
		VALUES(?, ?, ?, ?)
		ON CONFLICT(org, repo) DO UPDATE SET
			branch = excluded.branch,
			commit_sha = excluded.commit_sha,
			updated_at = strftime('%Y-%m-%dT%H:%M:%SZ','now')
	`, org, repo, branch, sha)
	return err
}

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
      <section style="margin-top:24px">
        <h2 style="font-size:1.1rem">Notebooks</h2>
        <ul>
          {{range .Notebooks}}
            <li>
              <a href="/n/{{.ID}}">{{.Org}}/{{.Repo}}</a>
              <small> ({{.Branch}} @ {{.CommitShort}}) &middot; {{.CreatedAt}}</small>
            </li>
          {{else}}
            <li><em>No notebooks yet</em></li>
          {{end}}
        </ul>
      </section>
    <script>
      (function(){
        var form = document.querySelector('form[action="/try"]');
        if (!form) return;
        var input = form.querySelector('input[name="url"]');
        if (!input) return;
        input.addEventListener('keydown', function(e){
          if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') {
            e.preventDefault();
            if (form.requestSubmit) form.requestSubmit(); else form.submit();
          }
        });
      })();
    </script>
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
    .outbox { border: 1px solid #e5e7eb; background: #f9fafb; border-radius:8px; padding:10px 12px; margin:8px 0 16px; }
    .box-header { display:flex; align-items:center; justify-content:space-between; margin-bottom:6px; }
    .status-badge { font-size:0.9rem; color:#6b7280; }
    .status-badge.done { color:#16a34a; }
    .status-badge.thinking { color:#6b7280; }
    .toggle { height:28px; padding: 0 10px; font-size: 0.9rem; }
    .preview { white-space: pre-wrap; font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace; color:#374151; }
    .actions { display:flex; gap:12px; align-items:center; }
    button { height:44px; padding:0 20px; font-size:1rem; border-radius:8px; cursor:pointer; }
    a.link { text-decoration: none; padding: 10px 12px; border-radius: 8px; }
    .msg { margin-top:8px; text-align:left; }
    .outbox.gemini { border-color: #dbeafe; }
    .outbox.claude { border-color: #f3e8ff; }
    .model-tag { font-size:0.85rem; color:#6b7280; margin-right:8px; text-transform: uppercase; letter-spacing:.02em; }
    .outbox.aider { border-color: #fee2e2; }
  </style>
</head>
<body>
  <main>
    <h1>{{.Org}}/{{.Repo}}</h1>
    <p><small>Branch: {{.Branch}} &middot; Commit: <span id="commitShort">{{.CommitShort}}</span></small></p>
    {{range $i, $e := .Entries}}
      <section class="prompt-view">
        <textarea class="prompt-input" readonly rows="2">{{ $e.Prompt }}</textarea>
      </section>
  {{if and $.HasPending (eq $i $.PendingIdx)}}
    <!-- Pending entry: initially hide all model boxes; router will decide -->
    <div class="outbox aider" id="box-aider-{{$i}}" data-model="aider" data-i="{{$i}}" style="display:none">
      <div class="box-header">
        <span class="model-tag">aider</span>
        <span id="status-aider-{{$i}}" class="status-badge thinking">thinking</span>
        <button type="button" class="toggle" data-i="{{$i}}" data-model="aider">Expand</button>
      </div>
      <pre id="prev-aider-{{$i}}" class="preview">thinking</pre>
      <pre id="out-aider-{{$i}}" class="llm-out" hidden>{{ $e.Output }}</pre>
    </div>
    <div class="outbox claude" id="box-claude-{{$i}}" data-model="claude" data-i="{{$i}}" style="display:none">
      <div class="box-header">
        <span class="model-tag">claude</span>
        <span id="status-claude-{{$i}}" class="status-badge thinking">thinking</span>
        <button type="button" class="toggle" data-i="{{$i}}" data-model="claude">Expand</button>
      </div>
      <pre id="prev-claude-{{$i}}" class="preview">thinking</pre>
      <pre id="out-claude-{{$i}}" class="llm-out" hidden>{{ $e.OutputClaude }}</pre>
    </div>
    <div class="outbox gemini" id="box-gemini-{{$i}}" data-model="gemini" data-i="{{$i}}" style="display:none">
      <div class="box-header">
        <span class="model-tag">gemini</span>
        <span id="status-gemini-{{$i}}" class="status-badge thinking">thinking</span>
        <button type="button" class="toggle" data-i="{{$i}}" data-model="gemini">Expand</button>
      </div>
      <pre id="prev-gemini-{{$i}}" class="preview">thinking</pre>
      <pre id="out-gemini-{{$i}}" class="llm-out" hidden>{{ $e.Output }}</pre>
    </div>
  {{else if eq $e.Intent "edit"}}
    <!-- Completed edit entries show the Aider placeholder -->
    <div class="outbox aider" id="box-aider-{{$i}}" data-model="aider" data-i="{{$i}}">
      <div class="box-header">
        <span class="model-tag">aider</span>
        <span id="status-aider-{{$i}}" class="status-badge {{if $e.Output}}done{{else}}thinking{{end}}">
          {{if $e.Output}}done{{else}}thinking{{end}}
        </span>
        <button type="button" class="toggle" data-i="{{$i}}" data-model="aider">Expand</button>
      </div>
      <pre id="prev-aider-{{$i}}" class="preview">thinking</pre>
      <pre id="out-aider-{{$i}}" class="llm-out" hidden>{{ $e.Output }}</pre>
    </div>
  {{else}}
    <!-- Completed question entries show both models -->
    <div class="outbox claude" id="box-claude-{{$i}}" data-model="claude" data-i="{{$i}}">
      <div class="box-header">
        <span class="model-tag">claude</span>
        <span id="status-claude-{{$i}}" class="status-badge {{if $e.OutputClaude}}done{{else}}thinking{{end}}">
          {{if $e.OutputClaude}}done{{else}}thinking{{end}}
        </span>
        <button type="button" class="toggle" data-i="{{$i}}" data-model="claude">Expand</button>
      </div>
      <pre id="prev-claude-{{$i}}" class="preview">thinking</pre>
      <pre id="out-claude-{{$i}}" class="llm-out" hidden>{{ $e.OutputClaude }}</pre>
    </div>
    <div class="outbox gemini" id="box-gemini-{{$i}}" data-model="gemini" data-i="{{$i}}">
      <div class="box-header">
        <span class="model-tag">gemini</span>
        <span id="status-gemini-{{$i}}" class="status-badge {{if $e.Output}}done{{else}}thinking{{end}}">
          {{if $e.Output}}done{{else}}thinking{{end}}
        </span>
        <button type="button" class="toggle" data-i="{{$i}}" data-model="gemini">Expand</button>
      </div>
      <pre id="prev-gemini-{{$i}}" class="preview">thinking</pre>
      <pre id="out-gemini-{{$i}}" class="llm-out" hidden>{{ $e.Output }}</pre>
    </div>
  {{end}}
    {{end}}
    {{if .HasPending}}
      <div id="pending" class="actions">
        <button id="stopBtn" type="button">Stop</button>
        <span id="runStatus">Running...</span>
      </div>
      <form id="runForm" method="post" action="/run" style="display:none">
        <input type="hidden" name="nb" value="{{.NotebookID}}">
        <input type="hidden" name="idx" value="{{.PendingIdx}}">
      </form>
      <script>
        (function(){
          var runForm = document.getElementById('runForm');
          var pendingEl = document.getElementById('pending');
          var runStatusEl = document.getElementById('runStatus');
          var stopBtn = document.getElementById('stopBtn');
          var stickToBottom = true;
          window.addEventListener('scroll', function(){
            var nearBottom = (window.scrollY + window.innerHeight) >= (document.documentElement.scrollHeight - 40);
            stickToBottom = nearBottom;
          });
          if (!runForm) return;

          var controllers = {};
          var abortedAll = false;
          var remaining = 0; // will set to 2 if we start both models

          function refreshCommit(){
            fetch('/api/head?nb={{.NotebookID}}')
              .then(function(res){ return res.text(); })
              .then(function(txt){
                var el = document.getElementById('commitShort');
                if (el && txt) el.textContent = (txt || '').trim();
              })
              .catch(function(){ /* ignore */ });
          }

          function showNextPromptAndRemovePending(){
            refreshCommit();
            if (pendingEl && pendingEl.remove) { pendingEl.remove(); }
            else if (pendingEl) { pendingEl.style.display = 'none'; }
            var next = document.getElementById('nextPrompt');
            if (next) {
              next.style.display = '';
              var ta = next.querySelector('textarea');
              if (ta) ta.focus();
            }
            if (stopBtn) stopBtn.disabled = true;
          }

          function startModel(model){
            var outEl = document.getElementById('out-' + model + '-{{.PendingIdx}}');
            var prevEl = document.getElementById('prev-' + model + '-{{.PendingIdx}}');
            var boxStatusEl = document.getElementById('status-' + model + '-{{.PendingIdx}}');
            function updatePreview(txt){
              if (!prevEl) return;
              var t = txt || '';
              prevEl.textContent = t ? t.slice(-80) : 'thinking';
            }
            updatePreview(outEl ? outEl.textContent : '');

            var controller = new AbortController();
            controllers[model] = controller;

            var fd = new FormData(runForm);
            fd.append('model', model);
            var body = new URLSearchParams(fd);
            runStatusEl.textContent = 'Running...';
            fetch('/run', {
              method: 'POST',
              headers: { 'Content-Type': 'application/x-www-form-urlencoded;charset=UTF-8' },
              body: body.toString(),
              signal: controller.signal
            })
            .then(function(res){
              var reader = res.body.getReader();
              var dec = new TextDecoder();
              function read(){
                return reader.read().then(function(result){
                  if (result.done) return;
                  outEl.textContent += dec.decode(result.value, {stream:true});
                  updatePreview(outEl.textContent);
                  outEl.scrollTop = outEl.scrollHeight;
                  if (stickToBottom && outEl.scrollIntoView) outEl.scrollIntoView({block:'end'});
                  return read();
                });
              }
              return read();
            })
            .catch(function(err){
              if (boxStatusEl) { boxStatusEl.textContent = 'stopped'; boxStatusEl.className = 'status-badge'; }
              if (!abortedAll && outEl) {
                outEl.textContent += '\n[stream error] ' + err + '\n';
              }
            })
            .finally(function(){
              if (boxStatusEl && !abortedAll) {
                boxStatusEl.textContent = 'done';
                boxStatusEl.className = 'status-badge done';
              }
              remaining--;
              if (remaining === 0) {
                showNextPromptAndRemovePending();
              }
            });
          }

          function startRouter(){
            var controller = new AbortController();
            controllers['router'] = controller;
            runStatusEl.textContent = 'Thinking...';
            var fd = new FormData(runForm);
            fd.append('model', 'router');
            var body = new URLSearchParams(fd);
            var routerOut = '';
            fetch('/run', {
              method: 'POST',
              headers: { 'Content-Type': 'application/x-www-form-urlencoded;charset=UTF-8' },
              body: body.toString(),
              signal: controller.signal
            })
            .then(function(res){
              var reader = res.body.getReader();
              var dec = new TextDecoder();
              function read(){
                return reader.read().then(function(result){
                  if (result.done) return;
                  routerOut += dec.decode(result.value, {stream:true});
                  return read();
                });
              }
              return read();
            })
            .catch(function(err){
              if (!abortedAll) {
                routerOut += '\n[router error] ' + err + '\n';
              }
            })
            .finally(function(){
              if (abortedAll) {
                showNextPromptAndRemovePending();
                return;
              }
              var s = (routerOut || '').toLowerCase();
              var decision = 'question';
              if (s.indexOf('edit') >= 0 && s.indexOf('question') < 0) decision = 'edit';
              if (s.trim() === 'edit') decision = 'edit';
              if (decision === 'edit') {
                // Show Aider box and start streaming
                var ba = document.getElementById('box-aider-{{.PendingIdx}}');
                if (ba) ba.style.display = '';
                var st = document.getElementById('status-aider-{{.PendingIdx}}');
                if (st) { st.textContent = 'thinking'; st.className = 'status-badge thinking'; }
                remaining = 1;
                startModel('aider');
              } else {
                // Show model boxes and start both
                var bc = document.getElementById('box-claude-{{.PendingIdx}}');
                var bg = document.getElementById('box-gemini-{{.PendingIdx}}');
                if (bc) bc.style.display = '';
                if (bg) bg.style.display = '';
                remaining = 2;
                startModel('claude');
                startModel('gemini');
              }
            });
          }

          stopBtn.addEventListener('click', function(){
            abortedAll = true;
            stopBtn.disabled = true;
            runStatusEl.textContent = 'Stopping...';
            Object.keys(controllers).forEach(function(k){
              try { controllers[k].abort(); } catch(e){}
            });
            // Mark any visible boxes as stopped
            ['claude','gemini','aider'].forEach(function(m){
              var el = document.getElementById('status-' + m + '-{{.PendingIdx}}');
              if (el) { el.textContent = 'stopped'; el.className = 'status-badge'; }
            });
            showNextPromptAndRemovePending();
          });

          // Kick off router first
          startRouter();
        })();
      </script>
    {{end}}
    <form id="nextPrompt" method="post" action="/prompt" novalidate{{if .HasPending}} style="display:none"{{end}}>
      <input type="hidden" name="nb" value="{{.NotebookID}}">
      <textarea name="prompt" class="prompt-input" placeholder="Enter a prompt..." rows="2"></textarea>
      <div class="actions">
        <button type="submit">Run</button>
        <a class="link" href="/">Back</a>
      </div>
    </form>
    <script>
      (function(){
        var form = document.getElementById('nextPrompt');
        if (!form) return;
        var ta = form.querySelector('textarea[name="prompt"]');
        if (!ta) return;
        ta.addEventListener('keydown', function(e){
          if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') {
            e.preventDefault();
            if (form.requestSubmit) form.requestSubmit(); else form.submit();
          }
        });
      })();
    </script>
    <script>
      (function(){
        function updatePreviewFor(model, i){
          var out = document.getElementById('out-' + model + '-' + i);
          var prev = document.getElementById('prev-' + model + '-' + i);
          if (!out || !prev) return;
          var txt = out.textContent || '';
          prev.textContent = txt ? txt.slice(-80) : 'thinking';
        }
        document.querySelectorAll('.outbox').forEach(function(box){
          var i = box.getAttribute('data-i');
          var model = box.getAttribute('data-model');
          if (i && model) updatePreviewFor(model, i);
        });
        document.querySelectorAll('.outbox .toggle').forEach(function(btn){
          btn.addEventListener('click', function(){
            var i = btn.getAttribute('data-i');
            var model = btn.getAttribute('data-model');
            var out = document.getElementById('out-' + model + '-' + i);
            var prev = document.getElementById('prev-' + model + '-' + i);
            if (!out || !prev) return;
            var hidden = out.hasAttribute('hidden');
            if (hidden) {
              out.removeAttribute('hidden');
              prev.style.display = 'none';
              btn.textContent = 'Collapse';
            } else {
              out.setAttribute('hidden', 'hidden');
              prev.style.display = '';
              btn.textContent = 'Expand';
              updatePreviewFor(model, i);
            }
          });
        });
      })();
    </script>
    {{if .Message}}<p class="msg {{.MsgClass}}">{{.Message}}</p>{{end}}
  </main>
</body>
</html>`
var repoTpl = template.Must(template.New("repo").Parse(repoPageTpl))

type viewModel struct {
	Title       string
	Message     string
	MsgClass    string
	Org         string
	Repo        string
	NotebookID  string
	Branch      string
	CommitShort string
	Notebooks   []nbListItem
	Entries     []entry
	PendingIdx  int  // index of the entry currently running; -1 if none
	HasPending  bool // true if there is a pending entry to run
}

func setHTMLHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline' 'self'; connect-src 'self'; form-action 'self'; base-uri 'none'")
}

type flushWriter struct {
	w http.ResponseWriter
	f http.Flusher
}

func (fw flushWriter) Write(p []byte) (int, error) {
	n, err := fw.w.Write(p)
	fw.f.Flush()
	return n, err
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
	return filepath.Join(cloneBaseDir(), org, repo)
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

// In-memory notebook

type entry struct {
	Prompt       string
	Output       string
	OutputClaude string
	Intent       string
}

var (
	notesMu sync.Mutex
	notes   = make(map[string]map[string][]entry) // sessionID -> "org/repo" -> entries
)

func repoKey(org, repo string) string { return org + "/" + repo }

func getSessionID(w http.ResponseWriter, r *http.Request) string {
	const ck = "tb"
	if c, err := r.Cookie(ck); err == nil && c.Value != "" {
		return c.Value
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		b = []byte(fmt.Sprintf("%d", time.Now().UnixNano()))
	}
	id := hex.EncodeToString(b)
	http.SetCookie(w, &http.Cookie{
		Name:     ck,
		Value:    id,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   86400 * 7,
	})
	return id
}

func getEntries(session, org, repo string) []entry {
	notesMu.Lock()
	defer notesMu.Unlock()
	m := notes[session]
	if m == nil {
		return nil
	}
	k := repoKey(org, repo)
	src := m[k]
	dst := make([]entry, len(src))
	copy(dst, src)
	return dst
}

func appendEntry(session, org, repo, prompt string) int {
	notesMu.Lock()
	defer notesMu.Unlock()
	m := notes[session]
	if m == nil {
		m = make(map[string][]entry)
		notes[session] = m
	}
	k := repoKey(org, repo)
	m[k] = append(m[k], entry{Prompt: prompt})
	return len(m[k]) - 1
}

func setEntryOutput(session, org, repo string, idx int, out string) {
	notesMu.Lock()
	defer notesMu.Unlock()
	m := notes[session]
	if m == nil {
		return
	}
	k := repoKey(org, repo)
	if idx >= 0 && idx < len(m[k]) {
		m[k][idx].Output = out
	}
}

func indexHandler(w http.ResponseWriter, r *http.Request) {
	log.Printf("indexHandler: %s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)
	if r.Method != http.MethodGet {
		log.Printf("indexHandler: non-GET; redirecting to /")
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	setHTMLHeaders(w)
	nbs, err := listNotebooks(r.Context())
	if err != nil {
		log.Printf("indexHandler: listNotebooks error: %v", err)
	}
	_ = tpl.Execute(w, viewModel{Title: "Trybook", Notebooks: nbs})
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
	if err := os.MkdirAll(cloneBaseDir(), 0o755); err != nil {
		log.Printf("tryHandler: MkdirAll(%q) error: %v", cloneBaseDir(), err)
		setHTMLHeaders(w)
		_ = tpl.Execute(w, viewModel{Title: "Trybook", Message: "Server cannot create clone dir.", MsgClass: "error"})
		return
	}
	if err := os.MkdirAll(worktreeBaseDir(), 0o755); err != nil {
		log.Printf("tryHandler: MkdirAll(%q) error: %v", worktreeBaseDir(), err)
		setHTMLHeaders(w)
		_ = tpl.Execute(w, viewModel{Title: "Trybook", Message: "Server cannot create worktree dir.", MsgClass: "error"})
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
	if err := recordClone(ctx, org, repo); err != nil {
		log.Printf("tryHandler: recordClone error: %v", err)
	}
	nbID, err := createNotebook(ctx, org, repo)
	if err != nil {
		log.Printf("tryHandler: createNotebook error: %v", err)
		setHTMLHeaders(w)
		_ = tpl.Execute(w, viewModel{Title: "Trybook", Message: "Failed to create notebook.", MsgClass: "error"})
		return
	}
	log.Printf("tryHandler: clone ready; redirecting to /n/%s", nbID)
	http.Redirect(w, r, "/n/"+nbID, http.StatusSeeOther)
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
	sid := getSessionID(w, r)
	entries := getEntries(sid, parts[0], parts[1])
	vm := viewModel{
		Title:      "Trybook - " + parts[0] + "/" + parts[1],
		Org:        parts[0],
		Repo:       parts[1],
		Entries:    entries,
		PendingIdx: -1,
		HasPending: false,
	}
	setHTMLHeaders(w)
	log.Printf("repoHandler: render %s/%s", parts[0], parts[1])
	_ = repoTpl.Execute(w, vm)
}

func notebookHandler(w http.ResponseWriter, r *http.Request) {
	log.Printf("notebookHandler: %s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)
	if r.Method != http.MethodGet {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/n/")
	if id == "" || !isSafeToken(id) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	meta, entries, err := loadNotebook(r.Context(), id)
	if err != nil {
		log.Printf("notebookHandler: load error: %v", err)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	pendingIdx := -1
	if p := r.URL.Query().Get("pending"); p != "" {
		if i, err := strconv.Atoi(p); err == nil {
			pendingIdx = i
		}
	}
	vm := viewModel{
		Title:       "Trybook - " + meta.Org + "/" + meta.Repo,
		Org:         meta.Org,
		Repo:        meta.Repo,
		Branch:      meta.Branch,
		CommitShort: func() string { if len(meta.SHA) >= 7 { return meta.SHA[:7] } else { return meta.SHA } }(),
		Entries:     entries,
		PendingIdx:  pendingIdx,
		HasPending:  pendingIdx >= 0,
		NotebookID:  meta.ID,
	}
	setHTMLHeaders(w)
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
	nbID := strings.TrimSpace(r.FormValue("nb"))
	if !isSafeToken(nbID) {
		log.Printf("promptHandler: invalid notebook id: %q", nbID)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	prompt := strings.TrimSpace(r.FormValue("prompt"))
	if prompt == "" {
		log.Printf("promptHandler: empty prompt")
		meta, entries, err := loadNotebook(r.Context(), nbID)
		if err != nil {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		vm := viewModel{
			Title:      "Trybook - " + meta.Org + "/" + meta.Repo,
			Org:        meta.Org,
			Repo:       meta.Repo,
			Branch:     meta.Branch,
			NotebookID: nbID,
			Message:    "Please enter a prompt.",
			MsgClass:   "error",
			Entries:    entries,
			PendingIdx: -1,
		}
		setHTMLHeaders(w)
		_ = repoTpl.Execute(w, vm)
		return
	}
	idx, err := appendNotebookEntry(r.Context(), nbID, prompt)
	if err != nil {
		log.Printf("promptHandler: appendNotebookEntry error: %v", err)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/n/"+nbID+"?pending="+strconv.Itoa(idx)+"#pending", http.StatusSeeOther)
	return
}

func runHandler(w http.ResponseWriter, r *http.Request) {
	log.Printf("runHandler: %s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)
	if r.Method != http.MethodPost {
		log.Printf("runHandler: non-POST; rejecting")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	log.Printf("runHandler: content-type=%q", r.Header.Get("Content-Type"))
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "multipart/form-data") {
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			log.Printf("runHandler: ParseMultipartForm error: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
	} else {
		if err := r.ParseForm(); err != nil {
			log.Printf("runHandler: ParseForm error: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
	}
	// Debug keys parsed
	keys := make([]string, 0, len(r.Form))
	for k := range r.Form {
		keys = append(keys, k)
	}
	log.Printf("runHandler: parsed form keys=%v", keys)
	nbID := strings.TrimSpace(r.FormValue("nb"))
	idxStr := strings.TrimSpace(r.FormValue("idx"))
	idx, err := strconv.Atoi(idxStr)
	if err != nil || !isSafeToken(nbID) {
		log.Printf("runHandler: bad nb/idx: nb=%q idx=%q err=%v", nbID, idxStr, err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	model := strings.TrimSpace(r.FormValue("model"))
	if model == "" {
		model = "gemini"
	}
	if model != "gemini" && model != "claude" && model != "router" && model != "aider" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	// Load notebook meta
	meta, _, err := loadNotebook(r.Context(), nbID)
	if err != nil {
		log.Printf("runHandler: loadNotebook error: %v", err)
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	// Load prompt
	var prompt string
	if err := db.QueryRowContext(r.Context(), `
		SELECT prompt FROM notebook_entries WHERE notebook_id = ? AND idx = ?
	`, nbID, idx).Scan(&prompt); err != nil {
		log.Printf("runHandler: load prompt error: %v", err)
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
	_, _ = w.Write([]byte("Starting " + model + "...\n\n"))
	f.Flush()

	ctx := r.Context() // canceled when client aborts (Stop button)
	var cmd *exec.Cmd
	if model == "gemini" {
		cmd = exec.CommandContext(ctx, "gemini", "--prompt", prompt)
	} else if model == "claude" {
		cmd = exec.CommandContext(ctx, "claude", "--print")
		cmd.Stdin = strings.NewReader(prompt)
	} else if model == "aider" {
		cmd = exec.CommandContext(ctx, "aider",
			"--model", "openai/gpt-5",
			"--architect",
			"--yes-always",
			"--auto-commit",
			"--auto-accept-architect",
			"--message", prompt,
		)
	} else { // router
		questionPrompt := "Is the following prompt asking an informational question or requesting edits to the code? Please respond 'question' or 'edit' and nothing else: " + prompt
		cmd = exec.CommandContext(ctx, "llm", "--model", "gpt-5-nano", questionPrompt)
	}
	cmd.Dir = worktreeDirPath(meta.Org, meta.Repo, meta.Worktree)
	// Ensure API keys are available to the child process
	if model == "gemini" {
		if key := os.Getenv("GEMINI_API_KEY"); key != "" {
			cmd.Env = append(os.Environ(), "GEMINI_API_KEY="+key)
		} else {
			cmd.Env = os.Environ()
			log.Printf("runHandler: warning: GEMINI_API_KEY not set")
		}
	} else if model == "claude" {
		if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
			cmd.Env = append(os.Environ(), "ANTHROPIC_API_KEY="+key)
		} else {
			cmd.Env = os.Environ()
			log.Printf("runHandler: warning: ANTHROPIC_API_KEY not set")
		}
	} else if model == "aider" {
		if key := os.Getenv("OPENAI_API_KEY"); key != "" {
			cmd.Env = append(os.Environ(), "OPENAI_API_KEY="+key)
		} else {
			cmd.Env = os.Environ()
			log.Printf("runHandler: warning: OPENAI_API_KEY not set")
		}
	} else { // router
		if key := os.Getenv("OPENAI_API_KEY"); key != "" {
			cmd.Env = append(os.Environ(), "OPENAI_API_KEY="+key)
		} else {
			cmd.Env = os.Environ()
			log.Printf("runHandler: warning: OPENAI_API_KEY not set")
		}
	}
	var buf bytes.Buffer
	fw := flushWriter{w: w, f: f}
	mw := io.MultiWriter(&buf, fw)
	cmd.Stdout = mw
	cmd.Stderr = mw

	log.Printf("runHandler: running model=%s in %s", model, cmd.Dir)
	if err := cmd.Start(); err != nil {
		log.Printf("runHandler: %s start error: %v", model, err)
		_, _ = w.Write([]byte("error: failed to start " + model + ": " + err.Error() + "\n"))
		f.Flush()
		return
	}
	if err := cmd.Wait(); err != nil {
		log.Printf("runHandler: %s exited with error: %v", model, err)
		_ = setNotebookEntryOutputForModel(r.Context(), nbID, idx, model, buf.String())
		_, _ = w.Write([]byte("\n[" + model + " exited with error: " + err.Error() + "]\n"))
		f.Flush()
		return
	}
	if model == "router" {
		// Parse decision and persist intent
		s := strings.ToLower(strings.TrimSpace(buf.String()))
		intent := ""
		if s == "edit" || strings.HasPrefix(s, "edit") {
			intent = "edit"
		} else if s == "question" || strings.HasPrefix(s, "question") {
			intent = "question"
		}
		if err := setNotebookEntryIntent(r.Context(), nbID, idx, intent); err != nil {
			log.Printf("runHandler: set intent error: %v", err)
		}
		// No output column for router; still write trailing [done] for client
		_, _ = w.Write([]byte("\n[done]\n"))
		f.Flush()
		log.Printf("runHandler: %s complete", model)
		return
	}
	log.Printf("runHandler: %s complete", model)
	_ = setNotebookEntryOutputForModel(r.Context(), nbID, idx, model, buf.String())
	_, _ = w.Write([]byte("\n[done]\n"))
	f.Flush()
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	log.Printf("healthHandler: %s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func nbHeadHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	nbID := strings.TrimSpace(r.URL.Query().Get("nb"))
	if !isSafeToken(nbID) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	meta, _, err := loadNotebook(r.Context(), nbID)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	ctx := r.Context()
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--short=7", "HEAD")
	cmd.Dir = worktreeDirPath(meta.Org, meta.Repo, meta.Worktree)
	out, err := cmd.Output()
	if err != nil {
		http.Error(w, "error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(strings.TrimSpace(string(out))))
}

func newMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", indexHandler)
	mux.HandleFunc("/try", tryHandler)
	mux.HandleFunc("/r/", repoHandler)
	mux.HandleFunc("/n/", notebookHandler)
	mux.HandleFunc("/prompt", promptHandler)
	mux.HandleFunc("/run", runHandler)
	mux.HandleFunc("/api/head", nbHeadHandler)
	mux.HandleFunc("/healthz", healthHandler)
	return mux
}

func main() {
	flag.Parse()
	if err := initDB(); err != nil {
		log.Fatalf("initDB: %v", err)
	}
	defer func() { if db != nil { _ = db.Close() } }()
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	addr := ":" + port
	srv := &http.Server{
		Addr:         addr,
		Handler:      newMux(),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 0, // no write timeout; needed for streaming
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
