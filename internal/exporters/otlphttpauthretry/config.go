package otlphttpauthretry

import (
	"errors"
	"time"

	upstream "go.opentelemetry.io/collector/exporter/otlphttpexporter"
)

type AuthRetryConfig struct {
	Enabled  bool          `mapstructure:"enabled"`
	Interval time.Duration `mapstructure:"interval"`
}

type Config struct {
	upstream.Config `mapstructure:",squash"`
	AuthRetry       AuthRetryConfig `mapstructure:"retry_on_auth_failure"`
}

func (config *Config) Validate() error {
	if err := config.Config.Validate(); err != nil {
		return err
	}
	if !config.AuthRetry.Enabled {
		return nil
	}
	if !config.RetryConfig.Enabled {
		return errors.New(`"retry_on_auth_failure" requires "retry_on_failure.enabled"`)
	}
	if config.AuthRetry.Interval <= 0 {
		return errors.New(`"retry_on_auth_failure.interval" must be positive`)
	}
	return nil
}
