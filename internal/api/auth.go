package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

var (
	authUser     string
	authPassword string
	hmacKey      []byte
	authOnce     sync.Once
)

func loadAuthConfig() {
	authOnce.Do(func() {
		authUser = os.Getenv("COSTDECK_AUTH_USER")
		authPassword = os.Getenv("COSTDECK_AUTH_PASSWORD")
		if authPassword != "" {
			hmacKey = []byte(authPassword + "-costdeck-hmac-key")
		}
	})
}

// AuthMiddleware wraps the handler with session-cookie authentication.
// If COSTDECK_AUTH_USER is not set, auth is disabled (dev mode).
func AuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		loadAuthConfig()
		if authUser == "" || authPassword == "" {
			next.ServeHTTP(w, r)
			return
		}

		path := r.URL.Path

		// Always allow these endpoints without auth
		if path == "/api/login" || path == "/api/logout" || path == "/api/docs" || path == "/api/openapi.yaml" {
			next.ServeHTTP(w, r)
			return
		}

		// Allow static assets (the React SPA) without auth
		if !strings.HasPrefix(path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}

		// All /api/* endpoints require a valid session cookie
		cookie, err := r.Cookie("costdeck-session")
		if err != nil || !validateSession(cookie.Value) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"error": "Authentication required"})
			return
		}

		next.ServeHTTP(w, r)
	})
}

// HandleLogin processes POST /api/login requests.
func HandleLogin(w http.ResponseWriter, r *http.Request) {
	loadAuthConfig()

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// If auth is disabled, always succeed
	if authUser == "" || authPassword == "" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		return
	}

	var creds struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&creds); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if creds.Username != authUser || creds.Password != authPassword {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid credentials"})
		return
	}

	// Generate session token: timestamp|hmac(timestamp)
	token := generateSession()
	http.SetCookie(w, &http.Cookie{
		Name:     "costdeck-session",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   86400, // 24 hours
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// HandleLogout clears the session cookie.
func HandleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     "costdeck-session",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		MaxAge:   -1,
	})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func generateSession() string {
	ts := fmt.Sprintf("%d", time.Now().Unix())
	mac := hmac.New(sha256.New, hmacKey)
	mac.Write([]byte(ts))
	sig := hex.EncodeToString(mac.Sum(nil))
	return ts + "." + sig
}

func validateSession(token string) bool {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return false
	}

	ts := parts[0]
	sig := parts[1]

	// Check if token is expired (24h)
	var tokenTime int64
	fmt.Sscanf(ts, "%d", &tokenTime)
	if time.Now().Unix()-tokenTime > 86400 {
		return false
	}

	// Verify HMAC
	mac := hmac.New(sha256.New, hmacKey)
	mac.Write([]byte(ts))
	expectedSig := hex.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(sig), []byte(expectedSig))
}
