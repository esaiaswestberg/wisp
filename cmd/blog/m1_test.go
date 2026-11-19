package main

import (
	"context"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"wisp/internal/auth"
	"wisp/internal/db"
)

func testApp(t *testing.T) *app {
	t.Helper()
	database, err := db.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	tmpl := template.Must(template.ParseFS(assets, "templates/*.html"))
	testNow := time.Now().Add(time.Hour)
	service := &auth.Service{DB: database, Now: func() time.Time { return testNow }}
	if _, err := service.CreateUser("test@example.com", "Test", "a sufficiently long password"); err != nil {
		t.Fatal(err)
	}
	return &app{
		db:    database,
		tmpl:  tmpl,
		clock: func() time.Time { return testNow },
		site:  site{Name: "Wisp", Description: "A small, fast blog.", Language: "en", BaseURL: "https://example.com", FeedSize: 20, PerPage: 10},
		auth:  service,
	}
}

func TestPublicRoutes(t *testing.T) {
	application := testApp(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/static/style.css", application.style)
	mux.HandleFunc("/", application.route)

	tests := []struct {
		path string
		want int
		body string
	}{
		{"/", http.StatusOK, "Hello from Wisp"},
		{"/posts/hello-from-wisp", http.StatusOK, "Wisp is a small blog system"},
		{"/tags", http.StatusOK, "Go"},
		{"/tags/go", http.StatusOK, "Hello from Wisp"},
		{"/search?q=SQLite", http.StatusOK, "Hello from Wisp"},
		{"/feed.xml", http.StatusOK, "Hello from Wisp"},
		{"/sitemap.xml", http.StatusOK, "https://example.com/posts/hello-from-wisp"},
		{"/robots.txt", http.StatusOK, "Sitemap: https://example.com/sitemap.xml"},
		{"/posts/missing", http.StatusNotFound, ""},
		{"/draft", http.StatusNotFound, ""},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			res := httptest.NewRecorder()
			mux.ServeHTTP(res, req)
			if res.Code != tc.want {
				t.Fatalf("status = %d, want %d", res.Code, tc.want)
			}
			if tc.body != "" && !strings.Contains(res.Body.String(), tc.body) {
				t.Fatalf("response does not contain %q", tc.body)
			}
		})
	}
}

func TestEditorSaveSanitizesHTML(t *testing.T) {
	application := testApp(t)
	form := url.Values{
		"title":     {"Unsafe post"},
		"body_html": {`<p>Hello</p><script>alert(1)</script><img src="https://example.com/a.png" onerror="bad()">`},
		"status":    {"draft"},
	}
	request := httptest.NewRequest(http.MethodPost, "/admin/posts/new", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	id, err := application.saveEditorPost(request, 0)
	if err != nil {
		t.Fatal(err)
	}
	var body string
	if err := application.db.QueryRow(`SELECT body_html FROM posts WHERE id = ?`, id).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "script") || strings.Contains(body, "onerror") {
		t.Fatalf("unsafe HTML survived sanitization: %s", body)
	}
	if !strings.Contains(body, `src="https://example.com/a.png"`) {
		t.Fatalf("safe image was removed: %s", body)
	}
}
