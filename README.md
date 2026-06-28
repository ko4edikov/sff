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
sff org list                 # all authenticated orgs (▸ marks the default)
sff org list --json
sff org list metadata-types -o pr-dev      # the org's metadata type catalog
sff org list metadata-types --refresh      # re-fetch (bypass the ~/.sff cache)
sff org display              # default org (~/.sf/config.json target-org)
sff org display pr-dev       # by alias (~/.sfdx/alias.json)
sff org display user@x.com   # by username
sff org display pr-dev --refresh   # refresh the access token first
```

`sff org list` reads the auth files directly (no token decryption, no network),
so it's instant; it skips sf's `*.sandbox.json` tracking stubs.

`sff org list metadata-types` calls the Metadata API `describeMetadata` (analog
of `sf org list metadata-types`) and prints each type's `directoryName`,
`suffix`, `metaFile`, `inFolder`, and `childXmlNames`. The result is cached in
`~/.sff/describe-<orgid>-v<api>.json` (the catalog changes rarely); `--refresh`
re-fetches. This catalog is what will drive correct `package.xml` building and
source-format decomposition.

Decryption details (verified against `@salesforce/cli` on macOS):

- Tokens in `~/.sfdx/<username>.json` are **AES-256-GCM**. Stored form is
  `<iv:12 hex chars><ciphertext hex>:<authTag:32 hex chars>`, where the 12-char
  IV string is used as 12 raw ASCII bytes (the GCM nonce).
- The 32-byte key is read from wherever sf stores it for the platform, used as
  ASCII bytes (not hex-decoded): the macOS Keychain (`security`, service=sfdx,
  account=local), Linux libsecret (`secret-tool lookup user local domain sfdx`),
  or — on Windows and as a universal fallback — the file `~/.sfdx/key.json`.
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

Retrieves metadata via the Metadata API (SOAP), selected by `-m Type:Name`
specifiers (sff builds the `package.xml`) or an existing manifest. By default
the result is **converted to source format** and merged into the sfdx project,
like `sf project retrieve start`; `--metadata-format` keeps the raw
metadata-format files instead.

```sh
sff retrieve -m ApexClass:MyClass                    # → source format into the project
sff retrieve -m ApexClass -m LWC:myCmp -o pr-dev      # multiple; bare type = wildcard *
sff retrieve -x manifest/package.xml                  # from an existing manifest
sff retrieve -m ApexClass:MyClass --metadata-format -d ./mdapi   # raw metadata-format unzip
```

Notes:
- A bare `-m ApexClass` retrieves all members (`*`). `Type:Name` retrieves one.
- **Source format** (default): the project is found by searching up from the
  current directory (override with `--project-dir`). Existing files are
  overwritten in place; new ones land under the default package directory's
  `main/default` tree. Classification is driven by the org's `describeMetadata`
  catalog (`metaFile`/`suffix`): content types (Apex, LWC, Aura) copy verbatim;
  XML-only types (PermissionSet, Tab, CustomMetadata, …) get the `-meta.xml`
  suffix and are re-serialized to match sf (LF endings, empty tags expanded).
- **Decomposition**: `CustomObject` (and `CustomObjectTranslation`, `Bot`) are
  split into source files (`objects/Account/fields/X__c.field-meta.xml`, etc.) —
  byte-for-byte identical to `sf project convert mdapi`. The rules live in a
  vendored `decomposition.json` (embedded via `go:embed`), since these
  source-format conventions aren't reported by `describeMetadata`.
- **StaticResource**: the `.resource` binary is renamed to its real extension
  from the meta's `contentType` (e.g. `Foo.resource` → `Foo.png`/`.js`/`.bin`),
  and archive resources (`application/zip`/`x-zip-compressed`/`jar`) are expanded
  into a `Foo/` directory — matching sf's `mime`-based mapping byte-for-byte.
- Friendly aliases: `apex`→`ApexClass`, `lwc`→`LightningComponentBundle`,
  `aura`→`AuraDefinitionBundle`. Other types pass through verbatim.
- Components from **managed packages** must be requested with their namespace
  (e.g. `clm__Foo`); a bare name returns only `package.xml`.
- Transport is hand-rolled SOAP (`encoding/xml`, no dependency); the session is
  refreshed once on an `INVALID_SESSION_ID` fault.

Flags may go before or after the SOQL. The client follows `nextRecordsUrl`
pagination and refreshes the access token once on a 401. End-to-end this runs
in ~0.3 s vs ~4.5 s for `sf data query`.

## Comparing with the org (`sff diff`)

Fetches a component's source from the org via the Tooling API and compares it
with the local copy. Supports Apex flat files (`.cls`/`.trigger`/`.page`/
`.component`) and LWC/Aura bundles. The target may be a path or a bare name
(searched from the current directory).

```sh
sff diff MyClass                      # unified diff to stdout, exit 1 if differs
sff diff force-app/.../lwc/myCmp      # bundle (directory diff)
sff diff MyClass OtherClass lwc/myCmp # several targets at once
sff diff classes/                     # a directory: recurses into all metadata
sff diff MyClass -o pr-dev
```

Each argument may be a file, an lwc/aura bundle, or a **directory** (walked
recursively for all supported metadata). Multiple targets are diffed in
sequence; a missing/failed target is reported but doesn't abort the rest, and
the exit code is 1 if any target differs or fails.

Viewer selection (for a GUI/terminal diff tool instead of stdout):

```sh
export SFF_DIFF='idea diff {remote} {local}'   # e.g. in ~/.zshrc
sff diff MyClass                               # opens the configured viewer
sff diff MyClass --exec 'code --diff {remote} {local}'   # one-off override
```

- Resolution order: `--exec` → `$SFF_DIFF` → built-in unified diff.
- `{remote}` is a temp file (flat) or directory (bundle); `{local}` is the
  working copy. Org content is normalized (CRLF→LF, trailing whitespace, final
  newline matched to local) so only real differences show.
- The built-in fallback shells out to `diff -u`/`diff -ru`; viewer mode and the
  org fetch need no external tools. This replaces the old `sf-compare` script.
- Output is colorized like git (added green, removed red, hunks cyan) when
  stdout is a terminal; it stays plain when piped/redirected or when `NO_COLOR`
  is set. When a target matches the org, sff prints `✓ <name>: no differences`
  (to stderr) and exits 0 — exit 1 means at least one target differs.

## Roadmap

- [x] `internal/auth` — read `sf` auth files, Keychain decrypt, token refresh
- [x] `internal/sfapi` — REST client with auto-refresh on 401
- [x] `sff query "SELECT ..."` — SOQL with pagination, table / `--json` / `--csv` output (~0.3s)
- [x] `sff retrieve` — Metadata API (SOAP), `-m`/`-x`, source-format by default (`--metadata-format` for raw)
- [x] `sff org list metadata-types` — describeMetadata catalog (cached in `~/.sff`)
- [x] source-format decomposition for `CustomObject`/`CustomObjectTranslation`/`Bot` (byte-identical to sf)
- [x] source-format conversion for `StaticResource` (content-type rename + archive expansion)
- [x] `sff diff` — compare local Apex/LWC/Aura against the org (Tooling API)
- [ ] `sff deploy` — Metadata API deploy (zip a dir / source passthrough)
- [ ] `sff apex run`, `sff data get/create/update/delete`

## Build

```sh
go build -o sff .
./sff --version
./sff --help     # command tree (built on cobra)
```
