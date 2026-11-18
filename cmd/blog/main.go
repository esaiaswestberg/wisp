package main

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"wisp/internal/auth"
	"wisp/internal/db"
	"wisp/internal/posts"
	"wisp/internal/uploads"
)

//go:embed templates/*.html static/*
var assets embed.FS

type site struct {
	Name        string
	Description string
	Language    string
	PerPage     int
	BaseURL     string
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
	User       auth.User
	CSRF       string
	Error      string
	Form       map[string]string
	InviteURL  string
}

type editorData struct {
	Site   site
	User   auth.User
	CSRF   string
	Error  string
	Notice string
	Post   post
	Tags   string
	IsNew  bool
}

type adminPost struct {
	ID          int64
	Slug        string
	Title       string
	Status      string
	PublishedAt string
	UpdatedAt   string
}

type app struct {
	db            *sql.DB
	tmpl          *template.Template
	site          site
	clock         func() time.Time
	auth          *auth.Service
	uploader      *uploads.Service
	loginMu       sync.Mutex
	loginFailures map[string][]time.Time
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
	application.auth = &auth.Service{DB: database, Now: time.Now, SecureCookies: strings.HasPrefix(application.site.BaseURL, "https:")}
	application.loginFailures = make(map[string][]time.Time)
	application.uploader, err = uploads.NewFromEnv()
	if err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/static/style.css", application.style)
	mux.HandleFunc("/static/editor.js", application.editorScript)
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
	a.site = site{Name: values["site_name"], Description: values["site_description"], Language: values["language"], BaseURL: values["base_url"], PerPage: 10}
	if n, err := strconv.Atoi(values["posts_per_page"]); err == nil && n > 0 && n <= 50 {
		a.site.PerPage = n
	}
	return rows.Err()
}

func (a *app) route(w http.ResponseWriter, r *http.Request) {
	hasUsers, err := a.auth.HasUsers()
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	if !hasUsers {
		if r.URL.Path == "/setup" {
			a.setup(w, r)
			return
		}
		if r.URL.Path == "/static/style.css" {
			a.style(w, r)
			return
		}
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	if r.URL.Path == "/setup" {
		http.NotFound(w, r)
		return
	}
	if r.URL.Path == "/login" {
		a.login(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/invite/") {
		a.invite(w, r, strings.TrimPrefix(r.URL.Path, "/invite/"))
		return
	}
	if r.URL.Path == "/logout" && r.Method == http.MethodPost {
		a.logout(w, r)
		return
	}
	if r.URL.Path == "/admin" || r.URL.Path == "/admin/" {
		a.admin(w, r)
		return
	}
	if r.URL.Path == "/admin/settings" {
		a.settings(w, r)
		return
	}
	if r.URL.Path == "/admin/users" {
		a.users(w, r)
		return
	}
	if r.URL.Path == "/admin/password" {
		a.password(w, r)
		return
	}
	if r.URL.Path == "/admin/uploads/presign" {
		a.presignUpload(w, r)
		return
	}
	if r.URL.Path == "/admin/posts" {
		a.adminPosts(w, r)
		return
	}
	if r.URL.Path == "/admin/posts/new" {
		a.editPost(w, r, 0)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/admin/posts/") {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/admin/posts/"), "/")
		if len(parts) == 2 {
			id, err := strconv.ParseInt(parts[0], 10, 64)
			if err == nil {
				switch parts[1] {
				case "edit":
					a.editPost(w, r, id)
					return
				case "publish":
					a.postAction(w, r, id, "publish")
					return
				case "unpublish":
					a.postAction(w, r, id, "unpublish")
					return
				case "delete":
					a.postAction(w, r, id, "delete")
					return
				case "preview":
					a.previewPost(w, r, id)
					return
				}
			}
		}
	}
	if strings.HasPrefix(r.URL.Path, "/admin/") {
		if _, ok := a.currentUser(w, r); !ok {
			return
		}
		http.NotFound(w, r)
		return
	}
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

func (a *app) setup(w http.ResponseWriter, r *http.Request) {
	data := pageData{Site: a.site, Form: map[string]string{}}
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			data.Error = "Invalid form."
		} else {
			data.Form = formValues(r)
			if err := validateAccount(data.Form["email"], data.Form["name"], data.Form["password"], data.Form["password_confirm"]); err != nil {
				data.Error = err.Error()
			} else if _, err := a.auth.CreateUser(data.Form["email"], data.Form["name"], data.Form["password"]); err != nil {
				data.Error = "Could not create the account. The email may already be in use."
			} else {
				settings := map[string]string{"site_name": data.Form["site_name"], "site_description": data.Form["site_description"], "site_author": data.Form["site_author"], "language": data.Form["language"], "base_url": strings.TrimRight(data.Form["base_url"], "/")}
				for key, value := range settings {
					_, _ = a.db.Exec(`UPDATE settings SET value = ?, updated_at = ? WHERE key = ?`, value, a.clock().UTC().Format(time.RFC3339), key)
				}
				_ = a.loadSite(context.Background())
				a.auth.SecureCookies = strings.HasPrefix(a.site.BaseURL, "https:")
				authUser, _ := a.auth.Authenticate(data.Form["email"], data.Form["password"])
				_, _ = a.auth.StartSession(w, authUser.ID)
				http.Redirect(w, r, "/admin", http.StatusSeeOther)
				return
			}
		}
	}
	a.render(w, "setup.html", data)
}

func (a *app) login(w http.ResponseWriter, r *http.Request) {
	data := pageData{Site: a.site, Form: map[string]string{}}
	if r.Method == http.MethodPost {
		_ = r.ParseForm()
		data.Form = formValues(r)
		if a.loginLimited(r) {
			data.Error = "Too many failed attempts. Try again in 15 minutes."
			a.render(w, "login.html", data)
			return
		}
		user, err := a.auth.Authenticate(data.Form["email"], data.Form["password"])
		if err != nil {
			a.recordLoginFailure(r)
			data.Error = "Invalid email or password."
		} else {
			a.clearLoginFailures(r)
			if _, err := a.auth.StartSession(w, user.ID); err != nil {
				http.Error(w, "could not start session", http.StatusInternalServerError)
				return
			}
			http.Redirect(w, r, "/admin", http.StatusSeeOther)
			return
		}
	}
	a.render(w, "login.html", data)
}

func (a *app) invite(w http.ResponseWriter, r *http.Request, token string) {
	data := pageData{Site: a.site, Form: map[string]string{}}
	if r.Method == http.MethodPost {
		_ = r.ParseForm()
		data.Form = formValues(r)
		if err := validateAccount(data.Form["email"], data.Form["name"], data.Form["password"], data.Form["password_confirm"]); err != nil {
			data.Error = err.Error()
		} else {
			user, err := a.auth.AcceptInvite(token, data.Form["email"], data.Form["name"], data.Form["password"])
			if err != nil {
				data.Error = "Invite is invalid, expired, or already used."
			} else {
				if _, err := a.auth.StartSession(w, user.ID); err != nil {
					http.Error(w, "could not start session", http.StatusInternalServerError)
					return
				}
				http.Redirect(w, r, "/admin", http.StatusSeeOther)
				return
			}
		}
	}
	a.render(w, "invite.html", data)
}

func (a *app) users(w http.ResponseWriter, r *http.Request) {
	session, ok := a.currentUser(w, r)
	if !ok {
		return
	}
	data := pageData{Site: a.site, User: session.User, CSRF: session.CSRFToken, Form: map[string]string{}}
	if r.Method == http.MethodPost {
		_ = r.ParseForm()
		if !auth.ValidateCSRF(session, r.FormValue("csrf")) {
			data.Error = "Invalid form token. Please try again."
		} else if !strings.Contains(r.FormValue("email"), "@") {
			data.Error = "Enter a valid email address."
		} else {
			token, err := a.auth.CreateInvite(r.FormValue("email"), session.User.ID)
			if err != nil {
				data.Error = "Could not create invite."
			} else {
				data.InviteURL = strings.TrimRight(a.site.BaseURL, "/") + "/invite/" + token
			}
		}
	}
	a.render(w, "users.html", data)
}

func (a *app) password(w http.ResponseWriter, r *http.Request) {
	session, ok := a.currentUser(w, r)
	if !ok {
		return
	}
	data := pageData{Site: a.site, User: session.User, CSRF: session.CSRFToken}
	if r.Method == http.MethodPost {
		_ = r.ParseForm()
		if !auth.ValidateCSRF(session, r.FormValue("csrf")) {
			data.Error = "Invalid form token. Please try again."
		} else if len(r.FormValue("new_password")) < 12 || r.FormValue("new_password") != r.FormValue("new_password_confirm") {
			data.Error = "New passwords must match and be at least 12 characters."
		} else if err := a.auth.ChangePassword(session.User.ID, r.FormValue("current_password"), r.FormValue("new_password")); err != nil {
			data.Error = err.Error()
		} else {
			a.auth.Logout(w, r)
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
	}
	a.render(w, "password.html", data)
}

func (a *app) adminPosts(w http.ResponseWriter, r *http.Request) {
	session, ok := a.currentUser(w, r)
	if !ok {
		return
	}
	rows, err := a.db.Query(`SELECT id, slug, title, status, COALESCE(published_at, ''), updated_at FROM posts ORDER BY updated_at DESC`)
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	var list []adminPost
	for rows.Next() {
		var item adminPost
		if err := rows.Scan(&item.ID, &item.Slug, &item.Title, &item.Status, &item.PublishedAt, &item.UpdatedAt); err != nil {
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}
		list = append(list, item)
	}
	a.render(w, "admin-posts.html", struct {
		Site  site
		User  auth.User
		CSRF  string
		Posts []adminPost
	}{a.site, session.User, session.CSRFToken, list})
}

func (a *app) editPost(w http.ResponseWriter, r *http.Request, id int64) {
	session, ok := a.currentUser(w, r)
	if !ok {
		return
	}
	data := editorData{Site: a.site, User: session.User, CSRF: session.CSRFToken, IsNew: id == 0}
	if id != 0 {
		if err := a.loadEditorPost(&data, id); err != nil {
			http.NotFound(w, r)
			return
		}
	}
	if r.Method == http.MethodPost {
		_ = r.ParseForm()
		if !auth.ValidateCSRF(session, r.FormValue("csrf")) {
			data.Error = "Invalid form token. Please try again."
		} else if strings.TrimSpace(r.FormValue("title")) == "" {
			data.Error = "A title is required."
		} else {
			postID, err := a.saveEditorPost(r, id)
			if err != nil {
				data.Error = "Could not save the post. The slug may already be in use."
			} else {
				http.Redirect(w, r, "/admin/posts/"+strconv.FormatInt(postID, 10)+"/edit", http.StatusSeeOther)
				return
			}
		}
		data.Post = editorPostFromRequest(r, data.Post)
		data.Tags = r.FormValue("tags")
	}
	a.render(w, "editor.html", data)
}

func (a *app) loadEditorPost(data *editorData, id int64) error {
	var published string
	err := a.db.QueryRow(`SELECT id, slug, title, summary, body_html, published_at FROM posts WHERE id = ?`, id).Scan(&data.Post.ID, &data.Post.Slug, &data.Post.Title, &data.Post.Summary, &data.Post.BodyHTML, &published)
	if err != nil {
		return err
	}
	data.Post.PublishedAt = formatEditorTime(published)
	_ = a.db.QueryRow(`SELECT group_concat(t.name, ', ') FROM post_tags pt JOIN tags t ON t.id = pt.tag_id WHERE pt.post_id = ?`, id).Scan(&data.Tags)
	return nil
}

func (a *app) saveEditorPost(r *http.Request, id int64) (int64, error) {
	title := strings.TrimSpace(r.FormValue("title"))
	slug := posts.Slug(r.FormValue("slug"))
	if slug == "" {
		slug = posts.Slug(title)
	}
	body := posts.Sanitize(r.FormValue("body_html"))
	bodyText := posts.PlainText(body)
	tagsText := strings.TrimSpace(r.FormValue("tags"))
	status := r.FormValue("status")
	if status != "published" {
		status = "draft"
	}
	publishedAt := parseEditorTime(r.FormValue("published_at"))
	if status == "published" && publishedAt == "" {
		publishedAt = a.clock().UTC().Format(time.RFC3339)
	}
	now := a.clock().UTC().Format(time.RFC3339)
	tx, err := a.db.Begin()
	if err != nil {
		return 0, err
	}
	if id == 0 {
		result, err := tx.Exec(`INSERT INTO posts(slug, title, summary, body_html, body_text, tags_text, status, published_at, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?)`, slug, title, strings.TrimSpace(r.FormValue("summary")), body, bodyText, tagsText, status, publishedAt, now, now)
		if err != nil {
			tx.Rollback()
			return 0, err
		}
		id, err = result.LastInsertId()
		if err != nil {
			tx.Rollback()
			return 0, err
		}
	} else if _, err := tx.Exec(`UPDATE posts SET slug = ?, title = ?, summary = ?, body_html = ?, body_text = ?, tags_text = ?, status = ?, published_at = NULLIF(?, ''), updated_at = ? WHERE id = ?`, slug, title, strings.TrimSpace(r.FormValue("summary")), body, bodyText, tagsText, status, publishedAt, now, id); err != nil {
		tx.Rollback()
		return 0, err
	}
	if _, err := tx.Exec(`DELETE FROM post_tags WHERE post_id = ?`, id); err != nil {
		tx.Rollback()
		return 0, err
	}
	for _, name := range strings.Split(tagsText, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		tagSlug := posts.Slug(name)
		if _, err := tx.Exec(`INSERT INTO tags(slug, name) VALUES (?, ?) ON CONFLICT(slug) DO UPDATE SET name = excluded.name`, tagSlug, name); err != nil {
			tx.Rollback()
			return 0, err
		}
		if _, err := tx.Exec(`INSERT INTO post_tags(post_id, tag_id) SELECT ?, id FROM tags WHERE slug = ?`, id, tagSlug); err != nil {
			tx.Rollback()
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

func (a *app) postAction(w http.ResponseWriter, r *http.Request, id int64, action string) {
	session, ok := a.currentUser(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodPost || !auth.ValidateCSRF(session, r.FormValue("csrf")) {
		http.Error(w, "invalid form token", http.StatusForbidden)
		return
	}
	var err error
	switch action {
	case "publish":
		_, err = a.db.Exec(`UPDATE posts SET status = 'published', published_at = COALESCE(published_at, ?), updated_at = ? WHERE id = ?`, a.clock().UTC().Format(time.RFC3339), a.clock().UTC().Format(time.RFC3339), id)
	case "unpublish":
		_, err = a.db.Exec(`UPDATE posts SET status = 'draft', updated_at = ? WHERE id = ?`, a.clock().UTC().Format(time.RFC3339), id)
	case "delete":
		_, err = a.db.Exec(`DELETE FROM posts WHERE id = ?`, id)
	}
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/posts", http.StatusSeeOther)
}

func (a *app) previewPost(w http.ResponseWriter, r *http.Request, id int64) {
	if _, ok := a.currentUser(w, r); !ok {
		return
	}
	var item post
	var published string
	if err := a.db.QueryRow(`SELECT id, slug, title, summary, body_html, COALESCE(published_at, '') FROM posts WHERE id = ?`, id).Scan(&item.ID, &item.Slug, &item.Title, &item.Summary, &item.BodyHTML, &published); err != nil {
		http.NotFound(w, r)
		return
	}
	item.PublishedAt = published
	a.render(w, "post.html", pageData{Site: a.site, Post: item})
}

func editorPostFromRequest(r *http.Request, item post) post {
	item.Title = r.FormValue("title")
	item.Slug = r.FormValue("slug")
	item.Summary = r.FormValue("summary")
	item.BodyHTML = template.HTML(posts.Sanitize(r.FormValue("body_html")))
	item.PublishedAt = r.FormValue("published_at")
	return item
}

func parseEditorTime(value string) string {
	if value == "" {
		return ""
	}
	parsed, err := time.ParseInLocation("2006-01-02T15:04", value, time.UTC)
	if err != nil {
		return ""
	}
	return parsed.Format(time.RFC3339)
}

func formatEditorTime(value string) string {
	if value == "" {
		return ""
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return ""
	}
	return parsed.UTC().Format("2006-01-02T15:04")
}

func (a *app) loginKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return host + "|" + strings.ToLower(strings.TrimSpace(r.FormValue("email")))
}

func (a *app) loginLimited(r *http.Request) bool {
	a.loginMu.Lock()
	defer a.loginMu.Unlock()
	key := a.loginKey(r)
	now := a.clock()
	cutoff := now.Add(-15 * time.Minute)
	var recent []time.Time
	for _, failure := range a.loginFailures[key] {
		if failure.After(cutoff) {
			recent = append(recent, failure)
		}
	}
	a.loginFailures[key] = recent
	return len(recent) >= 5
}

func (a *app) recordLoginFailure(r *http.Request) {
	a.loginMu.Lock()
	defer a.loginMu.Unlock()
	key := a.loginKey(r)
	if a.loginFailures == nil {
		a.loginFailures = make(map[string][]time.Time)
	}
	a.loginFailures[key] = append(a.loginFailures[key], a.clock())
}

func (a *app) clearLoginFailures(r *http.Request) {
	a.loginMu.Lock()
	defer a.loginMu.Unlock()
	delete(a.loginFailures, a.loginKey(r))
}

func (a *app) logout(w http.ResponseWriter, r *http.Request) {
	session, ok := a.currentUser(w, r)
	if !ok {
		return
	}
	if !auth.ValidateCSRF(session, r.FormValue("csrf")) {
		http.Error(w, "invalid form token", http.StatusForbidden)
		return
	}
	a.auth.Logout(w, r)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *app) admin(w http.ResponseWriter, r *http.Request) {
	session, ok := a.currentUser(w, r)
	if !ok {
		return
	}
	a.render(w, "admin.html", pageData{Site: a.site, User: session.User, CSRF: session.CSRFToken})
}

func (a *app) settings(w http.ResponseWriter, r *http.Request) {
	session, ok := a.currentUser(w, r)
	if !ok {
		return
	}
	data := pageData{Site: a.site, User: session.User, CSRF: session.CSRFToken, Form: map[string]string{"site_name": a.site.Name, "site_description": a.site.Description, "language": a.site.Language, "base_url": a.site.BaseURL}}
	if r.Method == http.MethodPost {
		_ = r.ParseForm()
		if !auth.ValidateCSRF(session, r.FormValue("csrf")) {
			data.Error = "Invalid form token. Please try again."
		} else if strings.TrimSpace(r.FormValue("site_name")) == "" || strings.TrimSpace(r.FormValue("base_url")) == "" {
			data.Error = "Site name and base URL are required."
		} else {
			for key, value := range map[string]string{"site_name": r.FormValue("site_name"), "site_description": r.FormValue("site_description"), "language": r.FormValue("language"), "base_url": strings.TrimRight(r.FormValue("base_url"), "/")} {
				_, _ = a.db.Exec(`UPDATE settings SET value = ?, updated_at = ? WHERE key = ?`, strings.TrimSpace(value), a.clock().UTC().Format(time.RFC3339), key)
			}
			_ = a.loadSite(context.Background())
			a.auth.SecureCookies = strings.HasPrefix(a.site.BaseURL, "https:")
			data.Site = a.site
			data.Form = map[string]string{"site_name": a.site.Name, "site_description": a.site.Description, "language": a.site.Language, "base_url": a.site.BaseURL}
		}
	}
	a.render(w, "settings.html", data)
}

func (a *app) currentUser(w http.ResponseWriter, r *http.Request) (auth.Session, bool) {
	session, err := a.auth.Current(r)
	if err != nil {
		http.Redirect(w, r, "/login?next="+r.URL.Path, http.StatusSeeOther)
		return auth.Session{}, false
	}
	return session, true
}

func formValues(r *http.Request) map[string]string {
	values := make(map[string]string)
	for key := range r.Form {
		values[key] = r.FormValue(key)
	}
	return values
}

func validateAccount(email, name, password, confirmation string) error {
	if !strings.Contains(email, "@") || strings.TrimSpace(name) == "" {
		return fmt.Errorf("enter a valid email and name")
	}
	if len(password) < 12 {
		return fmt.Errorf("password must be at least 12 characters")
	}
	if password != confirmation {
		return fmt.Errorf("passwords do not match")
	}
	return nil
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

func (a *app) editorScript(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	data, _ := assets.ReadFile("static/editor.js")
	_, _ = w.Write(data)
}

func (a *app) presignUpload(w http.ResponseWriter, r *http.Request) {
	session, ok := a.currentUser(w, r)
	if !ok {
		return
	}
	if a.uploader == nil {
		http.Error(w, "uploads are not configured", http.StatusNotImplemented)
		return
	}
	if r.Method != http.MethodPost || !auth.ValidateCSRF(session, r.Header.Get("X-CSRF-Token")) {
		http.Error(w, "invalid form token", http.StatusForbidden)
		return
	}
	var input struct {
		Filename    string `json:"filename"`
		ContentType string `json:"content_type"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	result, err := a.uploader.Presign(r.Context(), input.Filename, input.ContentType)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func (a *app) render(w http.ResponseWriter, name string, data any) {
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
