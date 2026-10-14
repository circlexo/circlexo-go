package middleware_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/circlexo/circlexo-go"
	"github.com/circlexo/circlexo-go/circlexotest"
	"github.com/circlexo/circlexo-go/middleware"
)

func ExampleAuth() {
	hub := circlexotest.NewServer("demo")
	defer hub.Close()
	hub.Install("org-1")
	client := circlexo.NewClient(hub.Config(), nil)
	if _, err := client.ConfirmTenant(context.Background(), "org-1", "tenant-1", "demo"); err != nil {
		panic(err)
	}
	auth := middleware.Auth(middleware.Options{
		Verifier: circlexo.NewVerifier(hub.Config()),
		Tenants:  &middleware.CachedTenants{Client: client},
	})
	handler := auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal := middleware.FromContext(r.Context())
		fmt.Fprintln(w, principal.UserID, principal.TenantID)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+hub.AccessToken(nil))
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	fmt.Print(res.Code, " ", res.Body.String())
	denied := httptest.NewRecorder()
	handler.ServeHTTP(denied, httptest.NewRequest(http.MethodGet, "/", nil))
	fmt.Println("anonymous:", denied.Code)
	// Output:
	// 200 user-1 tenant-1
	// anonymous: 401
}
