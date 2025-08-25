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

	// Truncate to 24 characters
	if len(s) > 24 {
		s = s[:24]
	}

	// Remove leading/trailing hyphens
	s = strings.Trim(s, "-")

	return s
}
