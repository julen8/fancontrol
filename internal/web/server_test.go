package web

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/julen8/fancontrol/internal/app"
	"github.com/julen8/fancontrol/internal/config"
	"golang.org/x/crypto/bcrypt"
)

func TestConfigAPIRequiresLogin(t *testing.T) {
	configuration := config.Default()
	hash, err := bcrypt.GenerateFromPassword([]byte("long-test-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	configuration.Server.Auth.Username = "admin"
	configuration.Server.Auth.PasswordHash = string(hash)
	manager := app.New(configuration, t.TempDir()+"/config.toml", nil, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server := httptest.NewServer(New(manager, slog.Default()))
	defer server.Close()

	response, err := http.Get(server.URL + "/api/config")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want %d", response.StatusCode, http.StatusUnauthorized)
	}

	client := &http.Client{}
	login, err := client.Post(server.URL+"/api/auth/login", "application/json", bytes.NewBufferString(`{"username":"admin","password":"long-test-password"}`))
	if err != nil {
		t.Fatal(err)
	}
	login.Body.Close()
	if login.StatusCode != http.StatusOK || len(login.Cookies()) == 0 {
		t.Fatalf("login status = %d, cookies = %d", login.StatusCode, len(login.Cookies()))
	}

	request, _ := http.NewRequest(http.MethodGet, server.URL+"/api/config", nil)
	request.AddCookie(login.Cookies()[0])
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("authenticated status = %d, want %d", response.StatusCode, http.StatusOK)
	}
}
