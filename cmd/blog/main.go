package main

import (
	"database/sql"
	"embed"
	"html/template"
	"log"
	"net/http"
	"os"

	_ "modernc.org/sqlite"
)

//go:embed templates/index.html
var templateFS embed.FS

type pageData struct {
	Title string
}

func main() {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		log.Fatal(err)
	}

	if _, err := db.Exec(`
		CREATE VIRTUAL TABLE posts_fts USING fts5(title, body);
		INSERT INTO posts_fts(title, body) VALUES ('M0 is working', 'Wisp uses SQLite FTS5.');
	`); err != nil {
		log.Fatal(err)
	}

	var title string
	if err := db.QueryRow(`SELECT title FROM posts_fts WHERE posts_fts MATCH ?`, "SQLite").Scan(&title); err != nil {
		log.Fatal(err)
	}
	log.Printf("FTS5 query matched: %s", title)

	tmpl := template.Must(template.ParseFS(templateFS, "templates/index.html"))
	http.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		if err := tmpl.Execute(w, pageData{Title: "Wisp"}); err != nil {
			log.Printf("render index: %v", err)
		}
	})

	addr := ":8080"
	if value := os.Getenv("BLOG_LISTEN"); value != "" {
		addr = value
	}
	log.Printf("listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
