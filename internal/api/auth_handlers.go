package api

import (
	"escrowd/internal/auth"
	"escrowd/internal/store"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

var emailRegex = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)

func handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var body struct {
		Username string `json:"username"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decode(r, &body); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Validate inputs
	body.Username = strings.TrimSpace(body.Username)
	body.Email = strings.ToLower(strings.TrimSpace(body.Email))

	if len(body.Username) < 3 || len(body.Username) > 32 {
		jsonError(w, "username must be 3-32 characters", http.StatusBadRequest)
		return
	}
	if !emailRegex.MatchString(body.Email) {
		jsonError(w, "invalid email address", http.StatusBadRequest)
		return
	}
	if len(body.Password) < 8 {
		jsonError(w, "password must be at least 8 characters", http.StatusBadRequest)
		return
	}
	if len(body.Password) > 128 {
		jsonError(w, "password too long", http.StatusBadRequest)
		return
	}

	// Check if email already registered
	exists, err := db.UserExists(body.Email)
	if err != nil {
		jsonError(w, "database error", http.StatusInternalServerError)
		return
	}
	if exists {
		jsonError(w, "email already registered", http.StatusConflict)
		return
	}

	// Hash password with Argon2id
	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		jsonError(w, "could not hash password", http.StatusInternalServerError)
		return
	}

	// Check if this email is the configured admin
	adminEmail := os.Getenv("ESCROWD_ADMIN_EMAIL")
	isAdmin := adminEmail != "" && body.Email == adminEmail

	user := store.User{
		ID:           uuid.NewString(),
		Username:     body.Username,
		Email:        body.Email,
		PasswordHash: hash,
		IsAdmin:      isAdmin,
		CreatedAt:    time.Now(),
	}

	if err := db.CreateUser(user); err != nil {
		jsonError(w, "could not create user", http.StatusInternalServerError)
		return
	}

	// Issue token immediately on registration
	token, err := auth.IssueToken(user.ID, user.Username, user.IsAdmin)
	if err != nil {
		jsonError(w, "could not issue token", http.StatusInternalServerError)
		return
	}

	jsonOK(w, map[string]any{
		"token":    token,
		"user_id":  user.ID,
		"username": user.Username,
		"is_admin": user.IsAdmin,
	})
}

func handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decode(r, &body); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	body.Email = strings.ToLower(strings.TrimSpace(body.Email))

	if body.Email == "" || body.Password == "" {
		jsonError(w, "email and password are required", http.StatusBadRequest)
		return
	}

	// Brute force protection
	if blocked, _ := shield.IsLocked(fmt.Sprintf("login:%s", body.Email)); blocked {
		jsonError(w, "too many failed attempts — try again in 1 hour", http.StatusTooManyRequests)
		return
	}

	user, err := db.GetUserByEmail(body.Email)
	if err != nil {
		// Don't reveal whether email exists — generic error
		shield.RecordFailure(fmt.Sprintf("login:%s", body.Email))
		jsonError(w, "invalid email or password", http.StatusUnauthorized)
		return
	}

	ok, err := auth.VerifyPassword(body.Password, user.PasswordHash)
	if err != nil || !ok {
		shield.RecordFailure(fmt.Sprintf("login:%s", body.Email))
		jsonError(w, "invalid email or password", http.StatusUnauthorized)
		return
	}

	shield.RecordSuccess(fmt.Sprintf("login:%s", body.Email))

	token, err := auth.IssueToken(user.ID, user.Username, user.IsAdmin)
	if err != nil {
		jsonError(w, "could not issue token", http.StatusInternalServerError)
		return
	}

	jsonOK(w, map[string]any{
		"token":    token,
		"user_id":  user.ID,
		"username": user.Username,
		"is_admin": user.IsAdmin,
	})
}
