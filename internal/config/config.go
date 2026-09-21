package config

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/julen8/fancontrol/internal/control"
	"golang.org/x/crypto/bcrypt"
)

type Duration struct {
	time.Duration
}

func (duration *Duration) UnmarshalText(text []byte) error {
	parsed, err := time.ParseDuration(string(text))
	if err != nil {
		return err
	}
	duration.Duration = parsed
	return nil
}

func (duration Duration) MarshalText() ([]byte, error) {
	return []byte(duration.String()), nil
}

func (duration Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(duration.String())
}

func (duration *Duration) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	return duration.UnmarshalText([]byte(value))
}

type Config struct {
	Version int              `toml:"version" json:"version"`
	Server  ServerConfig     `toml:"server" json:"server"`
	IPMI    IPMIConfig       `toml:"ipmi" json:"ipmi"`
	CPU     ControllerConfig `toml:"cpu" json:"cpu"`
	HDD     HDDConfig        `toml:"hdd" json:"hdd"`
}

type ServerConfig struct {
	Listen         string     `toml:"listen" json:"listen"`
	SessionTimeout Duration   `toml:"session_timeout" json:"session_timeout"`
	Auth           AuthConfig `toml:"auth" json:"auth"`
}

type AuthConfig struct {
	Enabled      bool   `toml:"enabled" json:"enabled"`
	Username     string `toml:"username" json:"username"`
	PasswordHash string `toml:"password_hash" json:"-"`
}

type IPMIConfig struct {
	Command         string   `toml:"command" json:"command"`
	Timeout         Duration `toml:"timeout" json:"timeout"`
	EnforceFullMode bool     `toml:"enforce_full_mode" json:"enforce_full_mode"`
	ExitSpeed       int      `toml:"exit_speed" json:"exit_speed"`
}

type ControllerConfig struct {
	Enabled             bool            `toml:"enabled" json:"enabled"`
	Mode                string          `toml:"mode" json:"mode"`
	Zone                int             `toml:"zone" json:"zone"`
	PollInterval        Duration        `toml:"poll_interval" json:"poll_interval"`
	FixedSpeed          int             `toml:"fixed_speed" json:"fixed_speed"`
	MinimumSpeed        int             `toml:"minimum_speed" json:"minimum_speed"`
	SafeSpeed           int             `toml:"safe_speed" json:"safe_speed"`
	CriticalTemperature float64         `toml:"critical_temperature" json:"critical_temperature"`
	ReadFailureLimit    int             `toml:"read_failure_limit" json:"read_failure_limit"`
	SmoothingSamples    int             `toml:"smoothing_samples" json:"smoothing_samples"`
	Curve               []control.Point `toml:"curve" json:"curve"`
}

type HDDConfig struct {
	ControllerConfig
	AvoidWakeup bool     `toml:"avoid_wakeup" json:"avoid_wakeup"`
	Devices     []string `toml:"devices" json:"devices"`
}

type InitialCredentials struct {
	Username string
	Password string
}

func Default() Config {
	return Config{
		Version: 1,
		Server: ServerConfig{
			Listen:         "0.0.0.0:8080",
			SessionTimeout: Duration{24 * time.Hour},
			Auth:           AuthConfig{Enabled: true},
		},
		IPMI: IPMIConfig{
			Command:         "/usr/bin/ipmitool",
			Timeout:         Duration{10 * time.Second},
			EnforceFullMode: true,
			ExitSpeed:       100,
		},
		CPU: ControllerConfig{
			Enabled:             true,
			Mode:                control.ModeCurve,
			Zone:                0,
			PollInterval:        Duration{3 * time.Second},
			FixedSpeed:          50,
			MinimumSpeed:        40,
			SafeSpeed:           100,
			CriticalTemperature: 80,
			ReadFailureLimit:    3,
			SmoothingSamples:    3,
			Curve: []control.Point{
				{Temperature: 35, Speed: 40},
				{Temperature: 50, Speed: 55},
				{Temperature: 65, Speed: 100},
			},
		},
		HDD: HDDConfig{
			ControllerConfig: ControllerConfig{
				Enabled:             false,
				Mode:                control.ModeCurve,
				Zone:                1,
				PollInterval:        Duration{2 * time.Minute},
				FixedSpeed:          40,
				MinimumSpeed:        20,
				SafeSpeed:           100,
				CriticalTemperature: 50,
				ReadFailureLimit:    3,
				SmoothingSamples:    1,
				Curve: []control.Point{
					{Temperature: 30, Speed: 20},
					{Temperature: 38, Speed: 45},
					{Temperature: 45, Speed: 100},
				},
			},
			AvoidWakeup: true,
			Devices:     []string{},
		},
	}
}

func LoadOrCreate(path string) (Config, *InitialCredentials, error) {
	configuration := Default()
	_, statErr := os.Stat(path)
	newFile := errors.Is(statErr, os.ErrNotExist)
	if statErr != nil && !newFile {
		return Config{}, nil, fmt.Errorf("stat config: %w", statErr)
	}
	if !newFile {
		if _, err := toml.DecodeFile(path, &configuration); err != nil {
			return Config{}, nil, fmt.Errorf("decode config: %w", err)
		}
	}

	credentials, err := ensureCredentials(&configuration)
	if err != nil {
		return Config{}, nil, err
	}
	if err := Validate(configuration); err != nil {
		return Config{}, nil, err
	}
	if newFile || credentials != nil {
		if err := Save(path, configuration); err != nil {
			return Config{}, nil, err
		}
	}
	return configuration, credentials, nil
}

func ensureCredentials(configuration *Config) (*InitialCredentials, error) {
	if configuration.Server.Auth.Username != "" && configuration.Server.Auth.PasswordHash != "" {
		return nil, nil
	}
	passwordBytes := make([]byte, 18)
	if _, err := rand.Read(passwordBytes); err != nil {
		return nil, fmt.Errorf("generate initial password: %w", err)
	}
	password := base64.RawURLEncoding.EncodeToString(passwordBytes)
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hash initial password: %w", err)
	}
	configuration.Server.Auth.Enabled = true
	configuration.Server.Auth.Username = "admin"
	configuration.Server.Auth.PasswordHash = string(hash)
	return &InitialCredentials{Username: "admin", Password: password}, nil
}

func ResetPassword(path string, configuration Config) (*InitialCredentials, error) {
	configuration.Server.Auth.PasswordHash = ""
	credentials, err := ensureCredentials(&configuration)
	if err != nil {
		return nil, err
	}
	if err := Save(path, configuration); err != nil {
		return nil, err
	}
	return credentials, nil
}

func Validate(configuration Config) error {
	if configuration.Version != 1 {
		return fmt.Errorf("unsupported config version %d", configuration.Version)
	}
	if _, _, err := net.SplitHostPort(configuration.Server.Listen); err != nil {
		return fmt.Errorf("invalid server listen address: %w", err)
	}
	if configuration.Server.SessionTimeout.Duration <= 0 {
		return errors.New("session timeout must be positive")
	}
	if configuration.Server.Auth.Enabled && (configuration.Server.Auth.Username == "" || configuration.Server.Auth.PasswordHash == "") {
		return errors.New("enabled authentication requires username and password hash")
	}
	if configuration.IPMI.Command == "" || configuration.IPMI.Timeout.Duration <= 0 {
		return errors.New("IPMI command and positive timeout are required")
	}
	if configuration.IPMI.ExitSpeed < 1 || configuration.IPMI.ExitSpeed > 100 {
		return errors.New("IPMI exit speed must be between 1 and 100")
	}
	if err := validateController("cpu", configuration.CPU); err != nil {
		return err
	}
	if err := validateController("hdd", configuration.HDD.ControllerConfig); err != nil {
		return err
	}
	if configuration.CPU.Enabled && configuration.HDD.Enabled && configuration.CPU.Zone == configuration.HDD.Zone {
		return errors.New("CPU and HDD controllers must use different IPMI zones")
	}
	if configuration.HDD.Enabled && len(configuration.HDD.Devices) == 0 {
		return errors.New("enabled HDD controller requires at least one selected disk")
	}
	return nil
}

func validateController(name string, controller ControllerConfig) error {
	if controller.Zone < 0 || controller.Zone > 255 {
		return fmt.Errorf("%s zone must be between 0 and 255", name)
	}
	if controller.PollInterval.Duration <= 0 {
		return fmt.Errorf("%s poll interval must be positive", name)
	}
	if controller.SafeSpeed < controller.MinimumSpeed || controller.SafeSpeed > 100 {
		return fmt.Errorf("%s safe speed must be between minimum speed and 100", name)
	}
	if controller.CriticalTemperature <= 0 || controller.CriticalTemperature > 120 {
		return fmt.Errorf("%s critical temperature must be between 0 and 120", name)
	}
	if controller.ReadFailureLimit < 1 || controller.SmoothingSamples < 1 {
		return fmt.Errorf("%s failure limit and smoothing samples must be positive", name)
	}
	if err := control.Validate(controller.Mode, controller.FixedSpeed, controller.MinimumSpeed, controller.Curve); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

func Save(path string, configuration Config) error {
	if err := Validate(configuration); err != nil {
		return err
	}
	var data bytes.Buffer
	if err := toml.NewEncoder(&data).Encode(configuration); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".fancontrol-*.toml")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data.Bytes()); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open config directory: %w", err)
	}
	defer directoryHandle.Close()
	if err := directoryHandle.Sync(); err != nil {
		return fmt.Errorf("sync config directory: %w", err)
	}
	return nil
}
