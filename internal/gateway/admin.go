package gateway

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"hashinex/internal/config"
	"hashinex/internal/store"
)

//go:embed adminui/*
var adminAssets embed.FS

type admin struct {
	config       config.Admin
	path         string
	logs         func() []string
	mu           sync.Mutex
	modified     bool
	restarting   bool
	restartError string
	restart      func(context.Context) (RestartInfo, error)
}

type RestartInfo struct {
	Listen        string `json:"listen"`
	ListenChanged bool   `json:"listen_changed"`
}

func (g *Gateway) ConfigureAdmin(settings config.Admin, path string, logs func() []string) {
	if settings.Token != "" {
		g.admin = &admin{config: settings, path: path, logs: logs}
	}
}

func (g *Gateway) ConfigureLocalSetup(path string, logs func() []string) {
	if g.admin == nil {
		g.admin = &admin{path: path, logs: logs}
	}
}

func (g *Gateway) ConfigureRestart(restart func(context.Context) (RestartInfo, error), previousError string) {
	if g.admin != nil {
		g.admin.restart = restart
		g.admin.restartError = previousError
		g.admin.modified = previousError != ""
	}
}

func (g *Gateway) registerAdmin(mux *http.ServeMux) {
	if g.admin == nil {
		return
	}
	static, _ := fs.Sub(adminAssets, "adminui")
	files := http.StripPrefix("/admin/", http.FileServerFS(static))
	mux.Handle("GET /admin/", g.admin.protect(false, files.ServeHTTP))
	mux.HandleFunc("GET /admin/api/state", g.admin.protect(true, g.adminState))
	mux.HandleFunc("GET /admin/api/identities", g.admin.protect(true, g.adminIdentities))
	mux.HandleFunc("GET /admin/api/candidates", g.admin.protect(true, g.adminCandidates))
	mux.HandleFunc("POST /admin/api/binding", g.admin.protect(true, g.adminBinding))
	mux.HandleFunc("GET /admin/api/config", g.admin.protect(true, g.adminConfig))
	mux.HandleFunc("PUT /admin/api/config", g.admin.protect(true, g.adminConfig))
	mux.HandleFunc("GET /admin/api/settings", g.admin.protect(true, g.adminConfig))
	mux.HandleFunc("PUT /admin/api/settings", g.admin.protect(true, g.adminConfig))
	if g.admin.restart != nil {
		mux.HandleFunc("POST /admin/api/restart", g.admin.protect(true, g.adminRestart))
	}
}

func (g *Gateway) adminRestart(w http.ResponseWriter, r *http.Request) {
	a := g.admin
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.restarting {
		http.Error(w, "网关正在重启", http.StatusConflict)
		return
	}
	info, err := a.restart(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	a.restarting = true
	a.restartError = ""
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"listen": info.Listen, "listen_changed": info.ListenChanged, "generation": g.generation,
	})
}

func (a *admin) protect(checkToken bool, handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		if !a.config.AllowRemote && !net.ParseIP(host).IsLoopback() {
			http.NotFound(w, r)
			return
		}
		if a.config.Token == "" {
			address, err := url.Parse("http://" + r.Host)
			if err != nil || (address.Hostname() != "localhost" && !net.ParseIP(address.Hostname()).IsLoopback()) {
				http.NotFound(w, r)
				return
			}
			if checkToken && r.Header.Get("X-Hashinex-Admin") != "1" {
				http.Error(w, "local setup requires the admin console", http.StatusUnauthorized)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; base-uri 'none'")
		if checkToken && !authorized(r, a.config.Token) {
			http.Error(w, "invalid admin token", http.StatusUnauthorized)
			return
		}
		handler(w, r)
	}
}

func (g *Gateway) adminState(w http.ResponseWriter, _ *http.Request) {
	type endpoint struct {
		ID               string `json:"id"`
		Protocol         string `json:"protocol,omitempty"`
		Mode             string `json:"mode,omitempty"`
		SelfID           string `json:"self_id"`
		Role             string `json:"role,omitempty"`
		Namespace        string `json:"namespace,omitempty"`
		PrivateNamespace string `json:"private_namespace,omitempty"`
		Input            string `json:"input,omitempty"`
		Online           bool   `json:"online"`
		Writable         bool   `json:"writable"`
		Queued           int    `json:"queued,omitempty"`
	}
	g.mu.Lock()
	inputs := make([]endpoint, 0, len(g.inputs))
	outputs := make([]endpoint, 0, len(g.routes))
	for _, in := range g.inputs {
		entry := endpoint{
			ID: in.cfg.ID, Protocol: in.cfg.Protocol, Mode: in.cfg.Mode, SelfID: in.cfg.SelfID,
			Role: in.cfg.Role, Online: in.peer != nil || in.online.Load(), Queued: len(in.events) + len(in.officialEvents),
		}
		if in.official != nil {
			entry.Namespace, entry.PrivateNamespace = in.namespace(), in.userNamespace("private")
		} else if in.virtualJobs != nil {
			entry.Namespace = in.virtualNamespace()
		}
		inputs = append(inputs, entry)
	}
	for _, r := range g.routes {
		outputs = append(outputs, endpoint{
			ID: r.output.ID, Protocol: r.output.Protocol, Mode: r.output.Mode, SelfID: r.input.cfg.SelfID, Input: r.cfg.From,
			Online: r.peer != nil, Writable: r.cfg.AllowActions,
		})
	}
	g.mu.Unlock()
	sort.Slice(inputs, func(i, j int) bool { return inputs[i].ID < inputs[j].ID })
	sort.Slice(outputs, func(i, j int) bool {
		if outputs[i].ID == outputs[j].ID {
			return outputs[i].Input < outputs[j].Input
		}
		return outputs[i].ID < outputs[j].ID
	})
	g.admin.mu.Lock()
	restartNeeded, restarting, restartError := g.admin.modified, g.admin.restarting, g.admin.restartError
	g.admin.mu.Unlock()
	var logs []string
	if g.admin.logs != nil {
		logs = g.admin.logs()
	}
	writeJSON(w, map[string]any{
		"inputs": inputs, "outputs": outputs, "matching": g.matching.Mode, "restart_needed": restartNeeded, "logs": logs,
		"listen": g.listen, "restart_available": g.admin.restart != nil, "restarting": restarting, "restart_error": restartError,
		"user_threshold": g.matching.Successes, "group_threshold": g.matching.GroupSuccesses,
		"group_sender_threshold": g.matching.GroupSenders, "avatar_distance": g.matching.Distance,
	})
}

func (g *Gateway) adminIdentities(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if query.Get("view") == "entities" && query.Has("size") {
		page, size := adminPage(query)
		result := store.Page[store.Entity]{Rows: []store.Entity{}, Page: 1, Size: size}
		if g.store != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
			defer cancel()
			var err error
			result, err = g.store.EntityPage(ctx, query.Get("q"), query.Get("kind"), page, size)
			if err != nil {
				http.Error(w, "identity query failed", http.StatusServiceUnavailable)
				return
			}
		}
		writeJSON(w, result)
		return
	}
	if g.store == nil {
		writeJSON(w, []any{})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if r.URL.Query().Get("view") == "entities" {
		query := r.URL.Query()
		rows, err := g.store.Entities(ctx, query.Get("q"), query.Get("kind"))
		if err != nil {
			http.Error(w, "identity query failed", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, rows)
		return
	}
	rows, err := g.store.Identities(ctx, r.URL.Query().Get("q"))
	if err != nil {
		http.Error(w, "identity query failed", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, rows)
}

func (g *Gateway) adminCandidates(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if query.Has("size") {
		page, size := adminPage(query)
		result := store.Page[store.Candidate]{Rows: []store.Candidate{}, Page: 1, Size: size}
		if g.store != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
			defer cancel()
			var err error
			result, err = g.store.CandidatePage(ctx, time.Now().Add(-g.matching.EvidenceTTL), query.Get("q"), query.Get("status"), page, size)
			if err != nil {
				http.Error(w, "candidate query failed", http.StatusServiceUnavailable)
				return
			}
		}
		writeJSON(w, result)
		return
	}
	if g.store == nil {
		writeJSON(w, []any{})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	rows, err := g.store.Candidates(ctx, time.Now().Add(-g.matching.EvidenceTTL))
	if err != nil {
		http.Error(w, "candidate query failed", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, rows)
}

func adminPage(query url.Values) (int, int) {
	page, _ := strconv.Atoi(query.Get("page"))
	size, _ := strconv.Atoi(query.Get("size"))
	switch size {
	case 10, 20, 50, 100, 200:
	default:
		size = 20
	}
	return max(1, page), size
}

func (g *Gateway) adminBinding(w http.ResponseWriter, r *http.Request) {
	if g.store == nil {
		http.Error(w, "identity database is disabled", http.StatusConflict)
		return
	}
	var change struct {
		Namespace string `json:"namespace"`
		Kind      string `json:"kind"`
		Native    string `json:"native"`
		RealID    string `json:"real_id"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	d.DisallowUnknownFields()
	if err := d.Decode(&change); err != nil || len(change.Native) > 128 {
		http.Error(w, "invalid binding request", http.StatusBadRequest)
		return
	}
	known := false
	for _, in := range g.inputs {
		if in.official != nil && (in.namespace() == change.Namespace ||
			(change.Kind == "user" && in.userNamespace("private") == change.Namespace)) {
			known = true
			break
		}
		if in.virtualJobs != nil && in.virtualNamespace() == change.Namespace {
			known = true
			break
		}
	}
	id, err := strconv.ParseInt(change.RealID, 10, 64)
	if err != nil || !known {
		http.Error(w, "invalid real ID or unknown input namespace", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	key := store.Identity{Namespace: change.Namespace, Kind: change.Kind, Native: change.Native}
	err = g.store.SetBinding(ctx, key, id, "web-admin")
	switch {
	case errors.Is(err, store.ErrInvalidBinding):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, store.ErrBindingConflict):
		http.Error(w, err.Error(), http.StatusConflict)
	case err != nil:
		http.Error(w, "binding update failed", http.StatusServiceUnavailable)
	default:
		g.log.Info("identity binding edited by administrator", "namespace", change.Namespace, "kind", change.Kind, "revoked", id == 0)
		writeJSON(w, map[string]bool{"ok": true})
	}
}

func (g *Gateway) adminConfig(w http.ResponseWriter, r *http.Request) {
	a := g.admin
	a.mu.Lock()
	defer a.mu.Unlock()
	if r.Method == http.MethodPut && a.restarting {
		http.Error(w, "网关正在重启", http.StatusConflict)
		return
	}
	file, err := os.Open(a.path)
	if err != nil {
		http.Error(w, "configuration file unavailable", http.StatusServiceUnavailable)
		return
	}
	current, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	closeErr := file.Close()
	if err != nil || closeErr != nil || len(current) > 1<<20 {
		http.Error(w, "configuration file unavailable", http.StatusServiceUnavailable)
		return
	}
	tag := fmt.Sprintf(`"%x"`, sha256.Sum256(current))
	structured := r.URL.Path == "/admin/api/settings"
	if r.Method == http.MethodGet {
		w.Header().Set("ETag", tag)
		if structured {
			settings, err := readSettings(current, a.path)
			if err != nil {
				http.Error(w, "could not read configuration fields", http.StatusUnprocessableEntity)
				return
			}
			writeJSON(w, settings)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write(current)
		return
	}
	if r.Header.Get("If-Match") != tag {
		http.Error(w, "configuration changed; reload before editing", http.StatusPreconditionFailed)
		return
	}
	proposed, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, "configuration body too large", http.StatusBadRequest)
		return
	}
	var secrets map[string]string
	if structured {
		proposed, secrets, err = extractCredentialEdits(proposed)
		if err == nil {
			proposed, err = mergeSettings(current, proposed)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if len(proposed) > 1<<20 {
		http.Error(w, "configuration body too large", http.StatusBadRequest)
		return
	}
	secretPath, secretData, err := config.PrepareUpdate(current, proposed, a.path, secrets)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if secretPath != "" {
		if err := os.MkdirAll(filepath.Dir(secretPath), 0700); err != nil || replaceConfig(secretPath, secretData) != nil {
			http.Error(w, "could not persist credentials", http.StatusServiceUnavailable)
			return
		}
	}
	if err := replaceConfig(a.path, proposed); err != nil {
		http.Error(w, "could not persist configuration", http.StatusServiceUnavailable)
		return
	}
	a.modified = true
	a.restartError = ""
	w.Header().Set("ETag", fmt.Sprintf(`"%x"`, sha256.Sum256(proposed)))
	writeJSON(w, map[string]bool{"restart_needed": true})
}

func replaceConfig(path string, data []byte) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".hashinex-*.tmp")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func writeJSON(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(data)
}
