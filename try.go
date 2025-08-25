package main

import (
	"fmt"
	"os"
	"os/exec"
	"flag" // Added import for flag
	"path/filepath"
	"regexp"
	"strings"

	"database/sql" // Added import for database/sql
	"html/template"
	"net"
	"net/http"
	"runtime"

	_ "modernc.org/sqlite" // Import for SQLite driver
)

var repoPath string

// InitDB initializes the SQLite database and creates the 'tries' table if it doesn't exist.
func InitDB() (*sql.DB, error) {
	dbFilePath := filepath.Join(repoPath, ".trydb")
	db, err := sql.Open("sqlite", dbFilePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database %s: %w", dbFilePath, err)
	}

	createTableSQL := `
	CREATE TABLE IF NOT EXISTS tries (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		initial_commit TEXT NOT NULL,
		initial_branch TEXT NOT NULL,
		prompt TEXT NOT NULL,
		sanitized_branch_name TEXT NOT NULL,
		worktree_path TEXT NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);`

	_, err = db.Exec(createTableSQL)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to create table: %w", err)
	}

	return db, nil
}

// InsertTry inserts a new record into the 'tries' table.
func InsertTry(db *sql.DB, initialCommit, initialBranch, prompt, sanitizedBranchName, worktreePath string) error {
	insertSQL := `
	INSERT INTO tries (initial_commit, initial_branch, prompt, sanitized_branch_name, worktree_path)
	VALUES (?, ?, ?, ?, ?);`

	_, err := db.Exec(insertSQL, initialCommit, initialBranch, prompt, sanitizedBranchName, worktreePath)
	if err != nil {
		return fmt.Errorf("failed to insert try record: %w", err)
	}
	return nil
}

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

func main() {
	// Define and parse flags
	flag.StringVar(&repoPath, "repo", ".", "Path to the Git repository")
	flag.Parse()

	// Resolve the absolute path for the repository
	absRepoPath, err := filepath.Abs(repoPath)
	if err != nil {
		fmt.Printf("Error resolving repository path: %v\n", err)
		os.Exit(1)
	}
	repoPath = absRepoPath

	// Initialize the database
	db, err := InitDB()
	if err != nil {
		fmt.Printf("Error initializing database: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	// Check if inside a Git repository
	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	cmd.Dir = repoPath
	_, err = cmd.Output()
	if err != nil {
		fmt.Println("Error: 'try' must be run from within a Git project. Use --repo flag if not in CWD.")
		os.Exit(1)
	}

	// Get initial commit hash
	cmd = exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = repoPath
	initialCommitBytes, err := cmd.Output()
	if err != nil {
		fmt.Printf("Error getting initial commit: %v\n", err)
		os.Exit(1)
	}
	initialCommit := strings.TrimSpace(string(initialCommitBytes))

	// Get initial branch name
	cmd = exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = repoPath
	initialBranchBytes, err := cmd.Output()
	if err != nil {
		fmt.Printf("Error getting initial branch: %v\n", err)
		os.Exit(1)
	}
	initialBranch := strings.TrimSpace(string(initialBranchBytes))

	if len(flag.Args()) < 1 {
		fmt.Println("Usage: try [options] \"something i would like to try\"")
		flag.PrintDefaults()
		os.Exit(1)
	}

	input := flag.Args()[0]
	// Format the user input for the branch name prefix
	branchPrefix := formatToBranchName(input)

	// Create a temporary directory for the worktree, using the branchPrefix in the pattern
	// os.MkdirTemp will append a unique suffix
	tempDir, err := os.MkdirTemp("", branchPrefix+"-")
	if err != nil {
		fmt.Printf("Error creating temporary directory: %v\n", err)
		os.Exit(1)
	}

	// Use the base name of the temporary directory as the branch name
	// This ensures the branch name is unique and descriptive
	branchName := filepath.Base(tempDir)

	// Create a new git worktree
	// The worktree path is the full temporary directory path
	cmd = exec.Command("git", "worktree", "add", "-b", branchName, tempDir)
	cmd.Dir = repoPath // Execute git command in the specified repository path
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err = cmd.Run()
	if err != nil {
		fmt.Printf("Error creating git worktree: %v\n", err)
		os.Exit(1)
	}

	// Record the try in the database
	err = InsertTry(db, initialCommit, initialBranch, input, branchName, tempDir)
	if err != nil {
		fmt.Printf("Error recording try in database: %v\n", err)
		// Do not exit, as the worktree was already created successfully
	}

	fmt.Printf("Created worktree at %s on branch %s\n", tempDir, branchName)

	// Start the web server
	serverURL, err := StartWebServer(branchName, tempDir)
	if err != nil {
		fmt.Printf("Error starting web server: %v\n", err)
		// Do not exit, as the worktree was already created successfully
	} else {
		fmt.Printf("Web server started at %s\n", serverURL)
		// Open the browser
		if err := OpenBrowser(serverURL); err != nil {
			fmt.Printf("Error opening browser: %v\n", err)
		}
	}

	// Keep the main goroutine alive to serve the web page
	select {}
}

func formatToBranchName(s string) string {
	// Replace spaces of any kind with hyphens
	s = regexp.MustCompile(`\s+`).ReplaceAllString(s, "-")

	// Strip any punctuation (keep alphanumeric and hyphens)
	reg := regexp.MustCompile(`[^a-zA-Z0-9-]+`)
	s = reg.ReplaceAllString(s, "")

	// Convert to lowercase
	s = strings.ToLower(s)

	// Split into words
	words := strings.Fields(s)

	// Take at most the first 3 words
	if len(words) > 3 {
		words = words[:3]
	}

	// Join words with hyphens
	s = strings.Join(words, "-")

	// Truncate to 24 characters, preferring not to truncate words
	if len(s) > 24 {
		truncated := ""
		currentLength := 0
		for _, word := range words {
			if currentLength+len(word) <= 24 {
				if currentLength > 0 {
					truncated += "-"
				}
				truncated += word
				currentLength += len(word) + 1 // +1 for the hyphen
			} else {
				break
			}
		}
		s = truncated
	}

	// Remove leading/trailing hyphens that might result from truncation
	s = strings.Trim(s, "-")

	return s
}
