# Basic HTTP app

Run from the repository root: `go run ./examples/basic`. This example composes
OIDC sign-in, cookie/bearer authentication, tenant resolution, an entitlement
check and one synchronous usage report. Tests use only the local fake hub:

```sh
go test -race ./examples/basic
```

For a real **test app**, load these values from your environment/secret store;
no issuer, app ID or credential is embedded in the example:

- The SDK's `CIRCLEXO_ISSUER`, `CIRCLEXO_APP_ID`, and client credentials
  (`CIRCLEXO_CLIENT_SECRET`, or `CIRCLEXO_PRIVATE_KEY` with `CIRCLEXO_KEY_ID`).
  This example also requires `CIRCLEXO_APP_KEY` for its hub API calls; it does not
  configure a service-token source. See the root README for other SDK configurations.
- `CIRCLEXO_REDIRECT_URL`: registered callback ending in `/auth/callback`.
  HTTP is allowed only on loopback; use HTTPS for a deployed app.
- `CIRCLEXO_LISTEN_ADDR`: the interface/port to listen on, such as
  `127.0.0.1:8080` for local development.
- `CIRCLEXO_SESSION_KEY`: a persistent secret containing 32 base64-encoded
  random bytes (`openssl rand -base64 32` can generate it). Do not commit it.
- `CIRCLEXO_FEATURE`: a metered feature configured for your app. Install the
  app for the test organisation, confirm its tenant, and grant that feature.

Open `/auth/login?return_to=/profile` and complete sign-in. `/profile` returns
the authenticated user/org/tenant IDs; anonymous requests get 401.

**`POST /use` sends a real usage report to your configured hub and can bill a
metered feature.** It checks the entitlement first and refuses read-only/off
features. Send an `Idempotency-Key` header of 1–128 bytes for one logical action;
reuse the same key when retrying an uncertain response, never generate a new key
for that retry. The example scopes the key to the authenticated organisation.
Cookie-authenticated requests must also carry the app's exact `Origin` header.
GET requests never report usage. A bearer client can send the same POST with its
app-scoped access token. Missing/disabled entitlements return 402 without a
usage report. Ctrl+C shuts down the HTTP server and closes the reporter.

This is a composition example, not a complete application: production apps also
need provisioning/webhook handlers, entitlement-cache invalidation, persistent
business-operation idempotency, operational limits and their own deployment/TLS
configuration. Do not store credentials in browser code.
