package spandedup

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDefaultConfigRequiresOnlyStorage(t *testing.T) {
	factory := NewFactory()
	config := factory.CreateDefaultConfig().(*Config)

	require.Equal(t, "span_dedup", factory.Type().String())
	require.Equal(t, 72*time.Hour, config.TTL)
	require.Equal(t, 500_000, config.MaxEntries)
	require.ErrorContains(t, config.Validate(), `"storage"`)
	config.Storage = storageID
	require.NoError(t, config.Validate())
}

func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*Config)
		wantError string
	}{
		{name: "zero ttl", configure: func(config *Config) { config.TTL = 0 }, wantError: `"ttl" must be positive`},
		{name: "negative ttl", configure: func(config *Config) { config.TTL = -time.Hour }, wantError: `"ttl" must be positive`},
		{name: "zero max entries", configure: func(config *Config) { config.MaxEntries = 0 }, wantError: `"max_entries" must be positive`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := createDefaultConfig().(*Config)
			config.Storage = storageID
			test.configure(config)

			require.ErrorContains(t, config.Validate(), test.wantError)
		})
	}
}
