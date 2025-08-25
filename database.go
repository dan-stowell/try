package main

import (
	"database/sql"
	"fmt"
	"os"

	_ "modernc.org/sqlite"
)

const dbPath = ".trydb"

// InitDB initializes the SQLite database and creates the 'tries' table if it doesn't exist.
func InitDB() (*sql.DB, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
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
