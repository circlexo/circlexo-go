package oidc_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"time"

	"github.com/circlexo/circlexo-go/circlexotest"
	"github.com/circlexo/circlexo-go/oidc"
)

func ExampleNew() {
	hub := circlexotest.NewServer("demo")
	defer hub.Close()
	mux := http.NewServeMux()
	app := httptest.NewServer(mux)
	defer app.Close()
	// This fixed key is only for the example. Use a secret-store key in production.
	rp, err := oidc.New(hub.Config(), app.URL+"/callback", []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		panic(err)
	}
	rp.OnLogin = func(_ http.ResponseWriter, _ *http.Request, login *oidc.Login) error {
		fmt.Println("signed in:", login.IDClaims.Subject)
		return nil
	}
	mux.HandleFunc("/login", rp.Login)
	mux.HandleFunc("/callback", rp.Callback)
	mux.HandleFunc("/done", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "welcome") })
	jar, err := cookiejar.New(nil)
	if err != nil {
		panic(err)
	}
	browser := &http.Client{Jar: jar, Timeout: 5 * time.Second}
	// Follow login -> fake hub -> callback, preserving the state/session cookies.
	res, err := browser.Get(app.URL + "/login?return_to=/done")
	if err != nil {
		panic(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		panic(err)
	}
	fmt.Println(res.StatusCode, string(body))
	// Output:
	// signed in: user-1
	// 200 welcome
}
