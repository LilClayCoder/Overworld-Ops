package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite" // pure-Go driver, no cgo toolchain needed
)

// ErrNotFound is returned when a lookup matches no row.
var ErrNotFound = errors.New("not found")

// ErrPortTaken is returned when a port is already claimed by another server.
var ErrPortTaken = errors.New("port already allocated")

// Store is the persistence layer over SQLite.
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS users (
	id            TEXT PRIMARY KEY,
	username      TEXT NOT NULL UNIQUE,
	password_hash TEXT NOT NULL,
	is_admin      INTEGER NOT NULL DEFAULT 0,
	created_at    DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS servers (
	id               TEXT PRIMARY KEY,
	name             TEXT NOT NULL,
	owner_id         TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	type             TEXT NOT NULL,
	version          TEXT NOT NULL,
	modpack_provider TEXT NOT NULL DEFAULT '',
	modpack_id       TEXT NOT NULL DEFAULT '',
	port             INTEGER NOT NULL UNIQUE,
	memory           TEXT NOT NULL,
	cpu_limit        REAL NOT NULL DEFAULT 0,
	status           TEXT NOT NULL,
	status_message   TEXT NOT NULL DEFAULT '',
	container_id     TEXT NOT NULL DEFAULT '',
	created_at       DATETIME NOT NULL,
	updated_at       DATETIME NOT NULL,
	last_active_at   DATETIME
);

CREATE INDEX IF NOT EXISTS idx_servers_owner ON servers(owner_id);
CREATE INDEX IF NOT EXISTS idx_servers_status ON servers(status);
`

// Open connects to the SQLite database at path, creating the file and schema
// if they do not exist.
func Open(ctx context.Context, path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}

	// _pragma args are the modernc driver's way of setting PRAGMAs on connect.
	// WAL keeps readers from blocking the writer; busy_timeout avoids spurious
	// SQLITE_BUSY when the idle reaper and an HTTP request collide.
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	// SQLite tolerates exactly one writer. Serialising here is simpler than
	// retrying on lock contention.
	db.SetMaxOpenConns(1)

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	if _, err := db.ExecContext(ctx, schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// --- users ---

// CreateUser inserts a new user. The caller supplies an already-hashed password.
func (s *Store) CreateUser(ctx context.Context, username, passwordHash string, isAdmin bool) (*User, error) {
	u := &User{
		ID:           uuid.NewString(),
		Username:     username,
		PasswordHash: passwordHash,
		IsAdmin:      isAdmin,
		CreatedAt:    time.Now().UTC(),
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO users (id, username, password_hash, is_admin, created_at) VALUES (?, ?, ?, ?, ?)`,
		u.ID, u.Username, u.PasswordHash, u.IsAdmin, u.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("insert user: %w", err)
	}
	return u, nil
}

// UserByUsername looks up a user for login.
func (s *Store) UserByUsername(ctx context.Context, username string) (*User, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, username, password_hash, is_admin, created_at FROM users WHERE username = ?`, username)
	return scanUser(row)
}

// UserByID looks up a user by primary key.
func (s *Store) UserByID(ctx context.Context, id string) (*User, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, username, password_hash, is_admin, created_at FROM users WHERE id = ?`, id)
	return scanUser(row)
}

// AnyUserExists reports whether the users table has at least one row. The
// bootstrap flow uses it to decide whether to seed an admin account.
func (s *Store) AnyUserExists(ctx context.Context) (bool, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return false, fmt.Errorf("count users: %w", err)
	}
	return n > 0, nil
}

func scanUser(row *sql.Row) (*User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.IsAdmin, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan user: %w", err)
	}
	return &u, nil
}

// ListUsers returns every account, oldest first, which is the order the admin
// panel renders them in.
func (s *Store) ListUsers(ctx context.Context) ([]*User, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, username, password_hash, is_admin, created_at FROM users ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("query users: %w", err)
	}
	defer rows.Close()

	users := []*User{}
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.IsAdmin, &u.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		users = append(users, &u)
	}
	return users, rows.Err()
}

// CountAdmins reports how many accounts hold the admin flag. The admin API
// checks it before a demotion or delete so the platform is never left with
// nobody who can administer it.
func (s *Store) CountAdmins(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE is_admin = 1`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count admins: %w", err)
	}
	return n, nil
}

// SetUserAdmin grants or revokes admin rights.
func (s *Store) SetUserAdmin(ctx context.Context, id string, isAdmin bool) error {
	res, err := s.db.ExecContext(ctx, `UPDATE users SET is_admin = ? WHERE id = ?`, isAdmin, id)
	if err != nil {
		return fmt.Errorf("set user admin: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetUserPassword replaces the stored hash. The caller supplies an
// already-hashed password, as with CreateUser.
func (s *Store) SetUserPassword(ctx context.Context, id, passwordHash string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, passwordHash, id)
	if err != nil {
		return fmt.Errorf("set user password: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteUser removes an account.
//
// servers.owner_id declares ON DELETE CASCADE, so deleting a user who still
// owns servers would drop those records and strand their containers and
// volumes on the host with nothing left to reference them. The caller is
// responsible for reassigning or deleting the user's servers first; the admin
// API refuses the request otherwise.
func (s *Store) DeleteUser(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ReassignServers transfers every server owned by from to to, returning how
// many changed hands. It is how an admin removes an account without touching
// the worlds that account created.
func (s *Store) ReassignServers(ctx context.Context, from, to string) (int, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE servers SET owner_id = ?, updated_at = ? WHERE owner_id = ?`,
		to, time.Now().UTC(), from)
	if err != nil {
		return 0, fmt.Errorf("reassign servers: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// ServerCountsByOwner returns how many servers each owner has, so the admin
// panel can show it without a query per row.
func (s *Store) ServerCountsByOwner(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT owner_id, COUNT(*) FROM servers GROUP BY owner_id`)
	if err != nil {
		return nil, fmt.Errorf("count servers by owner: %w", err)
	}
	defer rows.Close()

	counts := map[string]int{}
	for rows.Next() {
		var owner string
		var n int
		if err := rows.Scan(&owner, &n); err != nil {
			return nil, fmt.Errorf("scan server count: %w", err)
		}
		counts[owner] = n
	}
	return counts, rows.Err()
}

// --- servers ---

const serverCols = `id, name, owner_id, type, version, modpack_provider, modpack_id,
	port, memory, cpu_limit, status, status_message, container_id,
	created_at, updated_at, last_active_at`

// CreateServer inserts a server record. Port must already be allocated by the
// caller; the UNIQUE constraint on port is the final arbiter, so a lost race
// surfaces as ErrPortTaken rather than two servers sharing a port.
func (s *Store) CreateServer(ctx context.Context, srv *Server) error {
	now := time.Now().UTC()
	if srv.ID == "" {
		srv.ID = uuid.NewString()
	}
	srv.CreatedAt, srv.UpdatedAt = now, now

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO servers (`+serverCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		srv.ID, srv.Name, srv.OwnerID, srv.Type, srv.Version, srv.ModpackProvider, srv.ModpackID,
		srv.Port, srv.Memory, srv.CPULimit, srv.Status, srv.StatusMessage, srv.ContainerID,
		srv.CreatedAt, srv.UpdatedAt, srv.LastActiveAt)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrPortTaken
		}
		return fmt.Errorf("insert server: %w", err)
	}
	return nil
}

// ServerByID fetches one server record.
func (s *Store) ServerByID(ctx context.Context, id string) (*Server, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+serverCols+` FROM servers WHERE id = ?`, id)
	var srv Server
	err := scanServer(row.Scan, &srv)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan server: %w", err)
	}
	return &srv, nil
}

// ListServers returns every server, newest first. A non-empty ownerID scopes
// the result to that owner.
func (s *Store) ListServers(ctx context.Context, ownerID string) ([]*Server, error) {
	query := `SELECT ` + serverCols + ` FROM servers`
	args := []any{}
	if ownerID != "" {
		query += ` WHERE owner_id = ?`
		args = append(args, ownerID)
	}
	query += ` ORDER BY created_at DESC`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query servers: %w", err)
	}
	defer rows.Close()

	servers := []*Server{}
	for rows.Next() {
		var srv Server
		if err := scanServer(rows.Scan, &srv); err != nil {
			return nil, fmt.Errorf("scan server: %w", err)
		}
		servers = append(servers, &srv)
	}
	return servers, rows.Err()
}

// CountServers returns the total number of server records, used to enforce the
// max-concurrent-servers cap.
func (s *Store) CountServers(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM servers`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count servers: %w", err)
	}
	return n, nil
}

// UsedPorts returns every port currently claimed by a server record.
func (s *Store) UsedPorts(ctx context.Context) (map[int]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT port FROM servers`)
	if err != nil {
		return nil, fmt.Errorf("query ports: %w", err)
	}
	defer rows.Close()

	used := map[int]bool{}
	for rows.Next() {
		var p int
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("scan port: %w", err)
		}
		used[p] = true
	}
	return used, rows.Err()
}

// UpdateServer persists the mutable fields of a server record.
func (s *Store) UpdateServer(ctx context.Context, srv *Server) error {
	srv.UpdatedAt = time.Now().UTC()
	res, err := s.db.ExecContext(ctx,
		`UPDATE servers SET name = ?, type = ?, version = ?, modpack_provider = ?, modpack_id = ?,
			memory = ?, cpu_limit = ?, status = ?, status_message = ?, container_id = ?,
			updated_at = ?, last_active_at = ? WHERE id = ?`,
		srv.Name, srv.Type, srv.Version, srv.ModpackProvider, srv.ModpackID,
		srv.Memory, srv.CPULimit, srv.Status, srv.StatusMessage, srv.ContainerID,
		srv.UpdatedAt, srv.LastActiveAt, srv.ID)
	if err != nil {
		return fmt.Errorf("update server: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetStatus records a lifecycle transition. msg is stored verbatim and shown
// in the UI when status is StatusError.
func (s *Store) SetStatus(ctx context.Context, id string, status Status, msg string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE servers SET status = ?, status_message = ?, updated_at = ? WHERE id = ?`,
		status, msg, time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("set status: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetContainerID records the Docker container backing a server.
func (s *Store) SetContainerID(ctx context.Context, id, containerID string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE servers SET container_id = ?, updated_at = ? WHERE id = ?`,
		containerID, time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("set container id: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// TouchActivity records that players were seen on a server, resetting its idle
// timer.
func (s *Store) TouchActivity(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE servers SET last_active_at = ? WHERE id = ?`, time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("touch activity: %w", err)
	}
	return nil
}

// DeleteServer removes a server record. The caller is responsible for having
// already removed the container and volume.
func (s *Store) DeleteServer(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM servers WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete server: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// scanServer adapts both (*sql.Row).Scan and (*sql.Rows).Scan.
func scanServer(scan func(...any) error, srv *Server) error {
	var lastActive sql.NullTime
	err := scan(&srv.ID, &srv.Name, &srv.OwnerID, &srv.Type, &srv.Version,
		&srv.ModpackProvider, &srv.ModpackID, &srv.Port, &srv.Memory, &srv.CPULimit,
		&srv.Status, &srv.StatusMessage, &srv.ContainerID,
		&srv.CreatedAt, &srv.UpdatedAt, &lastActive)
	if err != nil {
		return err
	}
	if lastActive.Valid {
		t := lastActive.Time
		srv.LastActiveAt = &t
	}
	return nil
}

// isUniqueViolation detects a UNIQUE constraint failure. The modernc driver
// reports constraint failures in the error string with no typed sentinel to
// compare against.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
