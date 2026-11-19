package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/roman/kidstracker/internal/auth"
	"github.com/roman/kidstracker/internal/domain"
	"github.com/roman/kidstracker/internal/store"
)

const sessionTTL = 24 * time.Hour

var errUnauthenticated = errors.New("authentication required")

type principalContextKey struct{}

func (s *Server) userAuth(next func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, err := s.authenticate(r)
		if errors.Is(err, errUnauthenticated) {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not authenticate request")
			return
		}
		ctx := context.WithValue(r.Context(), principalContextKey{}, principal)
		next(w, r.WithContext(ctx))
	}
}

func (s *Server) adminAuth(next func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return s.userAuth(func(w http.ResponseWriter, r *http.Request) {
		if principalFromContext(r).Role != domain.RoleAdmin {
			writeError(w, http.StatusForbidden, "admin access required")
			return
		}
		next(w, r)
	})
}

func principalFromContext(r *http.Request) domain.Principal {
	principal, _ := r.Context().Value(principalContextKey{}).(domain.Principal)
	return principal
}

func (s *Server) authenticate(r *http.Request) (domain.Principal, error) {
	token := bearerToken(r)
	if token == "" {
		return domain.Principal{}, errUnauthenticated
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(s.adminToken)) == 1 {
		return domain.Principal{
			Username:    "admin",
			DisplayName: "Admin key",
			Role:        domain.RoleAdmin,
			AuthType:    domain.AuthTypeAdminKey,
		}, nil
	}
	session, err := s.repo.SessionByToken(r.Context(), tokenDigest(token))
	if errors.Is(err, store.ErrNotFound) {
		return domain.Principal{}, errUnauthenticated
	}
	if err != nil {
		return domain.Principal{}, err
	}
	if session.Role != domain.RoleAdmin && session.Role != domain.RoleUser {
		return domain.Principal{}, errUnauthenticated
	}
	principal := domain.Principal{
		Username:    session.Username,
		DisplayName: session.DisplayName,
		Role:        session.Role,
		AuthType:    domain.AuthTypePassword,
		ExpiresAt:   &session.ExpiresAt,
	}
	if session.UserID == nil {
		principal.Username = "admin"
		principal.DisplayName = "Admin key"
		principal.AuthType = domain.AuthTypeAdminKey
	}
	return principal, nil
}

type authUserView struct {
	Username    string     `json:"username"`
	DisplayName string     `json:"display_name"`
	Role        string     `json:"role"`
	AuthType    string     `json:"auth_type"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
}

func authUserViewFromPrincipal(principal domain.Principal) authUserView {
	return authUserView{
		Username:    principal.Username,
		DisplayName: principal.DisplayName,
		Role:        principal.Role,
		AuthType:    principal.AuthType,
		ExpiresAt:   principal.ExpiresAt,
	}
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var request struct {
		AdminKey string `json:"admin_key"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	hasAdminKey := strings.TrimSpace(request.AdminKey) != ""
	hasUserCredentials := strings.TrimSpace(request.Username) != "" || request.Password != ""
	if hasAdminKey && hasUserCredentials {
		writeError(w, http.StatusBadRequest, "provide either an admin key or a username and password")
		return
	}
	if hasAdminKey {
		provided := strings.TrimSpace(request.AdminKey)
		if subtle.ConstantTimeCompare([]byte(provided), []byte(s.adminToken)) != 1 {
			writeError(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		token, expiresAt, err := s.createSession(r.Context(), nil, domain.RoleAdmin, "admin key")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not create session")
			return
		}
		principal := domain.Principal{
			Username:    "admin",
			DisplayName: "Admin key",
			Role:        domain.RoleAdmin,
			AuthType:    domain.AuthTypeAdminKey,
			ExpiresAt:   &expiresAt,
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"token": token,
			"user":  authUserViewFromPrincipal(principal),
		})
		return
	}
	if !hasUserCredentials {
		writeError(w, http.StatusBadRequest, "admin key or username and password required")
		return
	}
	username := normalizeUsername(request.Username)
	if !validUsername(username) || request.Password == "" {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	user, err := s.repo.UserByUsername(r.Context(), username)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not authenticate user")
		return
	}
	if !auth.VerifyPassword(user.PasswordHash, request.Password) {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	_ = s.repo.MarkUserLogin(r.Context(), user.ID)
	token, expiresAt, err := s.createSession(r.Context(), &user.ID, user.Role, "web")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create session")
		return
	}
	principal := domain.Principal{
		Username:    user.Username,
		DisplayName: user.DisplayName,
		Role:        user.Role,
		AuthType:    domain.AuthTypePassword,
		ExpiresAt:   &expiresAt,
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token": token,
		"user":  authUserViewFromPrincipal(principal),
	})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	token := bearerToken(r)
	if token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(s.adminToken)) != 1 {
		if err := s.repo.DeleteSession(r.Context(), tokenDigest(token)); err != nil {
			writeError(w, http.StatusInternalServerError, "could not close session")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "logged_out"})
}

func (s *Server) currentUser(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"user": authUserViewFromPrincipal(principalFromContext(r)),
	})
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.repo.ListUsers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load users")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": users})
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
		Password    string `json:"password"`
		Role        string `json:"role"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	username := normalizeUsername(request.Username)
	if !validUsername(username) {
		writeError(w, http.StatusBadRequest, "username must be 3 to 64 characters using letters, numbers, dot, underscore or hyphen")
		return
	}
	displayName := strings.TrimSpace(request.DisplayName)
	if displayName == "" {
		displayName = username
	}
	if len(displayName) > 120 {
		writeError(w, http.StatusBadRequest, "display name must be at most 120 characters")
		return
	}
	if len(request.Password) < 10 || len(request.Password) > 256 {
		writeError(w, http.StatusBadRequest, "password must contain 10 to 256 characters")
		return
	}
	role := strings.TrimSpace(request.Role)
	if role == "" {
		role = domain.RoleUser
	}
	if role != domain.RoleAdmin && role != domain.RoleUser {
		writeError(w, http.StatusBadRequest, "role must be admin or user")
		return
	}
	passwordHash, err := auth.HashPassword(request.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not hash password")
		return
	}
	id, err := randomToken(16)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create user")
		return
	}
	user, err := s.repo.CreateUser(r.Context(), id, username, displayName, passwordHash, role)
	if errors.Is(err, store.ErrDuplicate) {
		writeError(w, http.StatusConflict, "username already exists")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create user")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"user": user})
}

func (s *Server) createSession(ctx context.Context, userID *string, role, label string) (string, time.Time, error) {
	token, err := randomToken(32)
	if err != nil {
		return "", time.Time{}, err
	}
	expiresAt := time.Now().UTC().Add(sessionTTL)
	if err := s.repo.CreateSession(ctx, tokenDigest(token), userID, role, label, expiresAt); err != nil {
		return "", time.Time{}, err
	}
	return token, expiresAt, nil
}

func tokenDigest(value string) []byte {
	sum := sha256.Sum256([]byte(value))
	return sum[:]
}

func normalizeUsername(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func validUsername(value string) bool {
	if len(value) < 3 || len(value) > 64 {
		return false
	}
	for _, char := range value {
		if !((char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') ||
			char == '.' || char == '_' || char == '-') {
			return false
		}
	}
	return true
}
