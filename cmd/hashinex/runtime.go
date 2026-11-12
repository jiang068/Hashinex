package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"hashinex/internal/config"
	"hashinex/internal/gateway"
	"hashinex/internal/logging"
	"hashinex/internal/store"
)

type runtime struct {
	cfg          *config.Config
	listener     net.Listener
	db           *store.Store
	output       *logging.Output
	restartError string
}

func run(ctx context.Context, cfg *config.Config, path string, localSetup bool) error {
	active, err := prepareRuntime(ctx, cfg, nil)
	if err != nil {
		return err
	}
	defer func() { active.closeExcept(nil) }()
	for {
		next, err := active.serve(ctx, path, localSetup)
		if next == nil {
			return err
		}
		if ctx.Err() != nil {
			next.closeExcept(active)
			return nil
		}
		if next.listener == nil {
			next.listener, err = net.Listen("tcp", next.cfg.Listen)
			if err != nil {
				next.closeExcept(active)
				active.restartError = fmt.Sprintf("新监听地址无法启用，已恢复原网关。%v", err)
				active.listener, err = net.Listen("tcp", active.cfg.Listen)
				if err != nil {
					return fmt.Errorf("restore previous listener: %w", err)
				}
				continue
			}
		}
		active.closeExcept(next)
		active = next
	}
}

func prepareRuntime(ctx context.Context, cfg *config.Config, active *runtime) (*runtime, error) {
	next := &runtime{cfg: cfg}
	err := next.prepare(ctx, active)
	if err != nil {
		next.closeExcept(active)
		return nil, err
	}
	return next, nil
}

func (r *runtime) prepare(ctx context.Context, active *runtime) error {
	var err error
	if active == nil || r.cfg.Listen != active.cfg.Listen {
		r.listener, err = (&net.ListenConfig{}).Listen(ctx, "tcp", r.cfg.Listen)
		if err != nil {
			var dns *net.DNSError
			_, nextPort, _ := net.SplitHostPort(r.cfg.Listen)
			if active == nil || errors.As(err, &dns) {
				return fmt.Errorf("open listener: %w", err)
			}
			_, activePort, _ := net.SplitHostPort(active.cfg.Listen)
			if nextPort != activePort {
				return fmt.Errorf("open listener: %w", err)
			}
			// The old listener can conflict with a new bind on the same port.
			// Try again after releasing it, with the old configuration as fallback.
		}
	}
	if active != nil && r.cfg.Storage.SQLite == active.cfg.Storage.SQLite {
		r.db = active.db
	} else if r.cfg.Storage.SQLite != "" {
		initCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		r.db, err = store.Open(initCtx, r.cfg.Storage.SQLite)
		cancel()
		if err != nil {
			return fmt.Errorf("open gateway database: %w", err)
		}
	}
	if active != nil && r.cfg.Logging.File == active.cfg.Logging.File {
		r.output = active.output
	} else {
		r.output, err = logging.Open(os.Stderr, r.cfg.Logging)
		if err != nil {
			return fmt.Errorf("open gateway logs: %w", err)
		}
	}
	return ctx.Err()
}

func (r *runtime) closeExcept(next *runtime) {
	if r.listener != nil {
		_ = r.listener.Close()
	}
	if r.db != nil && (next == nil || r.db != next.db) {
		_ = r.db.Close()
	}
	if r.output != nil && (next == nil || r.output != next.output) {
		_ = r.output.Close()
	}
}

func (r *runtime) serve(ctx context.Context, path string, localSetup bool) (*runtime, error) {
	var level slog.Level
	_ = level.UnmarshalText([]byte(r.cfg.LogLevel))
	r.output.SetLimits(r.cfg.Logging.MaxSize, r.cfg.Logging.Keep)
	log := slog.New(slog.NewJSONHandler(r.output, &slog.HandlerOptions{Level: level}))
	g := gateway.New(context.WithoutCancel(ctx), r.cfg, r.db, log)
	g.ConfigureAdmin(r.cfg.Admin, path, r.output.Recent)
	if localSetup {
		g.ConfigureLocalSetup(path, r.output.Recent)
	}
	restarts := make(chan *runtime, 1)
	g.ConfigureRestart(func(request context.Context) (gateway.RestartInfo, error) {
		cfg, err := config.Load(path)
		if err != nil {
			return gateway.RestartInfo{}, fmt.Errorf("读取新配置失败。%w", err)
		}
		next, err := prepareRuntime(request, cfg, r)
		if err != nil {
			return gateway.RestartInfo{}, fmt.Errorf("无法应用新配置。%w", err)
		}
		select {
		case <-ctx.Done():
			next.closeExcept(r)
			return gateway.RestartInfo{}, ctx.Err()
		case restarts <- next:
			return gateway.RestartInfo{Listen: cfg.Listen, ListenChanged: cfg.Listen != r.cfg.Listen}, nil
		}
	}, r.restartError)
	server := &http.Server{
		Handler: g.Handler(), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 15 * time.Second, WriteTimeout: 10 * time.Second,
		IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10,
	}
	errs := make(chan error, 1)
	go func() { errs <- server.Serve(r.listener) }()
	log.Info("gateway listening")
	var next *runtime
	var serveErr error
	select {
	case serveErr = <-errs:
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
	case <-ctx.Done():
	case next = <-restarts:
		log.Info("gateway restart requested")
	}
	drain, cancelDrain := context.WithTimeout(context.Background(), 2*time.Second)
	g.Drain(drain)
	cancelDrain()
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err := server.Shutdown(shutdown)
	cancel()
	if err != nil {
		_ = server.Close()
		log.Warn("gateway shutdown deadline reached", "reason", err)
	}
	g.Close()
	if next == nil {
		select {
		case queued := <-restarts:
			queued.closeExcept(r)
		default:
		}
		return nil, errors.Join(serveErr, err)
	}
	return next, nil
}
