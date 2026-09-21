package web

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/julen8/fancontrol/internal/app"
	"github.com/julen8/fancontrol/internal/config"
	"golang.org/x/crypto/bcrypt"
)

//go:embed static/*
var assets embed.FS

type session struct {
	ExpiresAt time.Time
}

type Server struct {
	manager  *app.Manager
	logger   *slog.Logger
	sessions map[string]session
	mu       sync.Mutex
}

func New(manager *app.Manager, logger *slog.Logger) http.Handler {
	server := &Server{manager: manager, logger: logger, sessions: make(map[string]session)}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/auth/login", server.login)
	mux.HandleFunc("POST /api/auth/logout", server.auth(server.logout))
	mux.HandleFunc("GET /api/auth/session", server.sessionStatus)
	mux.HandleFunc("PUT /api/auth/password", server.auth(server.changePassword))
	mux.HandleFunc("GET /api/status", server.auth(server.getStatus))
	mux.HandleFunc("GET /api/config", server.auth(server.getConfig))
	mux.HandleFunc("PUT /api/config", server.auth(server.putConfig))
	mux.HandleFunc("GET /api/disks", server.auth(server.getDisks))
	mux.HandleFunc("POST /api/disks/rescan", server.auth(server.getDisks))
	mux.HandleFunc("POST /api/fans/test", server.auth(server.testFan))
	mux.HandleFunc("GET /api/events", server.auth(server.getEvents))
	staticFS, _ := fs.Sub(assets, "static")
	mux.Handle("/", http.FileServer(http.FS(staticFS)))
	return server.securityHeaders(mux)
}

func (server *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("X-Content-Type-Options", "nosniff")
		response.Header().Set("X-Frame-Options", "DENY")
		response.Header().Set("Referrer-Policy", "no-referrer")
		response.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'; connect-src 'self'")
		if request.Method != http.MethodGet && request.Method != http.MethodHead && !sameOrigin(request) {
			writeError(response, http.StatusForbidden, "request origin does not match this server")
			return
		}
		next.ServeHTTP(response, request)
	})
}

func sameOrigin(request *http.Request) bool {
	origin := request.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	return err == nil && strings.EqualFold(parsed.Host, request.Host)
}

func (server *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		configuration := server.manager.Config()
		if !configuration.Server.Auth.Enabled {
			next(response, request)
			return
		}
		cookie, err := request.Cookie("fancontrol_session")
		if err != nil || !server.validSession(cookie.Value) {
			writeError(response, http.StatusUnauthorized, "authentication required")
			return
		}
		next(response, request)
	}
}

func (server *Server) validSession(token string) bool {
	server.mu.Lock()
	defer server.mu.Unlock()
	value, exists := server.sessions[token]
	if !exists || time.Now().After(value.ExpiresAt) {
		delete(server.sessions, token)
		return false
	}
	return true
}

func (server *Server) login(response http.ResponseWriter, request *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(request, &body); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	auth := server.manager.Config().Server.Auth
	if body.Username != auth.Username || bcrypt.CompareHashAndPassword([]byte(auth.PasswordHash), []byte(body.Password)) != nil {
		time.Sleep(350 * time.Millisecond)
		writeError(response, http.StatusUnauthorized, "invalid username or password")
		return
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		writeError(response, http.StatusInternalServerError, "could not create session")
		return
	}
	token := hex.EncodeToString(tokenBytes)
	expires := time.Now().Add(server.manager.Config().Server.SessionTimeout.Duration)
	server.mu.Lock()
	server.sessions[token] = session{ExpiresAt: expires}
	server.mu.Unlock()
	http.SetCookie(response, &http.Cookie{Name: "fancontrol_session", Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Expires: expires})
	writeJSON(response, http.StatusOK, map[string]any{"authenticated": true, "username": auth.Username})
}

func (server *Server) logout(response http.ResponseWriter, request *http.Request) {
	if cookie, err := request.Cookie("fancontrol_session"); err == nil {
		server.mu.Lock()
		delete(server.sessions, cookie.Value)
		server.mu.Unlock()
	}
	http.SetCookie(response, &http.Cookie{Name: "fancontrol_session", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	writeJSON(response, http.StatusOK, map[string]bool{"authenticated": false})
}

func (server *Server) sessionStatus(response http.ResponseWriter, request *http.Request) {
	auth := server.manager.Config().Server.Auth
	authenticated := !auth.Enabled
	if cookie, err := request.Cookie("fancontrol_session"); err == nil {
		authenticated = server.validSession(cookie.Value)
	}
	writeJSON(response, http.StatusOK, map[string]any{"authenticated": authenticated, "username": auth.Username})
}

func (server *Server) changePassword(response http.ResponseWriter, request *http.Request) {
	var body struct {
		Current string `json:"current"`
		Next    string `json:"next"`
	}
	if err := decodeJSON(request, &body); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	if len(body.Next) < 6 {
		writeError(response, http.StatusBadRequest, "new password must contain at least 6 characters")
		return
	}
	currentHash := server.manager.Config().Server.Auth.PasswordHash
	if bcrypt.CompareHashAndPassword([]byte(currentHash), []byte(body.Current)) != nil {
		writeError(response, http.StatusUnauthorized, "current password is incorrect")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(body.Next), bcrypt.DefaultCost)
	if err != nil || server.manager.UpdatePassword(string(hash)) != nil {
		writeError(response, http.StatusInternalServerError, "could not update password")
		return
	}
	server.mu.Lock()
	server.sessions = make(map[string]session)
	server.mu.Unlock()
	http.SetCookie(response, &http.Cookie{Name: "fancontrol_session", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	writeJSON(response, http.StatusOK, map[string]bool{"changed": true})
}

func (server *Server) getStatus(response http.ResponseWriter, _ *http.Request) {
	writeJSON(response, http.StatusOK, server.manager.Status())
}

func (server *Server) getConfig(response http.ResponseWriter, _ *http.Request) {
	writeJSON(response, http.StatusOK, server.manager.Config())
}

func (server *Server) putConfig(response http.ResponseWriter, request *http.Request) {
	var configuration config.Config
	if err := decodeJSON(request, &configuration); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	if err := server.manager.UpdateConfig(configuration); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(response, http.StatusOK, server.manager.Config())
}

func (server *Server) getDisks(response http.ResponseWriter, request *http.Request) {
	disks, err := server.manager.DiscoverDisks(request.Context())
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeJSON(response, http.StatusOK, disks)
}

func (server *Server) testFan(response http.ResponseWriter, request *http.Request) {
	var body struct {
		Zone     int `json:"zone"`
		Speed    int `json:"speed"`
		Duration int `json:"duration"`
	}
	if err := decodeJSON(request, &body); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	if body.Speed < 1 || body.Speed > 100 {
		writeError(response, http.StatusBadRequest, "speed must be between 1 and 100")
		return
	}
	if err := server.manager.TestFan(request.Context(), body.Zone, body.Speed, time.Duration(body.Duration)*time.Second); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(response, http.StatusAccepted, map[string]bool{"started": true})
}

func (server *Server) getEvents(response http.ResponseWriter, _ *http.Request) {
	writeJSON(response, http.StatusOK, server.manager.Events())
}

func decodeJSON(request *http.Request, target any) error {
	defer request.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(request.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request must contain one JSON object")
	}
	return nil
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func writeError(response http.ResponseWriter, status int, message string) {
	writeJSON(response, status, map[string]string{"error": message})
}
