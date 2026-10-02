# circlexo-go

The Go SDK for CircleXO apps. It lets a product use CircleXO sign-in, org membership, entitlements, pay-as-you-go usage, hub webhooks and the MCP gateway.

```sh
go get github.com/circlexo/circlexo-go
```

| Package | What it does |
|---|---|
| `circlexo` | Config from env, discovery, the JWKS token `Verifier`, and a `Client` for the hub's app APIs |
| `circlexo/oidc` | "Sign in with CircleXO": login (PKCE, PAR), callback, refresh, logout, and a sealed cookie session |
| `circlexo/middleware` | `net/http`/chi middleware that puts a `Principal{UserID, OrgID, OrgRole, TenantID, Scopes, Entitlements}` in the context |
| `circlexo/entitlements` | Cached entitlements (refreshed by `ent_v` and webhooks) with the fail-closed grace |
| `circlexo/usage` | Buffered, idempotent usage reporting with retries |
| `circlexo/webhooks` | Standard Webhooks verification and typed hub events |
| `circlexo/service` | Service tokens (`client_credentials` with a secret or `private_key_jwt`) and token exchange |
| `circlexo/mcp` | Securing a product MCP endpoint for the CircleXO gateway |
| `circlexo/manifest` | `circlexo.app.yaml` types and validation (`go run github.com/circlexo/circlexo-go/cmd/circlexo app validate`) |
| `circlexo/circlexotest` | An in-memory hub for your tests |

## Configuration

Keep every value below in your secret store or environment. Never put them in source or in examples.

| Variable | |
|---|---|
| `CIRCLEXO_ISSUER` | The hub, e.g. `https://accounts.circlexo.com` |
| `CIRCLEXO_APP_ID` | Your app id; tokens for your app carry it as `aud` |
| `CIRCLEXO_CLIENT_ID` | Defaults to `cxo_app_<app id>` |
| `CIRCLEXO_CLIENT_SECRET` | Client secret. Alternatively set `CIRCLEXO_PRIVATE_KEY` (PEM or a path to one) and `CIRCLEXO_KEY_ID` for `private_key_jwt` |
| `CIRCLEXO_APP_KEY` | A `cxa_` app API key. This is an alternative to service tokens for the app APIs |
| `CIRCLEXO_API_URL` | Only if the API is not served on the issuer |

```go
cfg, err := circlexo.FromEnv()
```

## Sign-in

```go
rp, _ := oidc.New(cfg, "https://app.example/auth/circlexo/callback", sessionKey32Bytes)
rp.OnLogin = func(w http.ResponseWriter, r *http.Request, l *oidc.Login) error {
	// l.IDClaims.Subject is the CircleXO user id; l.AccessClaims.OrgID the org they chose.
	return users.Link(r.Context(), l.IDClaims.Subject, l.IDClaims.Raw["email"])
}
mux.HandleFunc("/auth/circlexo/login", rp.Login)       // ?return_to=/dashboard
mux.HandleFunc("/auth/circlexo/callback", rp.Callback)
mux.Handle("/auth/circlexo/logout", rp.Logout("https://app.example/"))
```

## Protecting routes

```go
client := circlexo.NewClient(cfg, nil) // app key; or service.NewSource(svc, scopes...)
ents := entitlements.New(client)
auth := middleware.Auth(middleware.Options{
	Verifier:     circlexo.NewVerifier(cfg),
	Session:      rp.SessionToken,                          // bearer header first, then the session
	Tenants:      &middleware.CachedTenants{Client: client}, // org → your tenant id
	Entitlements: ents,
})
r.With(rp.Refresher, auth, middleware.RequireFeature("invoices")).Post("/invoices", createInvoice)

func createInvoice(w http.ResponseWriter, r *http.Request) {
	p := middleware.FromContext(r.Context())      // p.UserID, p.OrgID, p.TenantID, p.OrgRole
	if n, ok := entitlements.Limit(r.Context(), "seats"); ok { /* enforce n */ }
}
```

`RequireFeature` returns 402 when the org's plan lacks the feature. It also refuses writes while entitlements are read-only.

### Fail-closed

- Entitlements are cached for 5 minutes.
- The cache refetches sooner when a token carries a higher `ent_v`, or when an `entitlement.changed` webhook calls `ents.Invalidate`.
- If the hub is unreachable, the last copy is served with `ReadOnly = true` for 24 hours after the last successful fetch. During that window, let users read and export, but refuse paid writes. After it, paid features are off. Never delete data because of this.

## Usage and pay-as-you-go

```go
rep := usage.NewReporter(client, 1024, 2)
defer rep.Close(ctx)
rep.Report(orgID, "api_calls", 1, "req:"+requestID) // async; the key makes retries safe

// When the work must not happen unpaid, report synchronously first:
res, err := rep.Send(ctx, circlexo.UsageReport{OrgID: orgID, Feature: "sms", Qty: 1, IdempotencyKey: "sms:" + msgID})
if circlexo.IsStatus(err, 402) { /* over the limit or out of wallet balance */ }
if res.LowBalance { /* nudge the org to top up */ }
```

The hub charges each report beyond the plan's included quantity to the org's wallet, and it charges each idempotency key only once.

## Onboarding an org

1. When an org installs your app, the hub sends `org.app_installed`.
2. Create your tenant, then confirm it:

```go
wh := webhooks.NewHandler(os.Getenv("CIRCLEXO_WEBHOOK_SECRET"))
wh.Handle(webhooks.AppInstalled, func(ctx context.Context, e *webhooks.Envelope) error {
	org, _ := e.Org()
	tenantID := tenants.Create(ctx, org.Name, org.Slug)
	_, err := client.ConfirmTenant(ctx, e.OrgID, tenantID, org.Slug)
	return err
})
wh.Handle(webhooks.EntitlementChanged, func(ctx context.Context, e *webhooks.Envelope) error {
	d, _ := e.Entitlement()
	ents.Invalidate(e.OrgID, d.Version)
	return nil
})
wh.Handle(webhooks.SessionRevoked, func(ctx context.Context, e *webhooks.Envelope) error {
	d, _ := e.Session()
	return sessions.EndBySID(ctx, d.SessionID)
})
mux.Handle("/api/circlexo/events", wh)
```

- `client.Org(ctx, orgID)` returns the org and its members, so you can sync them.
- `client.TenantByProductID` maps the other way.
- With more than one instance, set `wh.Seen` to a shared deduper.

## Service calls and token exchange

```go
svc, _ := service.New(cfg)
src := service.NewSource(svc, "billing.usage", "tenants.read") // cached client_credentials tokens
client := circlexo.NewClient(cfg, src)

// Call another app for the current user: the user and org are kept, scopes only narrow.
tok, _ := svc.Exchange(ctx, principal.Token, service.AccessTokenType, "mahaam")
```

## MCP

```go
m, _ := manifest.Parse(yamlBytes, manifest.Options{})
policy := mcp.PolicyFromManifest(m, nil) // or map effects to required scopes
mux.Handle("/.well-known/oauth-protected-resource", mcp.ProtectedResource("https://app.example/mcp", cfg.Issuer, nil))
mux.Handle("/mcp", mcp.Middleware(mcp.Options{Verifier: circlexo.NewVerifier(cfg),
	ResourceMetadataURL: "https://app.example/.well-known/oauth-protected-resource"})(mcpServer))
// In a tool: c := mcp.FromContext(ctx); policy.Allowed(ctx, c, "create_issue"); audit c.UserID and c.Actors().
```

## Locations and addresses

The hub serves a public location database (countries, cities, areas, currencies, languages; English and Arabic) at `/api/locations`; no credentials needed.

```go
loc := locations.New("https://accounts.circlexo.com", nil)
hits, _ := loc.Search(ctx, "cairo", locations.SearchOptions{Type: "city"}) // English or Arabic prefix
cities, _ := loc.Cities(ctx, 65, 0, 0)
addr, formatted, err := loc.FormatAddress(ctx, locations.Address{CountryID: 65, CityID: 1, Street: "15 Tahrir St", Phone: "+201001234567"})
```

Store `locations.Address` (ids of the country, city and area plus street, building, floor, apartment, landmark, postal code, lat/lng, E.164 phone). `FormatAddress` validates it and writes it in English and Arabic; an invalid one is an `*locations.Error` (422, `Fields`).

## Testing

```go
hub := circlexotest.New(t, "demo")
hub.Install("org-1")
hub.Entitlements["org-1"] = circlexo.Entitlements{OrgID: "org-1", Active: true, Features: ...}
cfg := hub.Config()
token := hub.AccessToken(map[string]any{"scope": "openid org"})
```

The SDK's own tests run against it. `TestLive` runs against a real hub when `CIRCLEXO_LIVE_ORG` is set.

## License

Proprietary. © 3x1.
