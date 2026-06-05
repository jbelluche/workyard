package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/jackbelluche/workyard/internal/registry"
)

type ServerOptions struct {
	Listen          string
	RefreshInterval time.Duration
	StateDir        string
	Socket          string
	Version         string
	Open            bool
	AutoStartDaemon bool
}

type StateProvider interface {
	State() State
}

func Serve(ctx context.Context, opts ServerOptions) error {
	if opts.Listen == "" {
		opts.Listen = "127.0.0.1:3099"
	}
	if err := validateListen(opts.Listen); err != nil {
		return err
	}
	if opts.RefreshInterval <= 0 {
		opts.RefreshInterval = 3 * time.Second
	}
	store := registry.New(registry.DefaultPath(opts.StateDir))
	fetcher := DefaultFetcher{StateDir: opts.StateDir, Socket: opts.Socket, AutoStartDaemon: opts.AutoStartDaemon}
	poller := NewPoller(store, fetcher, opts.Version, opts.RefreshInterval, 20*time.Second)
	pollCtx, cancelPoller := context.WithCancel(ctx)
	defer cancelPoller()
	go poller.Start(pollCtx)

	srv := &http.Server{
		Addr:              opts.Listen,
		Handler:           Handler(poller),
		ReadHeaderTimeout: 5 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		if opts.Open {
			go openWhenReady(ctx, "http://"+opts.Listen+"/")
		}
		err := srv.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()
	select {
	case <-ctx.Done():
		cancelPoller()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		cancelPoller()
		return err
	}
}

func Handler(provider StateProvider) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(dashboardHTML))
	})
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"ok": true, "time": time.Now().UTC()})
	})
	mux.HandleFunc("/api/state", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, provider.State())
	})
	mux.HandleFunc("/api/workers", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"ok": true, "workers": provider.State().Workers})
	})
	mux.HandleFunc("/api/runs", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"ok": true, "runs": provider.State().Runs})
	})
	mux.HandleFunc("/api/services", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"ok": true, "services": provider.State().Services})
	})
	mux.HandleFunc("/api/urls", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"ok": true, "urls": urls(provider.State())})
	})
	mux.HandleFunc("/api/events", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"ok": true, "events": provider.State().Events})
	})
	return securityHeaders(localhostOnly(mux))
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(value)
}

func validateListen(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("workyard ui only listens on loopback addresses; got %s", addr)
	}
	return nil
}

func urls(state State) []URLSnapshot {
	out := []URLSnapshot{}
	for _, svc := range state.Services {
		if svc.URL == "" {
			continue
		}
		out = append(out, URLSnapshot{
			Worker:  svc.Worker,
			Project: svc.Project,
			RunID:   svc.RunID,
			Service: svc.Name,
			URL:     svc.URL,
			Healthy: svc.Healthy,
			Private: true,
			Public:  false,
		})
	}
	return out
}

func localhostOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if !hostAllowed(r.Host) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func hostAllowed(hostHeader string) bool {
	host := strings.TrimSpace(hostHeader)
	if host == "" {
		return true
	}
	if splitHost, _, err := net.SplitHostPort(host); err == nil {
		host = splitHost
	}
	host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; font-src 'self'; base-uri 'none'; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func openWhenReady(ctx context.Context, url string) {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(url, "/")+"/api/health", nil)
		if err == nil {
			res, err := http.DefaultClient.Do(req)
			if err == nil {
				_ = res.Body.Close()
				if res.StatusCode == http.StatusOK {
					_ = exec.Command("open", url).Run()
					return
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(150 * time.Millisecond):
		}
	}
}
