package auth

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"wisp/internal/db"
)

func TestPasswordAndSession(t *testing.T) {
	database, err := db.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	now := func() time.Time { return time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC) }
	service := &Service{DB: database, Now: now}
	user, err := service.CreateUser("writer@example.com", "Writer", "a password longer than twelve")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(user.Email, "wrong password"); err == nil {
		t.Fatal("wrong password authenticated")
	}
	if _, err := service.Authenticate(user.Email, "a password longer than twelve"); err != nil {
		t.Fatalf("authenticate: %v", err)
	}

	response := httptest.NewRecorder()
	csrf, err := service.StartSession(response, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "/admin", nil)
	request.Header.Set("Cookie", response.Header().Get("Set-Cookie"))
	session, err := service.Current(request)
	if err != nil {
		t.Fatalf("current session: %v", err)
	}
	if session.User.Email != user.Email || session.CSRFToken != csrf || !ValidateCSRF(session, csrf) {
		t.Fatal("session data did not round-trip")
	}
	if ValidateCSRF(session, "wrong") {
		t.Fatal("wrong CSRF token accepted")
	}
	if _, err := database.Exec(`DELETE FROM sessions`); err != nil {
		t.Fatal(err)
	}
}

func TestInviteIsSingleUse(t *testing.T) {
	database, err := db.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := func() time.Time { return time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC) }
	service := &Service{DB: database, Now: now}
	owner, err := service.CreateUser("owner@example.com", "Owner", "a password longer than twelve")
	if err != nil {
		t.Fatal(err)
	}
	token, err := service.CreateInvite("new@example.com", owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AcceptInvite(token, "new@example.com", "New User", "another password longer"); err != nil {
		t.Fatalf("accept invite: %v", err)
	}
	if _, err := service.AcceptInvite(token, "new@example.com", "Again", "another password longer"); err == nil {
		t.Fatal("invite was accepted twice")
	}
}
