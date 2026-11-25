package main

import (
	"log/slog"
	"os"

	"github.com/Im-Sage/cloudtask-go/internal/app"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout,
		&slog.HandlerOptions{
			Level: slog.LevelInfo,
		},
	))
	if err := app.Run(logger); err != nil {
		logger.Error("服务退出", "error", err)
		os.Exit(1)
	}
}
