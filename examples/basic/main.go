package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/circlexo/circlexo-go"
	"github.com/circlexo/circlexo-go/entitlements"
	"github.com/circlexo/circlexo-go/middleware"
	"github.com/circlexo/circlexo-go/oidc"
	"github.com/circlexo/circlexo-go/usage"
)

func newApp(cfg circlexo.Config, redirect string, key []byte, feature string) (http.Handler, func(context.Context) error, error) {
	if cfg.AppKey == "" {
		return nil, nil, errors.New("CIRCLEXO_APP_KEY is required by this example for hub API calls")
	}
	rp, err := oidc.New(cfg, redirect, key)
	if err != nil {
		return nil, nil, err
	}
	client := circlexo.NewClient(cfg, nil)
	reporter := usage.NewReporter(client, 16, 1)
	auth := middleware.Auth(middleware.Options{
		Verifier: circlexo.NewVerifier(cfg), Session: rp.SessionToken,
		Tenants: &middleware.CachedTenants{Client: client}, Entitlements: entitlements.New(client),
	})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "Sign in at /auth/login?return_to=/profile. POST /use reports one metered use; see README.")
	})
	mux.HandleFunc("GET /auth/login", rp.Login)
	mux.HandleFunc("GET /auth/callback", rp.Callback)
	mux.Handle("GET /profile", rp.Refresher(auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := middleware.FromContext(r.Context())
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"user_id": p.UserID, "org_id": p.OrgID, "tenant_id": p.TenantID})
	}))))
	origin, _ := url.Parse(redirect)
	mux.Handle("POST /use", rp.Refresher(auth(middleware.RequireFeature(feature)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Cookie-authenticated writes require the app origin; cross-site forms cannot bill usage.
		if r.Header.Get("Authorization") == "" && r.Header.Get("Origin") != origin.Scheme+"://"+origin.Host {
			http.Error(w, "same-origin request required", http.StatusForbidden)
			return
		}
		requestID := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
		if requestID == "" || len(requestID) > 128 {
			http.Error(w, "supply an Idempotency-Key of 1 to 128 bytes; reuse it when retrying", http.StatusBadRequest)
			return
		}
		p := middleware.FromContext(r.Context())
		result, err := reporter.Send(r.Context(), circlexo.UsageReport{
			OrgID: p.OrgID, Feature: feature, Qty: 1, IdempotencyKey: p.OrgID + ":basic:" + requestID,
		})
		if err != nil {
			status := http.StatusBadGateway
			if circlexo.IsStatus(err, http.StatusPaymentRequired) {
				status = http.StatusPaymentRequired
			}
			http.Error(w, "usage was not accepted; retry with the same Idempotency-Key", status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	})))))
	return mux, reporter.Close, nil
}

func run() error {
	cfg, err := circlexo.FromEnv()
	if err != nil {
		return err
	}
	redirect, addr, feature := os.Getenv("CIRCLEXO_REDIRECT_URL"), os.Getenv("CIRCLEXO_LISTEN_ADDR"), os.Getenv("CIRCLEXO_FEATURE")
	u, err := url.Parse(redirect)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.Path != "/auth/callback" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return errors.New("CIRCLEXO_REDIRECT_URL must be an absolute HTTP(S) URL ending in /auth/callback")
	}
	if u.Scheme == "http" && u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" && u.Hostname() != "::1" {
		return errors.New("HTTP callbacks are for loopback development only; use HTTPS otherwise")
	}
	if addr == "" || feature == "" {
		return errors.New("CIRCLEXO_LISTEN_ADDR and CIRCLEXO_FEATURE are required")
	}
	key, err := base64.StdEncoding.DecodeString(os.Getenv("CIRCLEXO_SESSION_KEY"))
	if err != nil || len(key) != 32 {
		return errors.New("CIRCLEXO_SESSION_KEY must contain 32 base64-encoded bytes")
	}
	handler, closeReporter, err := newApp(cfg, redirect, key, feature)
	if err != nil {
		return err
	}
	server := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	err = server.ListenAndServe()
	stop()
	<-shutdownDone
	drain, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	closeErr := closeReporter(drain)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return closeErr
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
