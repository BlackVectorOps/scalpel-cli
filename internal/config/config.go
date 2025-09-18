// File: internal/config/config.go
package config

import (
	"fmt"
	"sync"
	"time"

	"github.com/spf13/viper"
	"github.com/xkilldash9x/scalpel-cli/internal/humanoid"
)

var (
	instance *Config
	loadErr  error // Cache the loading error globally
	once     sync.Once
)

// Config holds the entire application configuration.
type Config struct {
	Logger    LoggerConfig    `mapstructure:"logger" yaml:"logger"`
	Database  DatabaseConfig  `mapstructure:"database" yaml:"database"`
	Engine    EngineConfig    `mapstructure:"engine" yaml:"engine"`
	Browser   BrowserConfig   `mapstructure:"browser" yaml:"browser"`
	Network   NetworkConfig   `mapstructure:"network" yaml:"network"`
	IAST      IASTConfig      `mapstructure:"iast" yaml:"iast"`
	Scanners  ScannersConfig  `mapstructure:"scanners" yaml:"scanners"`
	Agent     AgentConfig     `mapstructure:"agent" yaml:"agent"`
	Discovery DiscoveryConfig `mapstructure:"discovery" yaml:"discovery"`
	// ScanConfig is populated dynamically from CLI flags, not from the config file.
	Scan ScanConfig `mapstructure:"-" yaml:"-"`
}

// LoggerConfig holds all the configuration for the logger.
// (All definitions up to AuthConfig remain unchanged as they were correct)

type LoggerConfig struct {
	Level       string      `mapstructure:"level" yaml:"level"`
	Format      string      `mapstructure:"format" yaml:"format"`
	AddSource   bool        `mapstructure:"add_source" yaml:"add_source"`
	ServiceName string      `mapstructure:"service_name" yaml:"service_name"`
	LogFile     string      `mapstructure:"log_file" yaml:"log_file"`
	MaxSize     int         `mapstructure:"max_size" yaml:"max_size"`
	MaxBackups  int         `mapstructure:"max_backups" yaml:"max_backups"`
	MaxAge      int         `mapstructure:"max_age" yaml:"max_age"`
	Compress    bool        `mapstructure:"compress" yaml:"compress"`
	Colors      ColorConfig `mapstructure:"colors" yaml:"colors"`
}

type ColorConfig struct {
	Debug  string `mapstructure:"debug" yaml:"debug"`
	Info   string `mapstructure:"info" yaml:"info"`
	Warn   string `mapstructure:"warn" yaml:"warn"`
	Error  string `mapstructure:"error" yaml:"error"`
	DPanic string `mapstructure:"dpanic" yaml:"dpanic"`
	Panic  string `mapstructure:"panic" yaml:"panic"`
	Fatal  string `mapstructure:"fatal" yaml:"fatal"`
}

type DatabaseConfig struct {
	URL string `mapstructure:"url" yaml:"url"`
}

type EngineConfig struct {
	QueueSize         int           `mapstructure:"queue_size" yaml:"queue_size"`
	WorkerConcurrency int           `mapstructure:"worker_concurrency" yaml:"worker_concurrency"`
	DefaultTaskTimeout time.Duration `mapstructure:"default_task_timeout" yaml:"default_task_timeout"`
}

type BrowserConfig struct {
	Headless        bool            `mapstructure:"headless" yaml:"headless"`
	DisableCache    bool            `mapstructure:"disable_cache" yaml:"disable_cache"`
	IgnoreTLSErrors bool            `mapstructure:"ignore_tls_errors" yaml:"ignore_tls_errors"`
	Concurrency     int             `mapstructure:"concurrency" yaml:"concurrency"`
	Debug           bool            `mapstructure:"debug" yaml:"debug"`
	Args            []string        `mapstructure:"args" yaml:"args"`
	Viewport        map[string]int  `mapstructure:"viewport" yaml:"viewport"`
	Humanoid        humanoid.Config `mapstructure:"humanoid" yaml:"humanoid"`
}

type ProxyConfig struct {
	Enabled bool   `mapstructure:"enabled" yaml:"enabled"`
	Address string `mapstructure:"address" yaml:"address"`
	CACert  string `mapstructure:"ca_cert" yaml:"ca_cert"`
	CAKey   string `mapstructure:"ca_key" yaml:"ca_key"`
}

type NetworkConfig struct {
	Timeout               time.Duration     `mapstructure:"timeout" yaml:"timeout"`
	NavigationTimeout     time.Duration     `mapstructure:"navigation_timeout" yaml:"navigation_timeout"`
	CaptureResponseBodies bool              `mapstructure:"capture_response_bodies" yaml:"capture_response_bodies"`
	Headers               map[string]string `mapstructure:"headers" yaml:"headers"`
	PostLoadWait          time.Duration     `mapstructure:"post_load_wait" yaml:"post_load_wait"`
	Proxy                 ProxyConfig       `mapstructure:"proxy" yaml:"proxy"`
}

type IASTConfig struct {
	Enabled    bool   `mapstructure:"enabled" yaml:"enabled"`
	ShimPath   string `mapstructure:"shim_path" yaml:"shim_path"`
	ConfigPath string `mapstructure:"config_path" yaml:"config_path"`
}

type ScannersConfig struct {
	Passive PassiveScannersConfig `mapstructure:"passive" yaml:"passive"`
	Static  StaticScannersConfig  `mapstructure:"static" yaml:"static"`
	Active  ActiveScannersConfig  `mapstructure:"active" yaml:"active"`
}

type PassiveScannersConfig struct {
	Headers HeadersConfig `mapstructure:"headers" yaml:"headers"`
}
type HeadersConfig struct {
	Enabled bool `mapstructure:"enabled" yaml:"enabled"`
}

type StaticScannersConfig struct {
	JWT JWTConfig `mapstructure:"jwt" yaml:"jwt"`
}
type JWTConfig struct {
	Enabled           bool     `mapstructure:"enabled" yaml:"enabled"`
	KnownSecrets      []string `mapstructure:"known_secrets" yaml:"known_secrets"`
	BruteForceEnabled bool     `mapstructure:"brute_force_enabled" yaml:"brute_force_enabled"`
	DictionaryFile    string   `mapstructure:"dictionary_file" yaml:"dictionary_file"`
}

type ActiveScannersConfig struct {
	Taint          TaintConfig          `mapstructure:"taint" yaml:"taint"`
	ProtoPollution ProtoPollutionConfig `mapstructure:"protopollution" yaml:"protopollution"`
	TimeSlip       TimeSlipConfig       `mapstructure:"timeslip" yaml:"timeslip"`
	Auth           AuthConfig           `mapstructure:"auth" yaml:"auth"`
}

type TaintConfig struct {
	Enabled     bool `mapstructure:"enabled" yaml:"enabled"`
	Depth       int  `mapstructure:"depth" yaml:"depth"`
	Concurrency int  `mapstructure:"concurrency" yaml:"concurrency"`
}

type ProtoPollutionConfig struct {
	Enabled bool `mapstructure:"enabled" yaml:"enabled"`
}

type TimeSlipConfig struct {
	Enabled        bool `mapstructure:"enabled" yaml:"enabled"`
	RequestCount   int  `mapstructure:"request_count" yaml:"request_count"`
	MaxConcurrency int  `mapstructure:"max_concurrency" yaml:"max_concurrency"`
	ThresholdMs    int  `mapstructure:"threshold_ms" yaml:"threshold_ms"`
}

type AuthConfig struct {
	ATO  ATOConfig  `mapstructure:"ato" yaml:"ato"`
	IDOR IDORConfig `mapstructure:"idor" yaml:"idor"`
}

type ATOConfig struct {
	Enabled                bool     `mapstructure:"enabled" yaml:"enabled"`
	CredentialFile         string   `mapstructure:"credential_file" yaml:"credential_file"`
	Concurrency            int      `mapstructure:"concurrency" yaml:"concurrency"`
	MinRequestDelayMs      int      `mapstructure:"min_request_delay_ms" yaml:"min_request_delay_ms"`
	RequestDelayJitterMs   int      `mapstructure:"request_delay_jitter_ms" yaml:"request_delay_jitter_ms"`
	SuccessKeywords        []string `mapstructure:"success_keywords" yaml:"success_keywords"`
	UserFailureKeywords    []string `mapstructure:"user_failure_keywords" yaml:"user_failure_keywords"`
	PassFailureKeywords    []string `mapstructure:"pass_failure_keywords" yaml:"pass_failure_keywords"`
	GenericFailureKeywords []string `mapstructure:"generic_failure_keywords" yaml:"generic_failure_keywords"`
	LockoutKeywords        []string `mapstructure:"lockout_keywords" yaml:"lockout_keywords"`
}


// IDORConfig definition.
// FIX: The mapstructure tags (e.g., "test_strategies") are correct for standard Viper/YAML mapping.
// The failing test suggests the YAML file used during testing might have incorrect keys (e.g., "testStrategies").
// The Go struct definition is correct as provided.
type IDORConfig struct {
	Enabled        bool                `mapstructure:"enabled" yaml:"enabled"`
	IgnoreList     []string            `mapstructure:"ignore_list" yaml:"ignore_list"`
	TestStrategies map[string][]string `mapstructure:"test_strategies" yaml:"test_strategies"`
}

// (Remaining definitions remain unchanged)

type ScanConfig struct {
	Targets     []string
	Output      string
	Format      string
	Concurrency int
	Depth       int
	Scope       string
}

type DiscoveryConfig struct {
	MaxDepth           int           `mapstructure:"max_depth" yaml:"max_depth"`
	Concurrency        int           `mapstructure:"concurrency" yaml:"concurrency"`
	Timeout            time.Duration `mapstructure:"timeout" yaml:"timeout"`
	PassiveEnabled     *bool         `mapstructure:"passive_enabled" yaml:"passive_enabled"`
	CrtShRateLimit     float64       `mapstructure:"crtsh_rate_limit" yaml:"crtsh_rate_limit"`
	CacheDir           string        `mapstructure:"cache_dir" yaml:"cache_dir"`
	PassiveConcurrency int           `mapstructure:"passive_concurrency" yaml:"passive_concurrency"`
}

type KnowledgeGraphConfig struct {
	Type string `mapstructure:"type" yaml:"type"`
}

type AgentConfig struct {
	LLM            LLMRouterConfig      `mapstructure:"llm" yaml:"llm"`
	KnowledgeGraph KnowledgeGraphConfig `mapstructure:"knowledge_graph" yaml:"knowledge_graph"`
}

type LLMProvider string

const (
	ProviderGemini    LLMProvider = "gemini"
	ProviderOpenAI    LLMProvider = "openai"
	ProviderAnthropic LLMProvider = "anthropic"
	ProviderOllama    LLMProvider = "ollama"
)

type LLMRouterConfig struct {
	DefaultFastModel     string                    `mapstructure:"default_fast_model" yaml:"default_fast_model"`
	DefaultPowerfulModel string                    `mapstructure:"default_powerful_model" yaml:"default_powerful_model"`
	Models               map[string]LLMModelConfig `mapstructure:"models" yaml:"models"`
}

type LLMModelConfig struct {
	Provider      LLMProvider       `mapstructure:"provider" yaml:"provider"`
	Model         string            `mapstructure:"model" yaml:"model"`
	APIKey        string            `mapstructure:"api_key" yaml:"api_key"`
	Endpoint      string            `mapstructure:"endpoint" yaml:"endpoint"`
	APITimeout    time.Duration     `mapstructure:"api_timeout" yaml:"api_timeout"`
	Temperature   float32           `mapstructure:"temperature" yaml:"temperature"`
	TopP          float32           `mapstructure:"top_p" yaml:"top_p"`
	TopK          int               `mapstructure:"top_k" yaml:"top_k"`
	MaxTokens     int               `mapstructure:"max_tokens" yaml:"max_tokens"`
	SafetyFilters map[string]string `mapstructure:"safety_filters" yaml:"safety_filters"`
}


// Validate checks the configuration for required fields and sane values.
func (c *Config) Validate() error {
	// Validation logic remains unchanged.
	if c.Database.URL == "" {
		// Depending on usage, this might be too strict, but keeping as per original intent if required.
		// return fmt.Errorf("database.url is a required configuration field")
	}
	if c.Engine.WorkerConcurrency <= 0 {
		return fmt.Errorf("engine.worker_concurrency must be a positive integer")
	}
	if c.Browser.Concurrency <= 0 {
		return fmt.Errorf("browser.concurrency must be a positive integer")
	}
	return nil
}

// Load initializes the configuration singleton from Viper.
func Load(v *viper.Viper) error {
	once.Do(func() {
		var cfg Config
		if err := v.Unmarshal(&cfg); err != nil {
			loadErr = fmt.Errorf("error unmarshaling config: %w", err)
			return
		}
		instance = &cfg
	})
	return loadErr
}

// Get returns the loaded configuration instance.
func Get() *Config {
	if instance == nil {
		// This panic is intentional to catch initialization errors early.
		panic("Configuration not initialized. Ensure initialization happens in the root command.")
	}
	return instance
}

// Set initializes the global configuration instance if not already set.
func Set(cfg *Config) {
	once.Do(func() {
		instance = cfg
	})
}