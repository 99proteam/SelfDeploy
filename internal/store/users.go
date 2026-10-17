package store

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
)

type User struct {
	ID           int64     `json:"id"`
	Email        string    `json:"email"`
	Name         string    `json:"name"`
	PasswordHash string    `json:"-"`
	IsAdmin      bool      `json:"is_admin"`
	CreatedAt    time.Time `json:"created_at"`
}

type APIToken struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// HashToken returns the stored form of a session or API token, so a leaked
// database does not leak usable credentials.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

const userCols = `id, email, name, password_hash, is_admin, created_at`

func scanUser(sc interface{ Scan(...any) error }) (*User, error) {
	u := &User{}
	err := sc.Scan(&u.ID, &u.Email, &u.Name, &u.PasswordHash, &u.IsAdmin, &u.CreatedAt)
	return u, notFound(err)
}

func (s *Store) CountUsers() (int, error) {
	var n int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

func (s *Store) CreateUser(email, name, hash string, admin bool) (*User, error) {
	res, err := s.DB.Exec(`INSERT INTO users (email, name, password_hash, is_admin) VALUES (?, ?, ?, ?)`,
		email, name, hash, b2i(admin))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.User(id)
}

func (s *Store) User(id int64) (*User, error) {
	return scanUser(s.DB.QueryRow(`SELECT `+userCols+` FROM users WHERE id = ?`, id))
}

func (s *Store) UserByEmail(email string) (*User, error) {
	return scanUser(s.DB.QueryRow(`SELECT `+userCols+` FROM users WHERE email = ?`, email))
}

func (s *Store) Users() ([]*User, error) {
	rows, err := s.DB.Query(`SELECT ` + userCols + ` FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) UpdatePassword(id int64, hash string) error {
	_, err := s.DB.Exec(`UPDATE users SET password_hash = ? WHERE id = ?`, hash, id)
	return err
}

func (s *Store) DeleteUser(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM users WHERE id = ?`, id)
	return err
}

func (s *Store) CreateSession(token string, userID int64, ttl time.Duration) error {
	_, err := s.DB.Exec(`INSERT INTO sessions (token_hash, user_id, expires_at) VALUES (?, ?, ?)`,
		HashToken(token), userID, now().Add(ttl))
	return err
}

func (s *Store) DeleteSession(token string) error {
	_, err := s.DB.Exec(`DELETE FROM sessions WHERE token_hash = ?`, HashToken(token))
	return err
}

// UserBySession resolves a session cookie; expired sessions are treated as missing.
func (s *Store) UserBySession(token string) (*User, error) {
	return scanUser(s.DB.QueryRow(`SELECT u.id, u.email, u.name, u.password_hash, u.is_admin, u.created_at
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ? AND s.expires_at > ?`, HashToken(token), now()))
}

func (s *Store) UserByAPIToken(token string) (*User, error) {
	return scanUser(s.DB.QueryRow(`SELECT u.id, u.email, u.name, u.password_hash, u.is_admin, u.created_at
		FROM api_tokens t JOIN users u ON u.id = t.user_id WHERE t.token_hash = ?`, HashToken(token)))
}

func (s *Store) CreateAPIToken(userID int64, name, token string) error {
	_, err := s.DB.Exec(`INSERT INTO api_tokens (user_id, name, token_hash) VALUES (?, ?, ?)`,
		userID, name, HashToken(token))
	return err
}

func (s *Store) APITokens(userID int64) ([]APIToken, error) {
	rows, err := s.DB.Query(`SELECT id, name, created_at FROM api_tokens WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []APIToken{}
	for rows.Next() {
		var t APIToken
		if err := rows.Scan(&t.ID, &t.Name, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) DeleteAPIToken(userID, id int64) error {
	_, err := s.DB.Exec(`DELETE FROM api_tokens WHERE id = ? AND user_id = ?`, id, userID)
	return err
}

func (s *Store) PurgeExpiredSessions() {
	s.DB.Exec(`DELETE FROM sessions WHERE expires_at <= ?`, now())
}
