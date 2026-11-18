package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

//go:embed migrations/001_init.sql
var migrations embed.FS

func Open(ctx context.Context, dataDir string) (*sql.DB, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}

	path := filepath.Join(dataDir, "wisp.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if _, err := db.ExecContext(ctx, `PRAGMA journal_mode = WAL; PRAGMA foreign_keys = ON; PRAGMA busy_timeout = 5000; PRAGMA synchronous = NORMAL;`); err != nil {
		db.Close()
		return nil, fmt.Errorf("set sqlite pragmas: %w", err)
	}
	schema, err := migrations.ReadFile("migrations/001_init.sql")
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("read migration: %w", err)
	}
	if _, err := db.ExecContext(ctx, string(schema)); err != nil {
		db.Close()
		return nil, fmt.Errorf("run migration: %w", err)
	}
	return db, nil
}
