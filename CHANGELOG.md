# Changelog

Notable changes to circlexo-go are recorded here, following
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Versions follow [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- Nasaq CMS client, host and path resolution, route registration, menus, entries,
  projects and app bindings, with a resolve cache and host middleware.
- In-memory CMS in `circlexotest` for application tests.
- Contribution guide, security policy and community files.

### Changed

- Published the project under the AGPL-3.0 license.

## [0.2.0] - 2026-10-05

### Added

- `Client.ReleaseTenant` tells the hub that a product deleted an organisation's tenant.
- `webhooks.UserUpdated` and `Envelope.User` expose hub profile changes.
- `Claims.Profile` returns name, email, picture and locale from an ID token.
- In-memory hub support for tenant deletion and the picture claim.

### Fixed

- Arabic product names for Zekra and Hosbah.

## [0.1.1] - 2026-09-30

### Changed

- OIDC sign-in no longer requests `offline_access` by default. Apps that need
  refresh tokens must explicitly set `Scopes` and register the scope with the hub.

## [0.1.0] - 2026-09-30

### Added

- Initial Go SDK: hub discovery and token verification, OIDC login and cookie
  sessions, HTTP middleware, entitlements, usage reporting, webhooks, service
  tokens, MCP gateway authentication and app manifest validation.
- In-memory hub for tests, including the `email_verified` ID-token claim.

[Unreleased]: https://github.com/circlexo/circlexo-go/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/circlexo/circlexo-go/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/circlexo/circlexo-go/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/circlexo/circlexo-go/tree/v0.1.0
