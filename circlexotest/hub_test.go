package circlexotest_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/circlexo/circlexo-go/circlexotest"
)

func TestNewServerLifecycle(t *testing.T) {
	hub := circlexotest.NewServer("example")
	t.Cleanup(hub.Close)
	if _, err := hub.Config().Discover(t.Context()); err != nil {
		t.Fatal(err)
	}
	hub.Close()
	// The server is caller-owned and no longer accepts requests after Close.
	req, err := hub.Client().Get(hub.URL + "/.well-known/openid-configuration")
	if err == nil {
		req.Body.Close()
		t.Fatal("closed hub accepted a request")
	}
}

func TestNewRegistersCleanup(t *testing.T) {
	var url string
	client := &http.Client{Timeout: time.Second}
	t.Run("owner", func(t *testing.T) {
		hub := circlexotest.New(t, "example")
		url = hub.URL
		if _, err := hub.Config().Discover(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
	res, err := client.Get(url + "/.well-known/openid-configuration")
	if err == nil {
		res.Body.Close()
		t.Fatal("New did not close its hub during test cleanup")
	}
}
