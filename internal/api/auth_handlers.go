package api

import (
	"fmt"
	"github.com/xbuyan/Escrowd/internal/auth"
	"github.com/xbuyan/Escrowd/internal/email"
	"github.com/xbuyan/Escrowd/internal/store"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

var emailRegex = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)

// frontendURL returns the base URL of the web frontend for building links
// sent in emails (verification, deal invitations).
func frontendURL() string {
	url := os.Getenv("FRONTEND_URL")
	if url == "" {
		return "https://xbuyan.github.io/escrowd-web"
	}
	return url
}

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

	exists, err := db.UserExists(body.Email)
	if err != nil {
		jsonError(w, "database error", http.StatusInternalServerError)
		return
	}
	if exists {
		jsonError(w, "email already registered", http.StatusConflict)
		return
	}

	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		jsonError(w, "could not hash password", http.StatusInternalServerError)
		return
	}

	adminEmail := os.Getenv("ESCROWD_ADMIN_EMAIL")
	isAdmin := adminEmail != "" && body.Email == adminEmail

	user := store.User{
		ID:            uuid.NewString(),
		Username:      body.Username,
		Email:         body.Email,
		PasswordHash:  hash,
		IsAdmin:       isAdmin,
		EmailVerified: false,
		CreatedAt:     time.Now(),
	}

	if err := db.CreateUser(user); err != nil {
		jsonError(w, "could not create user", http.StatusInternalServerError)
		return
	}

	// Generate verification token and send email
	token := uuid.NewString()
	if err := db.CreateVerificationToken(token, user.ID); err != nil {
		jsonError(w, "could not create verification token", http.StatusInternalServerError)
		return
	}

	verifyURL := fmt.Sprintf("%s/#/verify-email?token=%s", frontendURL(), token)
	if err := email.SendVerificationEmail(user.Email, user.Username, verifyURL); err != nil {
		// Log but don't fail registration — user can request resend
		fmt.Println("warning: could not send verification email:", err)
	}

	auditLog.Record(user.ID, "user_registered", user.ID, user.Username,
		fmt.Sprintf("Registered with email %s", user.Email))

	jsonOK(w, map[string]any{
		"message":        "Registration successful. Please check your email to verify your account.",
		"user_id":        user.ID,
		"username":       user.Username,
		"email_verified": false,
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

	if blocked, _ := shield.IsLocked(fmt.Sprintf("login:%s", body.Email)); blocked {
		jsonError(w, "too many failed attempts — try again in 1 hour", http.StatusTooManyRequests)
		return
	}

	user, err := db.GetUserByEmail(body.Email)
	if err != nil {
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

	// Block login until email is verified
	if !user.EmailVerified {
		jsonError(w, "please verify your email address before logging in — check your inbox for the verification link", http.StatusForbidden)
		return
	}

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

// handleVerifyEmail processes the verification link a user clicks from their email.
func handleVerifyEmail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var body struct {
		Token string `json:"token"`
	}
	if err := decode(r, &body); err != nil || body.Token == "" {
		jsonError(w, "verification token is required", http.StatusBadRequest)
		return
	}

	if err := db.VerifyEmailToken(body.Token); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}

	jsonOK(w, map[string]string{"message": "Email verified successfully. You can now log in."})
}

// handleResendVerification issues a new verification token if the user
// exists and is not yet verified. Always returns success to avoid
// leaking whether an email is registered.
func handleResendVerification(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var body struct {
		Email string `json:"email"`
	}
	if err := decode(r, &body); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	body.Email = strings.ToLower(strings.TrimSpace(body.Email))

	// Rate limit resend requests per email to prevent abuse
	if blocked, _ := shield.IsLocked(fmt.Sprintf("resend:%s", body.Email)); blocked {
		jsonError(w, "too many resend requests — try again later", http.StatusTooManyRequests)
		return
	}
	shield.RecordFailure(fmt.Sprintf("resend:%s", body.Email)) // counts towards rate limit regardless

	user, err := db.GetUserByEmail(body.Email)
	if err != nil {
		// Always return generic success — don't leak registration status
		jsonOK(w, map[string]string{"message": "If this email is registered and unverified, a new verification link has been sent."})
		return
	}

	if user.EmailVerified {
		jsonOK(w, map[string]string{"message": "If this email is registered and unverified, a new verification link has been sent."})
		return
	}

	db.DeleteVerificationTokensForUser(user.ID)

	token := uuid.NewString()
	if err := db.CreateVerificationToken(token, user.ID); err != nil {
		jsonError(w, "could not create verification token", http.StatusInternalServerError)
		return
	}

	verifyURL := fmt.Sprintf("%s/#/verify-email?token=%s", frontendURL(), token)
	if err := email.SendVerificationEmail(user.Email, user.Username, verifyURL); err != nil {
		fmt.Println("warning: could not send verification email:", err)
	}

	jsonOK(w, map[string]string{"message": "If this email is registered and unverified, a new verification link has been sent."})
}
