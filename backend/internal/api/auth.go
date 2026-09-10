package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/calebgrabowski/overworld-ops/backend/internal/store"
)

const (
	sessionCookie = "owo_session"
	sessionTTL    = 30 * 24 * time.Hour
)

// ctxKey is the private type for context values so no other package can
// collide with these keys.
type ctxKey int

const userCtxKey ctxKey = iota

// userFrom returns the authenticated user attached by requireAuth.
func userFrom(ctx context.Context) *store.User {
	u, _ := ctx.Value(userCtxKey).(*store.User)
	return u
}

// issueSession mints a signed cookie carrying the user ID and an expiry. There
// is no server-side session table: for a handful of friends on a homelab, a
// signed cookie is enough, and it keeps logout/restart behaviour simple.
//
// The trade-off is that sessions cannot be revoked individually before they
// expire; rotating OWO_SESSION_SECRET invalidates all of them at once.
func (s *Server) issueSession(w http.ResponseWriter, userID string) {
	expiry := time.Now().Add(sessionTTL)
	payload := userID + "|" + strconv.FormatInt(expiry.Unix(), 10)
	token := base64.RawURLEncoding.EncodeToString([]byte(payload + "|" + s.sign(payload)))

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		Expires:  expiry,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		// Secure is off by default because homelab installs are usually
		// plain HTTP on a LAN. Turn it on behind a TLS reverse proxy.
		Secure: s.cfg.SecureCookies,
	})
}

// clearSession expires the session cookie.
func (s *Server) clearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.cfg.SecureCookies,
	})
}

// sign returns the HMAC-SHA256 of payload, hex encoded.
func (s *Server) sign(payload string) string {
	mac := hmac.New(sha256.New, []byte(s.cfg.SessionSecret))
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

// parseSession validates a session cookie and returns the user ID it carries.
func (s *Server) parseSession(r *http.Request) (string, error) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return "", errors.New("no session cookie")
	}

	raw, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return "", errors.New("malformed session cookie")
	}

	parts := strings.Split(string(raw), "|")
	if len(parts) != 3 {
		return "", errors.New("malformed session cookie")
	}
	userID, expiryStr, sig := parts[0], parts[1], parts[2]

	// Constant-time compare so a signature cannot be brute-forced by timing.
	want := s.sign(userID + "|" + expiryStr)
	if subtle.ConstantTimeCompare([]byte(sig), []byte(want)) != 1 {
		return "", errors.New("bad session signature")
	}

	expiry, err := strconv.ParseInt(expiryStr, 10, 64)
	if err != nil || time.Now().Unix() > expiry {
		return "", errors.New("session expired")
	}
	return userID, nil
}

// requireAuth rejects unauthenticated requests and attaches the user to the
// request context for downstream handlers.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, err := s.parseSession(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}

		user, err := s.store.UserByID(r.Context(), userID)
		if err != nil {
			// A valid signature for a deleted user: treat as logged out.
			s.clearSession(w)
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}

		ctx := context.WithValue(r.Context(), userCtxKey, user)
		next(w, r.WithContext(ctx))
	}
}

// ownsServer reports whether user may manage srv. Admins may manage any
// server; everyone else is scoped to what they created.
func ownsServer(user *store.User, srv *store.Server) bool {
	return user.IsAdmin || srv.OwnerID == user.ID
}

// --- handlers ---

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// minPasswordLength is the floor for both self-registration and an
// admin-set password, so the two paths cannot drift apart.
const minPasswordLength = 8

// validateCredentials normalises and checks a username/password pair. It
// returns the message to show the user, or "" when the pair is acceptable.
func validateCredentials(creds *credentials) string {
	creds.Username = strings.TrimSpace(creds.Username)
	if len(creds.Username) < 3 || len(creds.Username) > 32 {
		return "username must be 3-32 characters"
	}
	if len(creds.Password) < minPasswordLength {
		return fmt.Sprintf("password must be at least %d characters", minPasswordLength)
	}
	return ""
}

// handleRegister creates an account. Registration is open only while
// OWO_ALLOW_REGISTRATION is true; otherwise an admin creates accounts.
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.AllowRegistration {
		writeError(w, http.StatusForbidden, "registration is disabled")
		return
	}

	var creds credentials
	if err := decodeJSON(r, &creds); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if msg := validateCredentials(&creds); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(creds.Password), bcrypt.DefaultCost)
	if err != nil {
		slog.Error("hash password", "error", err)
		writeError(w, http.StatusInternalServerError, "could not create account")
		return
	}

	// The very first account to register becomes the admin, which avoids a
	// separate bootstrap step on a fresh install.
	existing, err := s.store.AnyUserExists(r.Context())
	if err != nil {
		slog.Error("check for existing users", "error", err)
		writeError(w, http.StatusInternalServerError, "could not create account")
		return
	}

	user, err := s.store.CreateUser(r.Context(), creds.Username, string(hash), !existing)
	if err != nil {
		// Almost always a duplicate username.
		writeError(w, http.StatusConflict, "username is taken")
		return
	}

	s.issueSession(w, user.ID)
	writeJSON(w, http.StatusCreated, user)
}

// handleLogin exchanges credentials for a session cookie.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var creds credentials
	if err := decodeJSON(r, &creds); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	user, err := s.store.UserByUsername(r.Context(), strings.TrimSpace(creds.Username))
	if err != nil {
		// Hash anyway so a missing username and a wrong password take the
		// same time, and the response is identical either way.
		bcrypt.CompareHashAndPassword([]byte(dummyHash), []byte(creds.Password))
		writeError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(creds.Password)); err != nil {
		writeError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}

	s.issueSession(w, user.ID)
	writeJSON(w, http.StatusOK, user)
}

// dummyHash is a valid bcrypt hash of a random string, used to equalise the
// timing of a failed username lookup against a failed password check.
const dummyHash = "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"

// handleLogout clears the session cookie.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.clearSession(w)
	writeJSON(w, http.StatusOK, map[string]string{"status": "logged out"})
}

// handleMe returns the currently authenticated user, which the frontend uses
// to decide whether to show the login screen.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, userFrom(r.Context()))
}

// SeedAdmin creates the initial admin account if the users table is empty and
// an admin password was supplied. It is a no-op otherwise.
func SeedAdmin(ctx context.Context, st *store.Store, username, password string) error {
	if username == "" || password == "" {
		return nil
	}
	exists, err := st.AnyUserExists(ctx)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash admin password: %w", err)
	}
	if _, err := st.CreateUser(ctx, username, string(hash), true); err != nil {
		return fmt.Errorf("create admin user: %w", err)
	}
	slog.Info("seeded initial admin account", "username", username)
	return nil
}
