// browser/session/logger_adapter.go
package session

import (
	"go.uber.org/zap"
)

// ZapAdapter wraps a *zap.Logger to satisfy the dom.Logger and network.Logger interfaces.
// It utilizes zap.SugaredLogger for flexible argument handling (e.g., key-value pairs).
type ZapAdapter struct {
	logger *zap.SugaredLogger
}

func NewZapAdapter(logger *zap.Logger) *ZapAdapter {
	return &ZapAdapter{logger: logger.Sugar()}
}

func (z *ZapAdapter) Error(msg string, args ...interface{}) {
    if len(args) > 0 {
        // Use Errorw for structured arguments.
	    z.logger.Errorw(msg, args...)
    } else {
        z.logger.Error(msg)
    }
}

func (z *ZapAdapter) Warn(msg string, args ...interface{}) {
	if len(args) > 0 {
	    z.logger.Warnw(msg, args...)
    } else {
        z.logger.Warn(msg)
    }
}

func (z *ZapAdapter) Info(msg string, args ...interface{}) {
	if len(args) > 0 {
	    z.logger.Infow(msg, args...)
    } else {
        z.logger.Info(msg)
    }
}

func (z *ZapAdapter) Debug(msg string, args ...interface{}) {
	if len(args) > 0 {
	    z.logger.Debugw(msg, args...)
    } else {
        z.logger.Debug(msg)
    }
}
