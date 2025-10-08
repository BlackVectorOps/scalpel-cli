// File: cmd/root.go
package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/xkilldash9x/scalpel-cli/internal/config"
	"github.com/xkilldash9x/scalpel-cli/internal/observability"
)

var (
	cfgFile     string
	validateFix bool // Flag for validation runs during self-healing
)

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:               "scalpel-cli",
	Short:             "Scalpel is an AI-native security scanner.",
	Version:           Version,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		// ... (PersistentPreRunE logic remains the same) ...
		return nil
	},
}

		// 1. Initialize configuration loading (Viper)
		if err := initializeConfig(); err != nil {
			// Initialize a basic logger if config loading fails early.
			basicLogger, _ := zap.NewDevelopment()
			basicLogger.Error("Failed to initialize configuration", zap.Error(err))
			return fmt.Errorf("failed to initialize configuration: %w", err)
		}

		// 2. Unmarshal the configuration
		var cfg config.Config
		if err := viper.Unmarshal(&cfg); err != nil {
			observability.InitializeLogger(config.LoggerConfig{Level: "info", Format: "console", ServiceName: "scalpel-cli"})
			return fmt.Errorf("failed to unmarshal config: %w", err)
		}

		// 3. Validate the configuration
		if err := cfg.Validate(); err != nil {
			observability.InitializeLogger(cfg.Logger)
			return fmt.Errorf("invalid configuration: %w", err)
		}

		// 4. Store the configuration globally
		config.Set(&cfg)

		// 5. Initialize the logger
		observability.InitializeLogger(cfg.Logger)
		logger := observability.GetLogger()
		logger.Info("Starting Scalpel-CLI", zap.String("version", Version))

		return nil
	},
}

// Execute adds all child commands to the root command and sets flags appropriately.
// It accepts a context passed from main.go for graceful shutdown.
func Execute(ctx context.Context) error {
	// Add subcommands
	rootCmd.AddCommand(newScanCmd())
	rootCmd.AddCommand(newReportCmd())
	rootCmd.AddCommand(newSelfHealCmd()) // Register the self-heal command
	rootCmd.AddCommand(newEvolveCmd())   // Register the evolve command

	// Execute the root command with the provided context
	if err := rootCmd.ExecuteContext(ctx); err != nil {
		// Handle execution errors gracefully.
		if logger := observability.GetLogger(); logger != nil && logger != zap.NewNop() {
			// Avoid logging context.Canceled errors as failures, as they are expected
			// during graceful shutdown.
			if ctx.Err() == nil {
				logger.Error("Command execution failed", zap.Error(err))
			}
		} else {
			// Fallback if logger isn't initialized yet.
			fmt.Fprintln(os.Stderr, "Error:", err)
		}
		return err
	}
	return nil
}

func init() {
	cobra.OnInitialize(initializeConfig)

	rootCmd.PersistentFlags().StringVarP(&cfgFile, "config", "c", "", "config file (default is ./config.yaml)")
	rootCmd.PersistentFlags().BoolVar(&validateFix, "validate-fix", false, "Internal flag for self-healing validation.")
	rootCmd.PersistentFlags().MarkHidden("validate-fix") // Hide from users

	// CORRECTED: Add the command variables directly instead of calling constructor functions.
	rootCmd.AddCommand(selfHealCmd)
	rootCmd.AddCommand(evolveCmd)
	rootCmd.AddCommand(scanCmd)
	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(reportCmd)
}


// initializeConfig reads in config file and ENV variables if set.
func initializeConfig() error {
	// Set default values so the app can run with a minimal config.
	config.SetDefaults(viper.GetViper())

	// 1. Set up config file search paths
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	} else {
		viper.AddConfigPath(".")
		viper.SetConfigName("config")
		viper.SetConfigType("yaml")
	}

	// 2. Environment Variable Configuration
	viper.SetEnvPrefix("SCALPEL")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	viper.AutomaticEnv()

	// Explicitly bind critical environment variables.
	// This ensures they are picked up correctly.

	// Database connection string
	_ = viper.BindEnv("database.url", "SCALPEL_DATABASE_URL")

	// Gemini API Key.
	// We bind both a convenient short name and the structured name.
	_ = viper.BindEnv("agent.llm.gemini_api_key", "SCALPEL_GEMINI_API_KEY", "SCALPEL_AGENT_LLM_GEMINI_API_KEY")

	// 3. Read the configuration file
	if err := viper.ReadInConfig(); err != nil {
		// It's okay if the config file is not found, but report other errors
		// like parsing issues.
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return fmt.Errorf("error reading config file: %w", err)
		}
		// Config file not found, proceed with defaults and environment variables.
	}
	return nil
}
