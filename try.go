package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath" // Added import
	"regexp"
	"strings"
)

func main() {
	// Check if inside a Git repository
	_, err := exec.Command("git", "rev-parse", "--is-inside-work-tree").Output()
	if err != nil {
		fmt.Println("Error: 'try' must be run from within a Git project.")
		os.Exit(1)
	}

	if len(os.Args) < 2 {
		fmt.Println("Usage: try \"something i would like to try\"")
		os.Exit(1)
	}

	input := os.Args[1]
	// Create a temporary directory for the worktree
	tempDir, err := os.MkdirTemp("", "try-") // Use a generic prefix for the temp dir
	if err != nil {
		fmt.Printf("Error creating temporary directory: %v\n", err)
		os.Exit(1)
	}

	// Use the basename of the temporary directory as the branch name
	branchName := formatToBranchName(input)
	// Extract the base name of the temporary directory
	tempDirBase := filepath.Base(tempDir)
	branchName = fmt.Sprintf("%s-%s", branchName, tempDirBase)

	// Re-sanitize the combined branch name to ensure it meets all criteria
	branchName = formatToBranchName(branchName)

	// Create a new git worktree
	cmd := exec.Command("git", "worktree", "add", "-b", branchName, tempDir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err = cmd.Run()
	if err != nil {
		fmt.Printf("Error creating git worktree: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Created worktree at %s on branch %s\n", tempDir, branchName)
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
