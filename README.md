# sff — fast Salesforce CLI

A native (Go) replacement for the daily-driver `sf` commands (`data query`,
`deploy`, `retrieve`, ...). Goal: eliminate the Node.js/oclif startup overhead
by shipping a single static binary.

## Why

Measured on this machine (`@salesforce/cli/2.139.6`, node v22, 35 plugins, 210 MB):

| Scenario | Time | What it includes |
|---|---|---|
| bare `node -e 0` | ~0.08 s | runtime only |
| `sf --version` / `--help` | ~2.5 s | + oclif plugin scan/load (no network, no command logic) |
| `sf data query` | ~4.5 s | + command module load (jsforce, ...) + network |
| raw REST query via `curl` | ~0.2 s | the actual work |

**~95% of a `sf data query` is pure JS/oclif overhead. Only ~0.2 s is real
work.** A Go equivalent should land around the curl time → **~15–20x faster**
on read commands.

## Auth model (how sff reads credentials)

sff does **not** shell out to `sf` for tokens (too slow; secrets are also
redacted in newer `sf` versions). Instead it reads the same files `sf` uses:

- `~/.sfdx/<username>.json` — per-org auth: `accessToken`, `refreshToken`
  (both **encrypted**), `instanceUrl`, `loginUrl`, `clientId` (= `PlatformCLI`,
  the public OAuth client id used by sf).
- Encryption key: macOS Keychain, `service=sfdx`, `account=local` (AES).
- Default org: `~/.sf/config.json` (`target-org`) and `~/.sfdx/alias.json`.

Flow: read auth file → fetch AES key from keychain → decrypt `accessToken` →
on 401, refresh via `POST {loginUrl}/services/oauth2/token`
(`grant_type=refresh_token`, `client_id=PlatformCLI`).

## Reading sf's credentials (`sff org display`)

sff reuses the orgs you already authenticated with `sf` — no separate login. It
reads the files directly (never shells out to `sf`, which is slow and redacts
secrets in newer versions):

```sh
sff org display              # default org (~/.sf/config.json target-org)
sff org display pr-dev       # by alias (~/.sfdx/alias.json)
sff org display user@x.com   # by username
sff org display pr-dev --refresh   # refresh the access token first
```

Decryption details (verified against `@salesforce/cli` on macOS):

- Tokens in `~/.sfdx/<username>.json` are **AES-256-GCM**. Stored form is
  `<iv:12 hex chars><ciphertext hex>:<authTag:32 hex chars>`, where the 12-char
  IV string is used as 12 raw ASCII bytes (the GCM nonce).
- The 32-byte key is a generic password in the macOS Keychain
  (`service=sfdx, account=local`), used as ASCII bytes (not hex-decoded).
- On an expired token, refresh via `POST {loginUrl}/services/oauth2/token`
  (`grant_type=refresh_token`, `client_id` from the auth file). Currently the
  refreshed token is kept in memory only (sff does not write back to `~/.sfdx`).

A native browser login (`sff login`, OAuth web flow + PKCE on the public
`PlatformCLI` connected app) is deferred until after the read path is useful.

## Querying (`sff query`)

```sh
sff query "SELECT Id, Name FROM Account LIMIT 10"          # default org, table
sff query "SELECT Id FROM Contact" -o pr-dev               # pick org by alias
sff query "SELECT Id, Name FROM Profile LIMIT 1" --json    # sf-compatible JSON
sff query "SELECT Id, Name FROM Account" --csv             # CSV to stdout
sff query "SELECT Id, Name FROM Account" --csv -f acc.csv  # CSV to a file
sff query "SELECT Id, Name FROM ApexClass" -t              # Tooling API (sf data query -t)
```

Output formats: table (default), `--json`, `--csv` (mutually exclusive).
`-f/--out-file` writes the data to a file; the timing summary then goes to
stderr, so piped or saved output stays clean.

`--json` mirrors `sf data query --json` exactly — `{"status":0,"result":
{"records":[…],"totalSize":N,"done":true}}` — so sff is a drop-in replacement
in scripts that parse `.result.records`.

## Retrieving metadata (`sff retrieve`)

Retrieves **metadata-format** files via the Metadata API (SOAP), selected by
`-m Type:Name` specifiers (sff builds the `package.xml`) or an existing
manifest. No source-format decomposition yet — files land as the API returns
them (which already matches source layout for Apex, LWC, Aura, etc.).

```sh
sff retrieve -m ApexClass:MyClass -d ./out          # one component
sff retrieve -m ApexClass -m LWC:myCmp -o pr-dev     # multiple; bare type = wildcard *
sff retrieve -x manifest/package.xml -d ./out        # from an existing manifest
```

Notes:
- A bare `-m ApexClass` retrieves all members (`*`). `Type:Name` retrieves one.
- Friendly aliases: `apex`→`ApexClass`, `lwc`→`LightningComponentBundle`,
  `aura`→`AuraDefinitionBundle`. Other types pass through verbatim.
- Components from **managed packages** must be requested with their namespace
  (e.g. `clm__Foo`); a bare name returns only `package.xml`.
- Transport is hand-rolled SOAP (`encoding/xml`, no dependency); the session is
  refreshed once on an `INVALID_SESSION_ID` fault.

Flags may go before or after the SOQL. The client follows `nextRecordsUrl`
pagination and refreshes the access token once on a 401. End-to-end this runs
in ~0.3 s vs ~4.5 s for `sf data query`.

## Roadmap

- [x] `internal/auth` — read `sf` auth files, Keychain decrypt, token refresh
- [x] `internal/sfapi` — REST client with auto-refresh on 401
- [x] `sff query "SELECT ..."` — SOQL with pagination, table / `--json` / `--csv` output (~0.3s)
- [x] `sff retrieve` — Metadata API (SOAP), `-m`/`-x`, metadata-format output
- [ ] `sff deploy` — Metadata API deploy (zip a dir / source passthrough)
- [ ] source↔metadata conversion (CustomObject decomposition)
- [ ] `sff apex run`, `sff data get/create/update/delete`

## Build

```sh
go build -o sff .
./sff --version
./sff --help     # command tree (built on cobra)
```
