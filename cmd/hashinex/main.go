package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"hashinex/internal/config"
)

func main() {
	var path string
	var check bool
	var localSetup bool
	flag.StringVar(&path, "config", "", "YAML configuration (default: config/config.yaml next to the executable)")
	flag.BoolVar(&check, "check", false, "validate configuration and exit")
	flag.BoolVar(&localSetup, "admin-setup", false, "enable credential setup from the loopback WebUI")
	flag.Parse()
	if path == "" {
		var err error
		path, err = config.ExecutableConfig(config.GatewayFilename)
		if err != nil {
			slog.Error("could not locate default configuration", "error", err)
			os.Exit(1)
		}
	}
	cfg, err := config.Load(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintln(os.Stderr, "Copy config.example.yaml to the configuration path before starting.")
		}
		slog.Error("configuration error", "error", err)
		os.Exit(1)
	}
	if check {
		fmt.Println("configuration valid")
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, cfg, path, localSetup); err != nil {
		slog.Error("gateway stopped", "error", err)
		os.Exit(1)
	}
}
