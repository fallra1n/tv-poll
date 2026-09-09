package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fallra1n/tvpoll/internal/config"
)

func TestFrontendRoutes(t *testing.T) {
	dir := t.TempDir()
	mustWriteTestFile(t, filepath.Join(dir, "index.html"), "public app")
	mustWriteTestFile(t, filepath.Join(dir, "admin", "index.html"), "admin app")
	mustWriteTestFile(t, filepath.Join(dir, "assets", "app-abc123.js"), "console.log('app')")

	handler := New(Deps{
		Config: config.Config{
			AdminToken:     "test-admin-token",
			FrontendDir:    dir,
			RateLimitRPS:   1,
			RateLimitBurst: 1,
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	tests := []struct {
		name         string
		path         string
		status       int
		body         string
		cacheControl string
		location     string
	}{
		{name: "root redirects to admin", path: "/", status: http.StatusTemporaryRedirect, cacheControl: "no-store", location: "/admin"},
		{name: "public deep link", path: "/polls/8c1fcf95-df55-477f-a1f7-63e44fdcc807", status: http.StatusOK, body: "public app", cacheControl: "no-cache"},
		{name: "admin root", path: "/admin", status: http.StatusOK, body: "admin app", cacheControl: "no-cache"},
		{name: "admin deep link", path: "/admin/polls/8c1fcf95-df55-477f-a1f7-63e44fdcc807", status: http.StatusOK, body: "admin app", cacheControl: "no-cache"},
		{name: "hashed asset", path: "/assets/app-abc123.js", status: http.StatusOK, body: "console.log('app')", cacheControl: "public, max-age=31536000, immutable"},
		{name: "unknown API is not SPA fallback", path: "/v1/not-an-endpoint", status: http.StatusNotFound, body: "404 page not found"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, req)

			if res.Code != tt.status {
				t.Fatalf("status = %d, want %d", res.Code, tt.status)
			}
			if tt.body != "" && !strings.Contains(res.Body.String(), tt.body) {
				t.Fatalf("body %q does not contain %q", res.Body.String(), tt.body)
			}
			if got := res.Header().Get("Cache-Control"); got != tt.cacheControl {
				t.Fatalf("Cache-Control = %q, want %q", got, tt.cacheControl)
			}
			if got := res.Header().Get("Location"); got != tt.location {
				t.Fatalf("Location = %q, want %q", got, tt.location)
			}
		})
	}
}

func mustWriteTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
