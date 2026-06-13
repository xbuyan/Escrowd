package api

import (
	"escrowd/internal/audit"
	"escrowd/internal/auth"
	"escrowd/internal/bruteforce"
	"escrowd/internal/ratelimit"
	"escrowd/internal/store"
	"escrowd/internal/watcher"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

var db *store.Store
var limiter *ratelimit.Limiter
var auditLog *audit.Log
var shield *bruteforce.Shield

func Start() {
	var err error
	db, err = store.New("./data")
	if err != nil {
		fmt.Println("could not open database:", err)
		return
	}
	defer db.Close()

	if err := db.MigrateUsers(); err != nil {
		fmt.Println("user migration failed:", err)
		return
	}

	watcher.Start(db)
	limiter = ratelimit.New(10, time.Hour)
	auditLog = audit.New(db.AuditDB)
	shield = bruteforce.New()

	mux := http.NewServeMux()

	// Root info route
	mux.HandleFunc("/", withCORS(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			jsonError(w, "not found", http.StatusNotFound)
			return
		}
		jsonOK(w, map[string]string{
			"service":  "escrowd-api",
			"version":  "1.0.0",
			"status":   "running",
			"frontend": "https://xbuyan.github.io/escrowd-web/",
		})
	}))

	// Public routes — no auth required
	mux.HandleFunc("/api/auth/register", withCORS(handleRegister))
	mux.HandleFunc("/api/auth/login", withCORS(handleLogin))
	mux.HandleFunc("/api/auth/verify-email", withCORS(handleVerifyEmail))
	mux.HandleFunc("/api/auth/resend-verification", withCORS(handleResendVerification))
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	// Invite preview is public (no auth) — accept requires auth
	mux.HandleFunc("/api/invites/", withCORS(handleInviteRoute))

	// Protected routes — JWT required
	mux.HandleFunc("/api/deals", withCORS(requireAuth(handleDeals)))
	mux.HandleFunc("/api/deals/", withCORS(requireAuth(handleDealByID)))

	// Admin routes — JWT + admin flag required
	mux.HandleFunc("/api/admin/deals", withCORS(requireAuth(requireAdmin(handleAdminDeals))))
	mux.HandleFunc("/api/admin/resolve/", withCORS(requireAuth(requireAdmin(handleAdminResolve))))
	mux.HandleFunc("/api/admin/audit", withCORS(requireAuth(requireAdmin(handleAdminAudit))))

	port := os.Getenv("API_PORT")
	if port == "" {
		port = "8080"
	}

	fmt.Printf("Escrowd API running on 0.0.0.0:%s\n", port)
	if err := http.ListenAndServe("0.0.0.0:"+port, mux); err != nil {
		fmt.Println("server error:", err)
	}
}

// handleInviteRoute splits public (view) and authenticated (accept) invite actions.
// GET /api/invites/:token         — public, no auth
// POST /api/invites/:token/accept — requires auth
func handleInviteRoute(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	// parts = ["api", "invites", "{token}"] or [..., "accept"]
	if len(parts) >= 4 && parts[3] == "accept" {
		requireAuth(handleInvite)(w, r)
		return
	}
	handleInvite(w, r)
}

// withCORS handles preflight and sets CORS headers.
func withCORS(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next(w, r)
	}
}

// requireAuth verifies the JWT from the Authorization header.
func requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if header == "" || !strings.HasPrefix(header, "Bearer ") {
			jsonError(w, "authorization required", http.StatusUnauthorized)
			return
		}

		tokenStr := strings.TrimPrefix(header, "Bearer ")
		claims, err := auth.VerifyToken(tokenStr)
		if err != nil {
			jsonError(w, "invalid or expired token", http.StatusUnauthorized)
			return
		}

		r.Header.Set("X-User-ID", claims.UserID)
		r.Header.Set("X-User-Name", claims.UserName)
		if claims.IsAdmin {
			r.Header.Set("X-Is-Admin", "true")
		}

		next(w, r)
	}
}

// requireAdmin checks the IsAdmin flag set by requireAuth.
func requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Is-Admin") != "true" {
			jsonError(w, "admin access required", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}
