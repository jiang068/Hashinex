package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"hashinex/internal/config"
	"hashinex/internal/gscore"
)

func main() {
	path := flag.String("config", "", "adapter configuration (default: config/gscore.yaml next to the executable)")
	flag.Parse()
	if *path == "" {
		var err error
		*path, err = config.ExecutableConfig("gscore.yaml")
		if err != nil {
			slog.Error("could not locate default adapter configuration", "error", err)
			os.Exit(1)
		}
	}
	connections, err := gscore.Load(*path)
	if err != nil {
		slog.Error("could not load adapter configuration", "error", err)
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	gscore.Run(ctx, connections, slog.Default())
}
