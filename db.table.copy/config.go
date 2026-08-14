package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/go-akka/configuration"
)

// DatabaseConfig mirrors the `database { ... }` block of the HOCON file.
type DatabaseConfig struct {
	URL                string
	Username           string
	Password           string
	AuthenticationType string
}

// Config is the root of the configuration file.
type Config struct {
	Database DatabaseConfig
}

// LoadConfig reads and parses a HOCON configuration file.
//
// go-akka/configuration panics rather than returning errors on malformed
// input, so the parse is wrapped in a recover.
func LoadConfig(path string) (cfg *Config, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}

	defer func() {
		if r := recover(); r != nil {
			cfg = nil
			err = fmt.Errorf("parsing config %s: %v", path, r)
		}
	}()

	conf := configuration.ParseString(string(raw))
	if conf == nil {
		return nil, fmt.Errorf("parsing config %s: empty configuration", path)
	}

	cfg = &Config{
		Database: DatabaseConfig{
			URL:                strings.TrimSpace(conf.GetString("database.url", "")),
			Username:           conf.GetString("database.username", ""),
			Password:           conf.GetString("database.password", ""),
			AuthenticationType: strings.TrimSpace(conf.GetString("database.authenticationType", "PASSWORD")),
		},
	}

	// Allow the password to come from the environment so it need not live in
	// the config file on disk.
	if v, ok := os.LookupEnv("ORACLE_PASSWORD"); ok {
		cfg.Database.Password = v
	}

	return cfg, cfg.Validate()
}

// Validate checks that the configuration is usable.
func (c *Config) Validate() error {
	if c.Database.URL == "" {
		return fmt.Errorf("config: database.url is required")
	}

	switch strings.ToUpper(c.Database.AuthenticationType) {
	case "", "PASSWORD":
		// Supported.
	default:
		return fmt.Errorf(
			"config: database.authenticationType %q is not supported (only PASSWORD)",
			c.Database.AuthenticationType)
	}

	if c.Database.Username == "" {
		return fmt.Errorf("config: database.username is required for PASSWORD authentication")
	}

	return nil
}
