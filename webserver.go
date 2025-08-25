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
	BranchName string
}

// StartWebServer starts a web server on an available port and serves the index page.
// It returns the URL of the server.
func StartWebServer(branchName string) (string, error) {
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
html, body { height: 100%; margin: 0; padding: 0; } /* Ensure html and body take full height */
body { font-family: sans-serif; padding: 2em; background-color: #f0f0f0; display: flex; flex-direction: column; }
h1 { color: #333; margin-bottom: 20px; text-align: left; }
.container { flex-grow: 1; display: flex; flex-direction: column; justify-content: space-between; max-width: 100%; margin: 0; padding: 0; box-sizing: border-box; } /* Add box-sizing */
        .form-container { display: flex; gap: 10px; margin-top: auto; align-items: flex-start; }
        input[type="text"] { flex-grow: 1; padding: 10px; border: 1px solid #ccc; border-radius: 4px; }
        button { padding: 10px 20px; background-color: #007bff; color: white; border: none; border-radius: 4px; cursor: pointer; flex-shrink: 0; }
        button:hover { background-color: #0056b3; }
    </style>
</head>
<body>
    <div class="container">
        <h1>{{.BranchName}}</h1>
        <form class="form-container" action="/" method="post">
            <input type="text" name="input_text" placeholder="Enter something to try...">
            <button type="submit">Try</button>
        </form>
    </div>
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
			data := PageData{BranchName: branchName}
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
