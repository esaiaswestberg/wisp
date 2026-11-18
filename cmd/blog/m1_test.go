package main

import (
	"context"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
	return &app{
		db:    database,
		tmpl:  tmpl,
		clock: func() time.Time { return time.Date(2026, 11, 18, 23, 0, 0, 0, time.UTC) },
		site:  site{Name: "Wisp", Description: "A small, fast blog.", Language: "en", PerPage: 10},
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
