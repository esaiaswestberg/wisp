package main

import (
	"context"
	"database/sql"
	"embed"
	"html/template"
	"log"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"wisp/internal/db"
)

//go:embed templates/*.html static/style.css
var assets embed.FS

type site struct {
	Name        string
	Description string
	Language    string
	PerPage     int
}

type post struct {
	ID          int64
	Slug        string
	Title       string
	Summary     string
	BodyHTML    template.HTML
	PublishedAt string
}

type tag struct {
	Slug string
	Name string
}

type pageData struct {
	Site       site
	Posts      []post
	Post       post
	Tags       []tag
	Tag        tag
	Page       int
	Pages      int
	HasPrev    bool
	HasNext    bool
	PrevURL    string
	NextURL    string
	CurrentURL string
}

type app struct {
	db    *sql.DB
	tmpl  *template.Template
	site  site
	clock func() time.Time
}

func main() {
	ctx := context.Background()
	dataDir := os.Getenv("BLOG_DATA_DIR")
	if dataDir == "" {
		dataDir = "./data"
	}
	database, err := db.Open(ctx, dataDir)
	if err != nil {
		log.Fatal(err)
	}
	defer database.Close()

	tmpl := template.Must(template.ParseFS(assets, "templates/*.html"))
	application := &app{db: database, tmpl: tmpl, clock: time.Now}
	if err := application.loadSite(ctx); err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/static/style.css", application.style)
	mux.HandleFunc("/", application.route)
	addr := os.Getenv("BLOG_LISTEN")
	if addr == "" {
		addr = ":8080"
	}
	log.Printf("listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func (a *app) loadSite(ctx context.Context) error {
	var values = map[string]string{}
	rows, err := a.db.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return err
		}
		values[key] = value
	}
	a.site = site{Name: values["site_name"], Description: values["site_description"], Language: values["language"], PerPage: 10}
	if n, err := strconv.Atoi(values["posts_per_page"]); err == nil && n > 0 && n <= 50 {
		a.site.PerPage = n
	}
	return rows.Err()
}

func (a *app) route(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" {
		a.index(w, r, "", "")
		return
	}
	if r.URL.Path == "/tags" {
		a.tags(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/tags/") {
		a.index(w, r, strings.TrimPrefix(r.URL.Path, "/tags/"), "tag")
		return
	}
	slug := strings.TrimPrefix(r.URL.Path, "/posts/")
	if strings.HasPrefix(r.URL.Path, "/posts/") && slug != "" && !strings.Contains(slug, "/") {
		a.post(w, r, slug)
		return
	}
	http.NotFound(w, r)
}

func (a *app) index(w http.ResponseWriter, r *http.Request, tagSlug, kind string) {
	page := queryPage(r)
	where := `p.status = 'published' AND p.published_at IS NOT NULL AND p.published_at <= ?`
	args := []any{a.clock().UTC().Format(time.RFC3339)}
	baseURL := "/"
	var currentTag tag
	if kind == "tag" {
		where += ` AND EXISTS (SELECT 1 FROM post_tags pt JOIN tags t ON t.id = pt.tag_id WHERE pt.post_id = p.id AND t.slug = ?)`
		args = append(args, tagSlug)
		baseURL = "/tags/" + tagSlug
		if err := a.db.QueryRow(`SELECT slug, name FROM tags WHERE slug = ?`, tagSlug).Scan(&currentTag.Slug, &currentTag.Name); err != nil {
			http.NotFound(w, r)
			return
		}
	}
	var total int
	if err := a.db.QueryRow(`SELECT count(*) FROM posts p WHERE `+where, args...).Scan(&total); err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	pages := (total + a.site.PerPage - 1) / a.site.PerPage
	if pages == 0 {
		pages = 1
	}
	if page > pages {
		http.NotFound(w, r)
		return
	}
	args = append(args, a.site.PerPage, (page-1)*a.site.PerPage)
	rows, err := a.db.Query(`SELECT p.id, p.slug, p.title, p.summary, p.body_html, p.published_at FROM posts p WHERE `+where+` ORDER BY p.published_at DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	posts, err := scanPosts(rows)
	rows.Close()
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	data := pageData{Site: a.site, Posts: posts, Tag: currentTag, Page: page, Pages: pages, HasPrev: page > 1, HasNext: page < pages, CurrentURL: baseURL}
	if data.HasPrev {
		data.PrevURL = pageURL(baseURL, page-1)
	}
	if data.HasNext {
		data.NextURL = pageURL(baseURL, page+1)
	}
	a.render(w, "index.html", data)
}

func (a *app) post(w http.ResponseWriter, r *http.Request, slug string) {
	var item post
	var published string
	err := a.db.QueryRow(`SELECT id, slug, title, summary, body_html, published_at FROM posts WHERE slug = ? AND status = 'published' AND published_at IS NOT NULL AND published_at <= ?`, slug, a.clock().UTC().Format(time.RFC3339)).Scan(&item.ID, &item.Slug, &item.Title, &item.Summary, &item.BodyHTML, &published)
	if err == sql.ErrNoRows {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	item.PublishedAt = published
	a.render(w, "post.html", pageData{Site: a.site, Post: item})
}

func (a *app) tags(w http.ResponseWriter, _ *http.Request) {
	rows, err := a.db.Query(`SELECT slug, name FROM tags ORDER BY name`)
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	var tags []tag
	for rows.Next() {
		var item tag
		if err := rows.Scan(&item.Slug, &item.Name); err != nil {
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}
		tags = append(tags, item)
	}
	a.render(w, "tags.html", pageData{Site: a.site, Tags: tags})
}

func (a *app) style(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	data, _ := assets.ReadFile("static/style.css")
	_, _ = w.Write(data)
}

func (a *app) render(w http.ResponseWriter, name string, data pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := a.tmpl.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("render %s: %v", name, err)
	}
}

func scanPosts(rows *sql.Rows) ([]post, error) {
	var posts []post
	for rows.Next() {
		var item post
		if err := rows.Scan(&item.ID, &item.Slug, &item.Title, &item.Summary, &item.BodyHTML, &item.PublishedAt); err != nil {
			return nil, err
		}
		posts = append(posts, item)
	}
	return posts, rows.Err()
}

func queryPage(r *http.Request) int {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		return 1
	}
	return page
}

func pageURL(base string, page int) string {
	if page <= 1 {
		return base
	}
	return base + "?page=" + strconv.Itoa(page)
}

func cleanPath(value string) string { return path.Clean("/" + value) }
