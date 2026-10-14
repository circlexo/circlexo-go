# AGENTS.md

Guidance for AI coding agents (Claude Code, Codex and others) working in this repository.

circlexo-go is the Go SDK for CircleXO apps. It is a library: no services, no database.

## Commands

```bash
gofmt -l .              # must print nothing
go vet ./...
go test -race ./...     # all tests run against the in-memory hub in circlexotest
go test -run TestName ./oidc/
go run ./cmd/circlexo app validate   # validate a circlexo.app.yaml
```

`TestLive` talks to a real hub only when `CIRCLEXO_LIVE_ORG` and the `CIRCLEXO_*` config are set;
never hard-code hub URLs, ids or secrets in code, tests or examples.

## Layout

| Package | Role |
|---|---|
| root (`circlexo`) | Config from env, discovery, JWKS `Verifier`, hub `Client` |
| `oidc` | Sign-in (PKCE, PAR), callback, refresh, logout, sealed cookie session |
| `middleware` | `net/http`/chi middleware that puts a `Principal` in the context |
| `entitlements`, `usage`, `webhooks`, `service`, `mcp` | Entitlements cache, usage reporting, webhook verification, service tokens, MCP gateway auth |
| `manifest`, `cmd/circlexo` | `circlexo.app.yaml` types, validation and CLI |
| `cms` | Nasaq CMS client |
| `circlexotest` | In-memory hub and CMS for tests |

## Rules

- Fail closed: an unverifiable token, an unknown entitlement state or a bad webhook signature
  is a denial, never a pass.
- Public API changes need a test against `circlexotest` and a README update.
- Keep dependencies minimal; the SDK is imported by every CircleXO app.
