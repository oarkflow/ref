package main

import (
	"errors"

	"github.com/oarkflow/fh"
	"github.com/oarkflow/zlog"
)

func defaultErrorHandler(logger *zlog.Logger) fh.ErrorHandler {
	return func(c fh.Ctx, err error) {
		status, code, message := 500, "INTERNAL_ERROR", "internal server error"
		var httpErr *fh.HTTPError
		if errors.As(err, &httpErr) {
			status, code, message = httpErr.Status, httpErr.Code, httpErr.Message
		}
		if status >= 500 {
			logger.Error("request error", zlog.String("method", c.Method()), zlog.String("path", c.Path()), zlog.Err(err))
		}
		_ = c.Status(status).JSON(map[string]any{
			"success": false,
			"code":    code,
			"message": message,
			"error":   err.Error(),
		})
	}
}
