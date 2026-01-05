// File: pkg/observability/logger.go
package observability

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

// -- Moved from internal/config to make them accessible --

// LoggerConfig defines all settings related to logging.
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

// ColorConfig specifies the terminal color codes for different log levels.
type ColorConfig struct {
	Debug  string `mapstructure:"debug" yaml:"debug"`
	Info   string `mapstructure:"info" yaml:"info"`
	Warn   string `mapstructure:"warn" yaml:"warn"`
	Error  string `mapstructure:"error" yaml:"error"`
	DPanic string `mapstructure:"dpanic" yaml:"dpanic"`
	Panic  string `mapstructure:"panic" yaml:"panic"`
	Fatal  string `mapstructure:"fatal" yaml:"fatal"`
}

// -- End of Config Types --

var (
	// globalLogger stores the global logger instance safely across goroutines.
	globalLogger atomic.Pointer[zap.Logger]
	// once ensures that initialization happens exactly once.
	oncePtr atomic.Pointer[sync.Once]
)

// ANSI color codes for the terminal.
const (
	colorBlack   = "\x1b[30m"
	colorRed     = "\x1b[31m"
	colorGreen   = "\x1b[32m"
	colorYellow  = "\x1b[33m"
	colorBlue    = "\x1b[34m"
	colorMagenta = "\x1b[35m"
	colorCyan    = "\x1b[36m"
	colorWhite   = "\x1b[37m"
	colorReset   = "\x1b[0m"
)

// colorMap translates friendly names to ANSI codes.
var colorMap = map[string]string{
	"black":   colorBlack,
	"red":     colorRed,
	"green":   colorGreen,
	"yellow":  colorYellow,
	"blue":    colorBlue,
	"magenta": colorMagenta,
	"cyan":    colorCyan,
	"white":   colorWhite,
}

func init() {
	oncePtr.Store(new(sync.Once))
}

// Initialize sets up the global Zap logger based on configuration and a specified output writer.
func Initialize(cfg LoggerConfig, consoleWriter zapcore.WriteSyncer) {
	// Load the current 'Once' instance
	o := oncePtr.Load()
	o.Do(func() {
		level := zap.NewAtomicLevel()
		if err := level.UnmarshalText([]byte(cfg.Level)); err != nil {
			level.SetLevel(zap.InfoLevel)
		}

		consoleEncoder := getEncoder(cfg)
		consoleCore := zapcore.NewCore(consoleEncoder, consoleWriter, level)
		cores := []zapcore.Core{consoleCore}

		if cfg.LogFile != "" {
			// File encoder is always JSON for structured logging.
			fileEncoder := getEncoder(LoggerConfig{Format: "json"})
			// lumberjack handles file rotation and thread-safe writes.
			fileWriter := zapcore.AddSync(&lumberjack.Logger{
				Filename:   cfg.LogFile,
				MaxSize:    cfg.MaxSize,
				MaxBackups: cfg.MaxBackups,
				MaxAge:     cfg.MaxAge,
				Compress:   cfg.Compress,
			})
			fileCore := zapcore.NewCore(fileEncoder, fileWriter, level)
			cores = append(cores, fileCore)
		}

		core := zapcore.NewTee(cores...)
		options := []zap.Option{zap.AddStacktrace(zap.ErrorLevel)}
		if cfg.AddSource {
			options = append(options, zap.AddCaller())
		}

		logger := zap.New(core, options...).Named(cfg.ServiceName)
		globalLogger.Store(logger)

		zap.ReplaceGlobals(logger)
		zap.RedirectStdLog(logger)
	})
}

// InitializeLogger is a convenience wrapper around Initialize for production use.
func InitializeLogger(cfg LoggerConfig) {
	Initialize(cfg, zapcore.Lock(os.Stdout))
}

// ResetForTest resets the sync.Once and clears the global logger.
func ResetForTest() {
	globalLogger.Store(nil)
	oncePtr.Store(new(sync.Once))
}

// SetLogger is a test helper that forcibly replaces the global logger instance.
func SetLogger(logger *zap.Logger) {
	globalLogger.Store(logger)
	zap.ReplaceGlobals(logger)
}

func newColorizedLevelEncoder(colors ColorConfig) zapcore.LevelEncoder {
	return func(level zapcore.Level, enc zapcore.PrimitiveArrayEncoder) {
		var color string
		levelStr := strings.ToUpper(level.String())

		switch level {
		case zapcore.DebugLevel:
			color = colorMap[colors.Debug]
		case zapcore.InfoLevel:
			color = colorMap[colors.Info]
		case zapcore.WarnLevel:
			color = colorMap[colors.Warn]
		case zapcore.ErrorLevel:
			color = colorMap[colors.Error]
		case zapcore.DPanicLevel:
			color = colorMap[colors.DPanic]
		case zapcore.PanicLevel:
			color = colorMap[colors.Panic]
		case zapcore.FatalLevel:
			color = colorMap[colors.Fatal]
		default:
			color = colorReset
		}

		if color != "" {
			enc.AppendString(fmt.Sprintf("%s%s%s", color, levelStr, colorReset))
		} else {
			enc.AppendString(levelStr)
		}
	}
}

func getEncoder(cfg LoggerConfig) zapcore.Encoder {
	encoderConfig := zap.NewProductionEncoderConfig()
	encoderConfig.EncodeTime = zapcore.TimeEncoderOfLayout("2006-01-02T15:04:05.000Z07:00")

	if cfg.Format == "console" {
		encoderConfig.EncodeLevel = newColorizedLevelEncoder(cfg.Colors)
		return newCustomConsoleEncoder(encoderConfig)
	}

	encoderConfig.EncodeLevel = zapcore.CapitalLevelEncoder
	return zapcore.NewJSONEncoder(encoderConfig)
}

func newCustomConsoleEncoder(cfg zapcore.EncoderConfig) zapcore.Encoder {
	consoleCfg := cfg
	consoleCfg.EncodeName = func(loggerName string, enc zapcore.PrimitiveArrayEncoder) {
		enc.AppendString(loggerName + ".")
	}
	return zapcore.NewConsoleEncoder(consoleCfg)
}

// GetLogger returns the initialized global logger instance.
func GetLogger() *zap.Logger {
	logger := globalLogger.Load()
	if logger == nil {
		l, err := zap.NewDevelopment()
		if err != nil {
			return zap.NewNop()
		}
		l.Warn("Global logger requested before initialization; using fallback.")
		return l.Named("fallback")
	}
	return logger
}

// Sync flushes any buffered log entries.
func Sync() {
	logger := globalLogger.Load()
	if logger != nil {
		if err := logger.Sync(); err != nil {
			errMsg := err.Error()
			if !strings.Contains(errMsg, "sync /dev/stdout") &&
				!strings.Contains(errMsg, "invalid argument") &&
				!strings.Contains(errMsg, "operation not supported") {
				fmt.Fprintln(os.Stderr, "Error: failed to sync logger:", err)
			}
		}
	}
}
