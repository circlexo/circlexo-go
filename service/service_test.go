package service_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"testing"

	"github.com/circlexo/circlexo-go"
	"github.com/circlexo/circlexo-go/circlexotest"
	"github.com/circlexo/circlexo-go/service"
)

var ctx = context.Background()

func TestServiceToken(t *testing.T) {
	h := circlexotest.New(t, "demo")
	c, err := service.New(h.Config())
	if err != nil {
		t.Fatal(err)
	}
	src := service.NewSource(c, "billing.usage")
	a, err := src.Token(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := src.Token(ctx); b != a {
		t.Fatal("token not cached")
	}
	// The source authenticates the hub client.
	cl := circlexo.NewClient(h.Config(), src)
	if _, err := cl.ReportUsage(ctx, circlexo.UsageReport{OrgID: "o", Feature: "f", Qty: 1, IdempotencyKey: "k"}); err != nil {
		t.Fatal(err)
	}

	cfg := h.Config()
	cfg.ClientSecret = "wrong"
	bad, _ := service.New(cfg)
	var oe *service.OAuthError
	if _, err := bad.ServiceToken(ctx); !errors.As(err, &oe) || oe.Code != "invalid_client" {
		t.Fatalf("wrong secret: %v", err)
	}
	cfg.ClientSecret = ""
	if _, err := service.New(cfg); err == nil {
		t.Fatal("no credentials accepted")
	}
}

func TestPrivateKeyJWTAndExchange(t *testing.T) {
	h := circlexotest.New(t, "demo")
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKCS8PrivateKey(k)
	cfg := h.Config()
	cfg.ClientSecret = ""
	cfg.PrivateKeyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	cfg.KeyID = "k1"
	c, err := service.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ServiceToken(ctx, "tenants.read"); err != nil {
		t.Fatalf("private_key_jwt: %v", err)
	}

	user := h.AccessToken(nil)
	tok, err := c.Exchange(ctx, user, "", "mahaam")
	if err != nil || tok.IssuedTokenType != service.AccessTokenType {
		t.Fatalf("exchange = %+v, %v", tok, err)
	}
	v := circlexo.NewVerifier(h.Config())
	v.Audience = "mahaam"
	cl, err := v.VerifyAccessToken(ctx, tok.AccessToken)
	if err != nil || cl.Actor == nil || cl.Actor.ClientID != "cxo_app_demo" {
		t.Fatalf("exchanged = %+v, %v", cl, err)
	}
}
