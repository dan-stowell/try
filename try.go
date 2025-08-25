package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: try \"something i would like to try\"")
		os.Exit(1)
	}

	input := os.Args[1]
	branchName := formatToBranchName(input)
	fmt.Println(branchName)
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
