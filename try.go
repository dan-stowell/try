package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	_ "modernc.org/sqlite" // Import for SQLite driver
)

func main() {
	// Initialize the database
	db, err := InitDB()
	if err != nil {
		fmt.Printf("Error initializing database: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	// Check if inside a Git repository
	_, err = exec.Command("git", "rev-parse", "--is-inside-work-tree").Output()
	if err != nil {
		fmt.Println("Error: 'try' must be run from within a Git project.")
		os.Exit(1)
	}

	// Get initial commit hash
	initialCommitBytes, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		fmt.Printf("Error getting initial commit: %v\n", err)
		os.Exit(1)
	}
	initialCommit := strings.TrimSpace(string(initialCommitBytes))

	// Get initial branch name
	initialBranchBytes, err := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		fmt.Printf("Error getting initial branch: %v\n", err)
		os.Exit(1)
	}
	initialBranch := strings.TrimSpace(string(initialBranchBytes))

	if len(os.Args) < 2 {
		fmt.Println("Usage: try \"something i would like to try\"")
		os.Exit(1)
	}

	input := os.Args[1]
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
	cmd := exec.Command("git", "worktree", "add", "-b", branchName, tempDir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err = cmd.Run()
	if err != nil {
		fmt.Printf("Error creating git worktree: %v\n", err)
		os.Exit(1)
	}

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
	serverURL, err := StartWebServer(branchName)
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
