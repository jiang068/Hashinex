package gateway

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hashinex/internal/config"
)

func TestAdminRequiresTokenAndProtectsConfigRevisions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixture.yaml")
	initial := "listen: 127.0.0.1:6700\n"
	if err := os.WriteFile(path, []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}
	g := New(t.Context(), testConfig(), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer g.Close()
	const token = "synthetic-admin-token"
	g.ConfigureAdmin(config.Admin{Token: token}, path, nil)
	handler := g.Handler()
	request := func(method, addr, body, auth, tag string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, addr, strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:12345"
		r.Header.Set("Authorization", "Bearer "+auth)
		r.Header.Set("If-Match", tag)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, r)
		return res
	}
	const api = "/admin/api/config"
	if got := request("GET", api, "", "", "").Code; got != http.StatusUnauthorized {
		t.Fatalf("anonymous config request = %d", got)
	}
	if got := request("GET", "/admin/", "", "", "").Code; got != http.StatusOK {
		t.Fatalf("admin assets unavailable: %d", got)
	}
	tag := request("GET", api, "", token, "").Header().Get("ETag")
	if tag == "" {
		t.Fatal("missing config revision")
	}
	if got := request("PUT", api, "unrecognized_field: true", token, tag).Code; got != http.StatusBadRequest {
		t.Fatalf("invalid YAML accepted: %d", got)
	}
	updated := initial + "log_level: debug\n"
	if got := request("PUT", api, updated, token, tag).Code; got != http.StatusOK {
		t.Fatalf("valid YAML not saved: %d", got)
	}
	if got := request("PUT", api, initial, token, tag).Code; got != http.StatusPreconditionFailed {
		t.Fatalf("stale revision accepted: %d", got)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != updated {
		t.Fatal("persisted content does not match valid revision")
	}
}
