package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

func main() {
	// Enable timestamped logging with microseconds
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: try <prompt>")
		os.Exit(2)
	}
	prompt := os.Args[1]

	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	start := time.Now()
	log.Printf("START %s", strings.Join(cmd.Args, " "))
	out, err := cmd.CombinedOutput()
	log.Printf("END   %s (%.3fs) status=%s", strings.Join(cmd.Args, " "), time.Since(start).Seconds(), map[bool]string{true: "error", false: "ok"}[err != nil])
	if err != nil || strings.TrimSpace(string(out)) != "true" {
		fmt.Fprintln(os.Stderr, "error: not in a git repository")
		os.Exit(1)
	}

	// Build branch name: try-YYYYMMDD-<8-char-hash>
	date := time.Now().Format("20060102")
	llmPrompt := fmt.Sprintf("Suggest a concise 2-3 word, lowercase, hyphen-separated Git branch name for this work: %q. Only output the branch name, no extra text.", prompt)
	cmd = exec.Command("llm", "-m", "gpt-5-nano-2025-08-07", llmPrompt)
	if v, ok := os.LookupEnv("OPENAI_API_KEY"); ok {
		cmd.Env = append(os.Environ(), "OPENAI_API_KEY="+v)
	}
	start = time.Now()
	log.Printf("START %s", strings.Join(cmd.Args, " "))
	out, err = cmd.CombinedOutput()
	log.Printf("END   %s (%.3fs) status=%s", strings.Join(cmd.Args, " "), time.Since(start).Seconds(), map[bool]string{true: "error", false: "ok"}[err != nil])
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to get branch name from llm: %v\n", err)
	}
	name := strings.ToLower(strings.TrimSpace(string(out)))
	name = strings.ReplaceAll(name, "_", "-")
	name = strings.ReplaceAll(name, " ", "-")
	reInvalid := regexp.MustCompile(`[^a-z0-9-]+`)
	name = reInvalid.ReplaceAllString(name, "")
	reHyphens := regexp.MustCompile(`-+`)
	name = reHyphens.ReplaceAllString(name, "-")
	name = strings.Trim(name, "-")
	if name == "" {
		name = "change"
	}
	branch := fmt.Sprintf("try-%s-%s", date, name)

	// git checkout -b <branch>
	cmd = exec.Command("git", "checkout", "-b", branch)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	start = time.Now()
	log.Printf("START %s", strings.Join(cmd.Args, " "))
	err = cmd.Run()
	log.Printf("END   %s (%.3fs) status=%s", strings.Join(cmd.Args, " "), time.Since(start).Seconds(), map[bool]string{true: "error", false: "ok"}[err != nil])
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to create and checkout branch %q: %v\n", branch, err)
		os.Exit(1)
	}

	// Invoke aider with the input prompt
	cmd = exec.Command("aider", "--model", "openai/gpt5", "--architect", "--yes-always", "--auto-commit", "--message", prompt)
	if v, ok := os.LookupEnv("OPENAI_API_KEY"); ok {
		cmd.Env = append(os.Environ(), "OPENAI_API_KEY="+v)
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	start = time.Now()
	log.Printf("START %s", strings.Join(cmd.Args, " "))
	err = cmd.Run()
	log.Printf("END   %s (%.3fs) status=%s", strings.Join(cmd.Args, " "), time.Since(start).Seconds(), map[bool]string{true: "error", false: "ok"}[err != nil])
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		fmt.Fprintf(os.Stderr, "error: failed to run aider: %v\n", err)
		os.Exit(1)
	}
}
