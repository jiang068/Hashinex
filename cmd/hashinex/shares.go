package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"hashinex/internal/tempfiles"
)

type shareHost struct {
	listener net.Listener
	server   *http.Server
	once     sync.Once
	handler  atomic.Pointer[tempfiles.Service]
}

func (r *runtime) prepareShares(ctx context.Context, active *runtime) error {
	cfg := &r.cfg.TempFiles
	if !cfg.Enabled {
		return nil
	}
	if active != nil && active.shareHost != nil && cfg.Listen == active.cfg.TempFiles.Listen {
		r.shareHost = active.shareHost
	} else {
		listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.Listen)
		if err != nil {
			return fmt.Errorf("无法启动临时分享端口。%w", err)
		}
		host := &shareHost{listener: listener}
		host.server = &http.Server{
			Handler: host, ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout: 10 * time.Second, WriteTimeout: 2 * time.Minute,
			IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8 << 10,
		}
		r.shareHost = host
	}
	directory, err := filepath.Abs(cfg.Directory)
	if err != nil {
		return fmt.Errorf("无法解析临时分享目录。%w", err)
	}
	cfg.Directory = directory
	if active != nil && active.shareStore != nil && directory == active.cfg.TempFiles.Directory {
		r.shareStore = active.shareStore
	} else {
		r.shareStore, err = tempfiles.Open(directory)
		if err != nil {
			return fmt.Errorf("无法打开临时分享目录。%w", err)
		}
	}
	r.shares = tempfiles.NewService(r.shareStore, cfg.PublicURL, cfg.TTL, cfg.MaxBytes)
	return nil
}

func (r *runtime) activateShares() {
	if r.shareHost == nil {
		return
	}
	host := r.shareHost
	host.handler.Store(r.shares)
	host.once.Do(func() { go func() { _ = host.server.Serve(host.listener) }() })
}

func (h *shareHost) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if service := h.handler.Load(); service != nil {
		service.ServeHTTP(w, req)
		return
	}
	http.NotFound(w, req)
}

func (h *shareHost) close() {
	_ = h.server.Close()
	_ = h.listener.Close()
}
