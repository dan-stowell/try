package main

import (
	"fmt"
	"html/template"
	"net"
	"net/http"
	"os/exec" // Added import
	"runtime"
)

// PageData holds data for the HTML template
type PageData struct {
	BranchName   string
	WorktreePath string
}

// StartWebServer starts a web server on an available port and serves the index page.
// It returns the URL of the server.
func StartWebServer(branchName, worktreePath string) (string, error) {
	// Find an available port
	listener, err := net.Listen("tcp", ":0") // Listen on port 0 to get a random available port
	if err != nil {
		return "", fmt.Errorf("failed to find an open port: %w", err)
	}
	// Do not close the listener here; it will be closed when http.Serve returns or on program exit.

	port := listener.Addr().(*net.TCPAddr).Port
	url := fmt.Sprintf("http://localhost:%d", port)

	// Define the HTML template
	const tmpl = `
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>{{.BranchName}}</title>
    <style>
        html, body { height: 100%; margin: 0; padding: 0; }
        body { display: flex; flex-direction: column; padding: 20px; box-sizing: border-box; }
        .header-container { background-color: #f0f0f0; padding: 10px 20px; margin: -20px -20px 20px -20px; } /* Neutral gray background for header */
        h1 { margin-top: 0; margin-bottom: 5px; font-size: 1.8em; } /* Slightly bigger font size */
        .worktree-path { font-size: 0.8em; color: #666; margin-bottom: 20px; }
        .content-area { flex-grow: 1; }
        form { display: flex; gap: 10px; width: 100%; margin-top: auto; }
        input[type="text"] { flex-grow: 1; }
    </style>
</head>
<body>
    <div class="header-container">
        <h1>{{.BranchName}}</h1>
        <div class="worktree-path">{{.WorktreePath}}</div>
    </div>
    <div class="content-area"></div>
    <form action="/" method="post">
        <input type="text" name="input_text" placeholder="Enter something to try..." style="width: 80%; padding: 15px; font-size: 1.2em;">
        <button type="submit" style="padding: 15px 30px; font-size: 1.2em;">Try</button>
    </form>
</body>
</html>
`
	t, err := template.New("index").Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("failed to parse template: %w", err)
	}

	// Set up the HTTP handler
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			// Handle form submission (for future use)
			fmt.Fprintf(w, "You submitted: %s", r.FormValue("input_text"))
		} else {
			data := PageData{BranchName: branchName, WorktreePath: worktreePath}
			t.Execute(w, data)
		}
	})

	// Start the server in a goroutine so it doesn't block
	go func() {
		// Use the listener we already created to serve
		if err := http.Serve(listener, nil); err != nil && err != http.ErrServerClosed {
			fmt.Printf("Web server error: %v\n", err)
		}
	}()

	return url, nil
}

// OpenBrowser opens the default web browser to the given URL.
func OpenBrowser(url string) error {
	var cmd string
	var args []string

	switch runtime.GOOS {
	case "windows":
		cmd = "cmd"
		args = []string{"/c", "start"}
	case "darwin":
		cmd = "open"
	default: // "linux", "freebsd", "openbsd", "netbsd"
		cmd = "xdg-open"
	}
	args = append(args, url)
	return exec.Command(cmd, args...).Start()
}
