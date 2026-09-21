package config

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestLoadOrCreateCreatesSecureConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "fancontrol.toml")
	configuration, credentials, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	if credentials == nil || credentials.Username != "admin" || credentials.Password == "" {
		t.Fatal("initial credentials were not returned")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(configuration.Server.Auth.PasswordHash), []byte(credentials.Password)); err != nil {
		t.Fatal("generated password does not match stored hash")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config permissions = %o, want 600", info.Mode().Perm())
	}

	_, secondCredentials, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	if secondCredentials != nil {
		t.Fatal("credentials were regenerated for an existing valid config")
	}
}

func TestConfigRoundTripDurations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fancontrol.toml")
	configuration, _, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	if configuration.CPU.PollInterval.String() != "3s" {
		t.Fatalf("CPU poll interval = %s", configuration.CPU.PollInterval.String())
	}
}
