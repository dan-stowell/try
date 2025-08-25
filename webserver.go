package main

import (
	"fmt"
	"html/template"
	"net"
	"net/http"
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
	defer listener.Close() // Close the listener after we get the address

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
        body { font-family: sans-serif; margin: 2em; }
        h1 { color: #333; }
        .container { max-width: 600px; margin: 0 auto; padding: 20px; border: 1px solid #ccc; border-radius: 8px; box-shadow: 0 2px 4px rgba(0,0,0,0.1); }
        input[type="text"] { width: calc(100% - 22px); padding: 10px; margin-bottom: 10px; border: 1px solid #ccc; border-radius: 4px; }
        button { padding: 10px 20px; background-color: #007bff; color: white; border: none; border-radius: 4px; cursor: pointer; }
        button:hover { background-color: #0056b3; }
    </style>
</head>
<body>
    <div class="container">
        <h1>{{.BranchName}}</h1>
        <form action="/" method="post">
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
