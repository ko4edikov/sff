# sff — fast native Salesforce CLI (Go)

A single static Go binary that replaces the daily-driver `sf` commands without the
Node.js/oclif startup cost. It **reuses credentials already stored by the official
`sf` CLI** (read-only) instead of doing its own OAuth login.

## Layout

- `main.go` — entry point; injects version/commit/date via `-ldflags`.
- `internal/cli/` — cobra command tree (`root.go` wires it). One file per command:
  `org.go`, `query.go`, `deploy.go`, `retrieve.go`, `diff.go`.
- `pkg/auth/` — reads sf's `~/.sfdx/<user>.json` (AES-256-GCM, key in macOS
  Keychain) + `~/.sf/config.json` / `~/.sfdx/alias.json`. `Org.Refresh` does the
  OAuth `refresh_token` grant (in-memory only; never writes back to sf's store).
- `pkg/sfapi/` — REST client (auto-refreshes once on 401). `DefaultAPIVersion`,
  composite/batch pagination, Tooling deploy.
- `pkg/mdapi/` — Metadata API over SOAP (describe/retrieve/deploy, zip, manifest).
- `pkg/source/` — source-format convert/decompose/recompose; the decomposition
  table is vendored in `decomposition.json` and `//go:embed`-ed into the binary.
- `pkg/project/` — locates `sfdx-project.json` and its package directories.
- `pkg/progress/` — reusable spinner (silent by default for library consumers).

## Build / test

- `make build` → `./sff`  ·  `make install` → `$GOBIN`  ·  `make test` · `make vet` · `make fmt`
- Release: push a `vX.Y.Z` tag (goreleaser). Repo is public at `ko4edikov/sff`.

## Conventions

- Tab-indented Go and (for any `.cls`) ApexDoc `/** */` blocks per the global rules.
- **Org target resolution** (match `org display`/`org open`): positional arg → `-o/--target-org`
  → configured default org. Register `-o` with `addTargetOrgFlag`; resolve via `auth.Resolve`.
- Commands that hit an org take `-o`; catalog/listing commands don't.
- `SFF_DEBUG` env var turns on stderr SOAP tracing (see `newMDClient` in `root.go`).
- Runtime errors are printed by `Execute`; set `SilenceUsage`/`SilenceErrors`. Use
  `ExitError{Code}` for bare exit codes (e.g. `diff` exits 1 when files differ).
