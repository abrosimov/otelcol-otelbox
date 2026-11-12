package spandedup

import (
	"errors"
	"time"

	"go.opentelemetry.io/collector/component"
)

type Config struct {
	// Storage names the storage extension that persists the index. Without it the index lives in memory
	// alone and starts empty after a restart.
	Storage    component.ID  `mapstructure:"storage"`
	TTL        time.Duration `mapstructure:"ttl"`
	MaxEntries int           `mapstructure:"max_entries"`
}

func (config *Config) Validate() error {
	var errs []error
	if config.TTL <= 0 {
		errs = append(errs, errors.New(`"ttl" must be positive`))
	}
	if config.MaxEntries <= 0 {
		errs = append(errs, errors.New(`"max_entries" must be positive`))
	}
	return errors.Join(errs...)
}
