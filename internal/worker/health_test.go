package worker

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHealthOKRequiresTwoHundredStatus(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ok.Close()
	if !healthOK(ok.URL, time.Second) {
		t.Fatal("expected 2xx response to be healthy")
	}

	notFound := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer notFound.Close()
	if healthOK(notFound.URL, time.Second) {
		t.Fatal("expected 404 response to be unhealthy")
	}
}

func TestProbeURLRejectsRedirectToDisallowedHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data", http.StatusFound)
	}))
	defer srv.Close()
	if _, err := probeURL(srv.URL, time.Second); err == nil {
		t.Fatal("expected redirect to link-local host to be rejected")
	}
}
