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

// ServerErrorRetryConfig opts a recipient's server errors into the ordinary
// retry backoff instead of the upstream permanent classification.
type ServerErrorRetryConfig struct {
	Enabled bool `mapstructure:"enabled"`
}

type Config struct {
	upstream.Config  `mapstructure:",squash"`
	AuthRetry        AuthRetryConfig        `mapstructure:"retry_on_auth_failure"`
	ServerErrorRetry ServerErrorRetryConfig `mapstructure:"retry_on_server_error"`
	// StorageCompression compresses the items the persistent queue stores. A
	// sibling of `sending_queue` because that structure is upstream's.
	StorageCompression StorageCompression `mapstructure:"storage_compression"`
}

func (config *Config) Validate() error {
	if err := config.Config.Validate(); err != nil {
		return err
	}
	if err := validateStorageCompression(config.StorageCompression, config.QueueConfig); err != nil {
		return err
	}
	if config.ServerErrorRetry.Enabled && !config.RetryConfig.Enabled {
		return errors.New(`"retry_on_server_error" requires "retry_on_failure.enabled"`)
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
