package main

import (
	"time"

	"github.com/oarkflow/fh"
	"github.com/oarkflow/zlog"
)

// newLogger builds the one structured logger every part of this process
// writes through: HTTP access logs (httpAccessLog below), the DAG execution
// observer wired in main via zlog.NewSlogHandler, and anything else that
// wants zlog.Logger directly. Production gets JSON output at info level or
// above; anything else gets a readable console logger.
func newLogger(env, level string) *zlog.Logger {
	if env == "production" {
		return zlog.NewProductionLogger("starter", env)
	}
	logger := zlog.NewDevelopment()
	if lvl, ok := parseLevel(level); ok {
		logger.SetLevel(lvl)
	}
	return logger
}

func parseLevel(level string) (zlog.Level, bool) {
	switch level {
	case "debug":
		return zlog.DebugLevel, true
	case "info":
		return zlog.InfoLevel, true
	case "warn", "warning":
		return zlog.WarnLevel, true
	case "error":
		return zlog.ErrorLevel, true
	default:
		return 0, false
	}
}

// httpAccessLog logs one structured line per request: method, path, status,
// duration and any error the route returned.
func httpAccessLog(logger *zlog.Logger) fh.Handler {
	return func(c fh.Ctx) error {
		start := time.Now()
		err := c.Next()
		attrs := []zlog.Attr{
			zlog.String("method", c.Method()),
			zlog.String("path", c.Path()),
			zlog.Int("status", c.StatusCode()),
			zlog.Duration("duration", time.Since(start)),
			zlog.String("ip", c.IP()),
		}
		switch {
		case err != nil:
			logger.Error("http request failed", append(attrs, zlog.Err(err))...)
		case c.StatusCode() >= 500:
			logger.Error("http server error", attrs...)
		case c.StatusCode() >= 400:
			logger.Warn("http client error", attrs...)
		default:
			logger.Info("http request", attrs...)
		}
		return err
	}
}
