package main

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestSQLiteFTS5(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE VIRTUAL TABLE posts_fts USING fts5(title, body)`); err != nil {
		t.Fatalf("create FTS5 table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO posts_fts(title, body) VALUES (?, ?)`, "M0", "Wisp uses FTS5"); err != nil {
		t.Fatalf("insert indexed row: %v", err)
	}

	var title string
	if err := db.QueryRow(`SELECT title FROM posts_fts WHERE posts_fts MATCH ?`, "FTS5").Scan(&title); err != nil {
		t.Fatalf("query FTS5 index: %v", err)
	}
	if title != "M0" {
		t.Fatalf("got title %q, want %q", title, "M0")
	}
}
