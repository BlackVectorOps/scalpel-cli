package config

import (
	"bytes"
	"sync" // FIX: Added the missing 'sync' import.
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGetUninitialized verifies that calling Get() before Load() causes a panic.
func TestGetUninitialized(t *testing.T) {
	// Reset the singleton for a clean test environment.
	instance = nil
	once = sync.Once{}

	assert.Panics(t, func() {
		Get()
	}, "Get() should panic if configuration is not initialized")
}

// TestLoadAndGet verifies the basic singleton load and get functionality.
func TestLoadAndGet(t *testing.T) {
	// Reset singleton
	instance = nil
	once = sync.Once{}
	loadErr = nil

	// A minimal valid YAML config
	yamlConfig := []byte(`
database:
  url: "postgres://test:test@localhost/test"
engine:
  worker_concurrency: 4
browser:
  concurrency: 2
`)

	v := viper.New()
	v.SetConfigType("yaml")
	err := v.ReadConfig(bytes.NewBuffer(yamlConfig))
	require.NoError(t, err)

	err = Load(v)
	require.NoError(t, err)

	cfg := Get()
	require.NotNil(t, cfg)
	assert.Equal(t, "postgres://test:test@localhost/test", cfg.Database.URL)
	assert.Equal(t, 4, cfg.Engine.WorkerConcurrency)
	assert.Equal(t, 2, cfg.Browser.Concurrency)

	// Verify that subsequent calls to Load do not change the instance
	v2 := viper.New()
	v2.SetConfigType("yaml")
	_ = v2.ReadConfig(bytes.NewBuffer([]byte(`database: {url: "new_url"}`)))
	err = Load(v2)
	require.NoError(t, err)

	cfg2 := Get()
	assert.Same(t, cfg, cfg2, "Get() should return the same instance")
	assert.Equal(t, "postgres://test:test@localhost/test", cfg2.Database.URL, "Configuration should not be reloaded")
}

// TestConfigValidation verifies the Validate() method.
func TestConfigValidation(t *testing.T) {
	testCases := []struct {
		name        string
		config      Config
		expectError bool
		errorMsg    string
	}{
		{
			name: "valid config",
			config: Config{
				Database: DatabaseConfig{URL: "valid_url"},
				Engine:   EngineConfig{WorkerConcurrency: 1},
				Browser:  BrowserConfig{Concurrency: 1},
			},
			expectError: false,
		},
		{
			name:        "missing database url",
			config:      Config{},
			expectError: true,
			errorMsg:    "database.url is a required configuration field",
		},
		{
			name: "zero worker concurrency",
			config: Config{
				Database: DatabaseConfig{URL: "valid_url"},
				Engine:   EngineConfig{WorkerConcurrency: 0},
			},
			expectError: true,
			errorMsg:    "engine.worker_concurrency must be a positive integer",
		},
		{
			name: "zero browser concurrency",
			config: Config{
				Database: DatabaseConfig{URL: "valid_url"},
				Engine:   EngineConfig{WorkerConcurrency: 1},
				Browser:  BrowserConfig{Concurrency: 0},
			},
			expectError: true,
			errorMsg:    "browser.concurrency must be a positive integer",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.config.Validate()
			if tc.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tc.errorMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestConfigStructureMapping verifies that the YAML tags correctly map to the struct fields.
func TestConfigStructureMapping(t *testing.T) {
	// This YAML uses snake_case, matching our updated config struct tags.
	yamlInput := `
logger:
  level: debug
  format: console
  log_file: /var/log/app.log
network:
  timeout: 5s
  navigation_timeout: 45s
  capture_response_bodies: true
  post_load_wait: 1s
  proxy:
    enabled: true
    address: "127.0.0.1:8080"
iast:
  enabled: true
  shim_path: "/path/to/shim.js"
  config_path: "/path/to/config.json"
scanners:
  active:
    auth:
      ato:
        enabled: true
        success_keywords:
          - welcome
          - dashboard
        concurrency: 3
        credential_file: "creds.txt"
      idor:
        enabled: true
        test_strategies:
          NumericID: ["increment"]
`
	v := viper.New()
	v.SetConfigType("yaml")
	err := v.ReadConfig(bytes.NewBufferString(yamlInput))
	require.NoError(t, err, "Viper should read the YAML without error")

	var cfg Config
	err = v.Unmarshal(&cfg)
	require.NoError(t, err, "Unmarshaling into Config struct should not produce an error")

	// Assertions to verify correct mapping
	assert.Equal(t, "debug", cfg.Logger.Level)
	assert.Equal(t, "/var/log/app.log", cfg.Logger.LogFile)
	assert.Equal(t, 5*time.Second, cfg.Network.Timeout)
	assert.Equal(t, 45*time.Second, cfg.Network.NavigationTimeout)
	assert.True(t, cfg.Network.CaptureResponseBodies)
	assert.Equal(t, 1*time.Second, cfg.Network.PostLoadWait)
	assert.True(t, cfg.Network.Proxy.Enabled)
	assert.True(t, cfg.IAST.Enabled)
	assert.Equal(t, "/path/to/shim.js", cfg.IAST.ShimPath)
	assert.Equal(t, "/path/to/config.json", cfg.IAST.ConfigPath)
	assert.True(t, cfg.Scanners.Active.Auth.ATO.Enabled)
	assert.Contains(t, cfg.Scanners.Active.Auth.ATO.SuccessKeywords, "welcome")
	assert.Equal(t, "creds.txt", cfg.Scanners.Active.Auth.ATO.CredentialFile)
	assert.Equal(t, 3, cfg.Scanners.Active.Auth.ATO.Concurrency)
	require.NotNil(t, cfg.Scanners.Active.Auth.IDOR.TestStrategies)
	assert.Contains(t, cfg.Scanners.Active.Auth.IDOR.TestStrategies["NumericID"], "increment")
}

// TestSet ensures that the Set function correctly sets the global instance.
func TestSet(t *testing.T) {
	// Reset singleton
	instance = nil
	once = sync.Once{}

	expectedCfg := &Config{
		Database: DatabaseConfig{URL: "set-from-test"},
	}

	Set(expectedCfg)

	actualCfg := Get()

	assert.Same(t, expectedCfg, actualCfg, "Get should return the exact instance that was Set")
	assert.Equal(t, "set-from-test", actualCfg.Database.URL)
}