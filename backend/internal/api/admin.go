package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"golang.org/x/crypto/bcrypt"

	"github.com/calebgrabowski/overworld-ops/backend/internal/store"
)

// requireAdmin gates an endpoint behind the admin flag. It wraps requireAuth,
// so an anonymous caller still gets 401 and a logged-in non-admin gets 403 —
// the distinction matters to the frontend, which redirects on the first and
// shows a message on the second.
func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		if !userFrom(r.Context()).IsAdmin {
			writeError(w, http.StatusForbidden, "admin access required")
			return
		}
		next(w, r)
	})
}

// adminUserView is a user record plus how many servers they own, which is what
// the admin panel lists. The password hash stays out of it via the `json:"-"`
// tag on store.User.
type adminUserView struct {
	*store.User
	ServerCount int `json:"serverCount"`
}

// handleListUsers returns every account with its server count.
func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		slog.Error("list users", "error", err)
		writeError(w, http.StatusInternalServerError, "could not list users")
		return
	}

	// One grouped query rather than a count per row.
	counts, err := s.store.ServerCountsByOwner(r.Context())
	if err != nil {
		slog.Error("count servers by owner", "error", err)
		writeError(w, http.StatusInternalServerError, "could not list users")
		return
	}

	views := make([]*adminUserView, 0, len(users))
	for _, u := range users {
		views = append(views, &adminUserView{User: u, ServerCount: counts[u.ID]})
	}
	writeJSON(w, http.StatusOK, views)
}

// adminCreateUserInput is the payload for an admin-created account. IsAdmin is
// optional and defaults to false.
type adminCreateUserInput struct {
	Username string `json:"username"`
	Password string `json:"password"`
	IsAdmin  bool   `json:"isAdmin,omitempty"`
}

// handleCreateUser makes an account on someone else's behalf. This is the path
// that keeps the platform usable once OWO_ALLOW_REGISTRATION is turned off:
// registration closes, and the admin adds friends by hand instead.
//
// Unlike handleRegister it does not issue a session — the admin stays logged
// in as themselves and hands the credentials over out of band.
func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var in adminCreateUserInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	creds := credentials{Username: in.Username, Password: in.Password}
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

	user, err := s.store.CreateUser(r.Context(), creds.Username, string(hash), in.IsAdmin)
	if err != nil {
		// Almost always a duplicate username.
		writeError(w, http.StatusConflict, "username is taken")
		return
	}

	slog.Info("admin created account",
		"by", userFrom(r.Context()).Username, "username", user.Username, "admin", user.IsAdmin)
	writeJSON(w, http.StatusCreated, &adminUserView{User: user})
}

// adminUpdateUserInput carries the two things an admin may change about
// somebody else's account. Both fields are optional; nil means "leave alone".
type adminUpdateUserInput struct {
	IsAdmin  *bool   `json:"isAdmin,omitempty"`
	Password *string `json:"password,omitempty"`
}

// handleUpdateUser promotes, demotes, or resets the password on an account.
func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	actor := userFrom(r.Context())

	target, ok := s.lookupUser(w, r)
	if !ok {
		return
	}

	var in adminUpdateUserInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if in.IsAdmin != nil && *in.IsAdmin != target.IsAdmin {
		// Refusing self-demotion keeps an admin from pulling the panel out
		// from under themselves. The last-admin check below is the separate
		// invariant: the platform always keeps someone who can administer it.
		if target.ID == actor.ID && !*in.IsAdmin {
			writeError(w, http.StatusConflict, "you cannot remove your own admin access")
			return
		}
		if !*in.IsAdmin && !s.ensureAnotherAdmin(w, r, target) {
			return
		}

		if err := s.store.SetUserAdmin(r.Context(), target.ID, *in.IsAdmin); err != nil {
			slog.Error("set user admin", "user", target.ID, "error", err)
			writeError(w, http.StatusInternalServerError, "could not update account")
			return
		}
		target.IsAdmin = *in.IsAdmin
		slog.Info("admin changed account role",
			"by", actor.Username, "username", target.Username, "admin", target.IsAdmin)
	}

	if in.Password != nil {
		creds := credentials{Username: target.Username, Password: *in.Password}
		if msg := validateCredentials(&creds); msg != "" {
			writeError(w, http.StatusBadRequest, msg)
			return
		}

		hash, err := bcrypt.GenerateFromPassword([]byte(*in.Password), bcrypt.DefaultCost)
		if err != nil {
			slog.Error("hash password", "error", err)
			writeError(w, http.StatusInternalServerError, "could not update account")
			return
		}
		if err := s.store.SetUserPassword(r.Context(), target.ID, string(hash)); err != nil {
			slog.Error("set user password", "user", target.ID, "error", err)
			writeError(w, http.StatusInternalServerError, "could not update account")
			return
		}
		// The session cookie is signed over the user ID, not the password, so
		// a reset does not kick the account out of an existing session.
		// Rotating OWO_SESSION_SECRET is still the only way to end all of them.
		slog.Info("admin reset account password", "by", actor.Username, "username", target.Username)
	}

	counts, err := s.store.ServerCountsByOwner(r.Context())
	if err != nil {
		slog.Error("count servers by owner", "error", err)
		writeError(w, http.StatusInternalServerError, "could not update account")
		return
	}
	writeJSON(w, http.StatusOK, &adminUserView{User: target, ServerCount: counts[target.ID]})
}

// handleDeleteUser removes an account.
//
// A user who still owns servers is refused unless ?reassign=true, in which
// case their servers are transferred to the calling admin first. The reason is
// in the schema: servers.owner_id cascades on delete, so removing the user
// outright would drop the server records while their containers and volumes
// kept running — invisible to the registry and to the port allocator.
func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	actor := userFrom(r.Context())

	target, ok := s.lookupUser(w, r)
	if !ok {
		return
	}

	if target.ID == actor.ID {
		writeError(w, http.StatusConflict, "you cannot delete your own account")
		return
	}
	if target.IsAdmin && !s.ensureAnotherAdmin(w, r, target) {
		return
	}

	counts, err := s.store.ServerCountsByOwner(r.Context())
	if err != nil {
		slog.Error("count servers by owner", "error", err)
		writeError(w, http.StatusInternalServerError, "could not delete account")
		return
	}

	if owned := counts[target.ID]; owned > 0 {
		if r.URL.Query().Get("reassign") != "true" {
			writeError(w, http.StatusConflict,
				"this account still owns "+strconv.Itoa(owned)+
					" server(s) — delete them first, or reassign them to yourself")
			return
		}

		moved, err := s.store.ReassignServers(r.Context(), target.ID, actor.ID)
		if err != nil {
			slog.Error("reassign servers", "from", target.ID, "to", actor.ID, "error", err)
			writeError(w, http.StatusInternalServerError, "could not reassign servers")
			return
		}
		slog.Info("reassigned servers before deleting account",
			"from", target.Username, "to", actor.Username, "servers", moved)
	}

	if err := s.store.DeleteUser(r.Context(), target.ID); err != nil {
		slog.Error("delete user", "user", target.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "could not delete account")
		return
	}

	slog.Info("admin deleted account", "by", actor.Username, "username", target.Username)
	w.WriteHeader(http.StatusNoContent)
}

// --- helpers ---

// lookupUser resolves the {id} path value. It writes the error response
// itself; ok is false when the caller should stop.
func (s *Server) lookupUser(w http.ResponseWriter, r *http.Request) (*store.User, bool) {
	id := r.PathValue("id")
	user, err := s.store.UserByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "user not found")
			return nil, false
		}
		slog.Error("load user", "user", id, "error", err)
		writeError(w, http.StatusInternalServerError, "could not load user")
		return nil, false
	}
	return user, true
}

// ensureAnotherAdmin refuses an operation that would strip the last admin.
// ok is false when the caller should stop.
func (s *Server) ensureAnotherAdmin(w http.ResponseWriter, r *http.Request, target *store.User) bool {
	admins, err := s.store.CountAdmins(r.Context())
	if err != nil {
		slog.Error("count admins", "error", err)
		writeError(w, http.StatusInternalServerError, "could not verify admin count")
		return false
	}
	if target.IsAdmin && admins <= 1 {
		writeError(w, http.StatusConflict, "there must be at least one admin")
		return false
	}
	return true
}
