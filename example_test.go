package circlexo_test

import (
	"context"
	"fmt"
	"os"

	"github.com/circlexo/circlexo-go"
	"github.com/circlexo/circlexo-go/circlexotest"
)

func ExampleFromEnv() {
	// Isolate the example from any existing CircleXO configuration.
	keys := []string{"CIRCLEXO_ISSUER", "CIRCLEXO_API_URL", "CIRCLEXO_APP_ID", "CIRCLEXO_CLIENT_ID", "CIRCLEXO_CLIENT_SECRET", "CIRCLEXO_PRIVATE_KEY", "CIRCLEXO_KEY_ID", "CIRCLEXO_APP_KEY"}
	for _, key := range keys {
		value, present := os.LookupEnv(key)
		defer func() {
			if present {
				_ = os.Setenv(key, value)
			} else {
				_ = os.Unsetenv(key)
			}
		}()
		if err := os.Unsetenv(key); err != nil {
			panic(err)
		}
	}
	if err := os.Setenv("CIRCLEXO_ISSUER", "https://hub.example"); err != nil {
		panic(err)
	}
	if err := os.Setenv("CIRCLEXO_APP_ID", "demo"); err != nil {
		panic(err)
	}
	cfg, err := circlexo.FromEnv()
	if err != nil {
		panic(err)
	}
	fmt.Println(cfg.Issuer, cfg.AppID, cfg.ClientID)
	// Output: https://hub.example demo cxo_app_demo
}

func ExampleNewVerifier() {
	hub := circlexotest.NewServer("demo")
	defer hub.Close()
	verifier := circlexo.NewVerifier(hub.Config())
	claims, err := verifier.VerifyAccessToken(context.Background(), hub.AccessToken(nil))
	if err != nil {
		panic(err)
	}
	fmt.Println(claims.Subject, claims.OrgID)
	_, err = verifier.VerifyAccessToken(context.Background(), hub.AccessToken(map[string]any{"aud": "other"}))
	fmt.Println("wrong audience rejected:", err != nil)
	// Output:
	// user-1 org-1
	// wrong audience rejected: true
}
