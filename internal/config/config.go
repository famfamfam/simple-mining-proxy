// Package config reads the infrastructure settings from the environment.
// Only what cannot be changed from the admin UI lives here: listen
// addresses, admin credentials, paths and the log format.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	StratumTCPAddr     string // "" when off
	StratumTLSAddr     string // "" when off
	AdminAddr          string
	AdminUsername      string
	AdminPassword      string
	APIToken           string
	DataDir            string
	TLSCertFile        string
	TLSKeyFile         string
	TLSSelfSignedHosts []string
	PublicHost         string
	PublicTCPPort      int
	PublicTLSPort      int
	LogFormat          string
	TelegramAPIURL     string // Bot API server; the bot token is set in the admin UI
}

// LogValue keeps secrets out of logs.
func (c *Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("stratum_tcp", orOff(c.StratumTCPAddr)),
		slog.String("stratum_tls", orOff(c.StratumTLSAddr)),
		slog.String("admin", c.AdminAddr),
		slog.String("admin_user", c.AdminUsername),
		slog.Bool("api_token_set", c.APIToken != ""),
		slog.String("telegram_api", c.TelegramAPIURL),
		slog.String("data_dir", c.DataDir),
	)
}

func orOff(s string) string {
	if s == "" {
		return "off"
	}
	return s
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func listenAddr(key, def string) string {
	v := env(key, def)
	if strings.EqualFold(v, "off") {
		return ""
	}
	return v
}

func Load() (*Config, error) {
	c := &Config{
		StratumTCPAddr: listenAddr("STRATUM_TCP_ADDR", ":13333"),
		StratumTLSAddr: listenAddr("STRATUM_TLS_ADDR", ":14443"),
		AdminAddr:      env("ADMIN_ADDR", ":18080"),
		AdminUsername:  env("ADMIN_USERNAME", "admin"),
		AdminPassword:  os.Getenv("ADMIN_PASSWORD"),
		APIToken:       strings.TrimSpace(os.Getenv("API_TOKEN")),
		DataDir:        env("DATA_DIR", "/data"),
		PublicHost:     env("PUBLIC_HOST", ""),
		LogFormat:      strings.ToLower(env("LOG_FORMAT", "json")),
		// Another Bot API server: a local one, or a mirror where
		// api.telegram.org is blocked.
		TelegramAPIURL: strings.TrimRight(env("TELEGRAM_API_URL", "https://api.telegram.org"), "/"),
	}
	c.TLSCertFile = env("TLS_CERT_FILE", filepath.Join(c.DataDir, "certs", "fullchain.pem"))
	c.TLSKeyFile = env("TLS_KEY_FILE", filepath.Join(c.DataDir, "certs", "privkey.pem"))
	for _, h := range strings.Split(env("TLS_SELF_SIGNED_HOSTS", ""), ",") {
		if h = strings.TrimSpace(h); h != "" {
			c.TLSSelfSignedHosts = append(c.TLSSelfSignedHosts, h)
		}
	}
	var errs []error
	if c.AdminPassword == "" {
		errs = append(errs, errors.New("ADMIN_PASSWORD is required"))
	}
	if c.StratumTCPAddr == "" && c.StratumTLSAddr == "" {
		errs = append(errs, errors.New("both STRATUM_TCP_ADDR and STRATUM_TLS_ADDR are off"))
	}
	if c.LogFormat != "json" && c.LogFormat != "text" {
		errs = append(errs, fmt.Errorf("LOG_FORMAT must be json or text, got %q", c.LogFormat))
	}
	if u, err := url.Parse(c.TelegramAPIURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		errs = append(errs, fmt.Errorf("TELEGRAM_API_URL must be an http(s) URL, got %q", c.TelegramAPIURL))
	}
	var err error
	if c.PublicTCPPort, err = port("PUBLIC_TCP_PORT"); err != nil {
		errs = append(errs, err)
	}
	if c.PublicTLSPort, err = port("PUBLIC_TLS_PORT"); err != nil {
		errs = append(errs, err)
	}
	return c, errors.Join(errs...)
}

func port(key string) (int, error) {
	v := env(key, "")
	if v == "" {
		return 0, nil
	}
	p, err := strconv.Atoi(v)
	if err != nil || p < 1 || p > 65535 {
		return 0, fmt.Errorf("%s must be a port number, got %q", key, v)
	}
	return p, nil
}

// AdminHealthURL is used by the healthcheck subcommand inside the container.
func AdminHealthURL() string {
	addr := env("ADMIN_ADDR", ":18080")
	host, p, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://127.0.0.1:18080/api/health"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, p) + "/api/health"
}
