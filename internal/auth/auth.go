package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

const sessionCookie = "wisp_session"

type User struct {
	ID    int64
	Email string
	Name  string
}

type Session struct {
	User      User
	CSRFToken string
}

type Invite struct {
	Email     string
	ExpiresAt time.Time
}

type Service struct {
	DB            *sql.DB
	Now           func() time.Time
	SecureCookies bool
}

func (s *Service) HasUsers() (bool, error) {
	var count int
	err := s.DB.QueryRow(`SELECT count(*) FROM users`).Scan(&count)
	return count > 0, err
}

func (s *Service) CreateUser(email, name, password string) (User, error) {
	hash, err := hashPassword(password)
	if err != nil {
		return User{}, err
	}
	now := s.now().UTC().Format(time.RFC3339)
	result, err := s.DB.Exec(`INSERT INTO users(email, name, password_hash, created_at) VALUES (?, ?, ?, ?)`, strings.TrimSpace(email), strings.TrimSpace(name), hash, now)
	if err != nil {
		return User{}, err
	}
	id, err := result.LastInsertId()
	return User{ID: id, Email: strings.TrimSpace(email), Name: strings.TrimSpace(name)}, err
}

func (s *Service) Authenticate(email, password string) (User, error) {
	var user User
	var encoded string
	err := s.DB.QueryRow(`SELECT id, email, name, password_hash FROM users WHERE email = ?`, strings.TrimSpace(email)).Scan(&user.ID, &user.Email, &user.Name, &encoded)
	if err != nil {
		return User{}, errors.New("invalid email or password")
	}
	if err := verifyPassword(encoded, password); err != nil {
		return User{}, errors.New("invalid email or password")
	}
	_, _ = s.DB.Exec(`UPDATE users SET last_login_at = ? WHERE id = ?`, s.now().UTC().Format(time.RFC3339), user.ID)
	return user, nil
}

func (s *Service) ChangePassword(userID int64, current, next string) error {
	var encoded string
	if err := s.DB.QueryRow(`SELECT password_hash FROM users WHERE id = ?`, userID).Scan(&encoded); err != nil {
		return errors.New("current password is incorrect")
	}
	if err := verifyPassword(encoded, current); err != nil {
		return errors.New("current password is incorrect")
	}
	hash, err := hashPassword(next)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(`UPDATE users SET password_hash = ? WHERE id = ?`, hash, userID)
	return err
}

func (s *Service) CreateInvite(email string, createdBy int64) (string, error) {
	token, err := randomBytes(32)
	if err != nil {
		return "", err
	}
	expires := s.now().Add(7 * 24 * time.Hour).UTC()
	_, err = s.DB.Exec(`INSERT INTO invites(token_hash, email, created_by, expires_at, created_at) VALUES (?, ?, ?, ?, ?)`, tokenHash(token), strings.TrimSpace(email), createdBy, expires.Format(time.RFC3339), s.now().UTC().Format(time.RFC3339))
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(token), nil
}

func (s *Service) AcceptInvite(token, email, name, password string) (User, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(decoded) != 32 {
		return User{}, errors.New("invite is invalid or expired")
	}
	var storedEmail, expires string
	err = s.DB.QueryRow(`SELECT email, expires_at FROM invites WHERE token_hash = ? AND accepted_at IS NULL`, tokenHash(decoded)).Scan(&storedEmail, &expires)
	if err != nil || expires <= s.now().UTC().Format(time.RFC3339) || (storedEmail != "" && !strings.EqualFold(storedEmail, email)) {
		return User{}, errors.New("invite is invalid or expired")
	}
	hash, err := hashPassword(password)
	if err != nil {
		return User{}, err
	}
	now := s.now().UTC().Format(time.RFC3339)
	tx, err := s.DB.Begin()
	if err != nil {
		return User{}, err
	}
	result, err := tx.Exec(`INSERT INTO users(email, name, password_hash, created_at) VALUES (?, ?, ?, ?)`, strings.TrimSpace(email), strings.TrimSpace(name), hash, now)
	if err != nil {
		tx.Rollback()
		return User{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		tx.Rollback()
		return User{}, err
	}
	if _, err := tx.Exec(`UPDATE invites SET accepted_at = ? WHERE token_hash = ?`, now, tokenHash(decoded)); err != nil {
		tx.Rollback()
		return User{}, err
	}
	if err := tx.Commit(); err != nil {
		return User{}, err
	}
	return User{ID: id, Email: strings.TrimSpace(email), Name: strings.TrimSpace(name)}, nil
}

func (s *Service) StartSession(w http.ResponseWriter, userID int64) (string, error) {
	token, err := randomBytes(32)
	if err != nil {
		return "", err
	}
	csrf, err := randomBytes(32)
	if err != nil {
		return "", err
	}
	expires := s.now().Add(14 * 24 * time.Hour).UTC()
	if _, err := s.DB.Exec(`INSERT INTO sessions(token_hash, user_id, csrf_token, expires_at, created_at) VALUES (?, ?, ?, ?, ?)`, tokenHash(token), userID, base64.RawURLEncoding.EncodeToString(csrf), expires.Format(time.RFC3339), s.now().UTC().Format(time.RFC3339)); err != nil {
		return "", err
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: base64.RawURLEncoding.EncodeToString(token), Path: "/", HttpOnly: true, Secure: s.SecureCookies, SameSite: http.SameSiteLaxMode, Expires: expires})
	return base64.RawURLEncoding.EncodeToString(csrf), nil
}

func (s *Service) Current(r *http.Request) (Session, error) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return Session{}, sql.ErrNoRows
	}
	token, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil || len(token) != 32 {
		return Session{}, sql.ErrNoRows
	}
	var session Session
	var expires string
	err = s.DB.QueryRow(`SELECT u.id, u.email, u.name, s.csrf_token, s.expires_at FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.token_hash = ?`, tokenHash(token)).Scan(&session.User.ID, &session.User.Email, &session.User.Name, &session.CSRFToken, &expires)
	if err != nil || expires <= s.now().UTC().Format(time.RFC3339) {
		return Session{}, sql.ErrNoRows
	}
	return session, nil
}

func (s *Service) Logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		if token, err := base64.RawURLEncoding.DecodeString(cookie.Value); err == nil {
			_, _ = s.DB.Exec(`DELETE FROM sessions WHERE token_hash = ?`, tokenHash(token))
		}
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, Secure: s.SecureCookies, SameSite: http.SameSiteLaxMode, MaxAge: -1})
}

func ValidateCSRF(session Session, value string) bool {
	return subtle.ConstantTimeCompare([]byte(session.CSRFToken), []byte(value)) == 1
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func randomBytes(size int) ([]byte, error) {
	value := make([]byte, size)
	_, err := rand.Read(value)
	return value, err
}

func tokenHash(token []byte) []byte {
	hash := sha256.Sum256(token)
	return hash[:]
}

func hashPassword(password string) (string, error) {
	salt, err := randomBytes(16)
	if err != nil {
		return "", err
	}
	const memory, iterations, threads, keyLen = 64 * 1024, 3, 2, 32
	key := argon2.IDKey([]byte(password), salt, iterations, memory, threads, keyLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", memory, iterations, threads, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

func verifyPassword(encoded, password string) error {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return errors.New("invalid password hash")
	}
	var memory, iterations, threads uint32
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &threads); err != nil {
		return errors.New("invalid password hash")
	}
	saltEncoded, keyEncoded := parts[4], parts[5]
	salt, err := base64.RawStdEncoding.DecodeString(saltEncoded)
	if err != nil {
		return err
	}
	expected, err := base64.RawStdEncoding.DecodeString(keyEncoded)
	if err != nil {
		return err
	}
	actual := argon2.IDKey([]byte(password), salt, iterations, memory, uint8(threads), uint32(len(expected)))
	if subtle.ConstantTimeCompare(actual, expected) != 1 {
		return errors.New("password mismatch")
	}
	return nil
}
