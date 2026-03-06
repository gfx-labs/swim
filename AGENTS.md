# AGENTS.md — swim

Custom Caddy server with modules for hosting static websites.
Module: `github.com/gfx-labs/swim` — Go, Caddy v2.

## Build / Test / Run Commands

```bash
# Build the binary
go build -v ./...

# Run all tests (verbose)
go test -v ./...

# Run a single test by name
go test -v -run TestS3Integration ./modules/vfs/

# Run a single test file (specify the package)
go test -v ./modules/vfs/ -run TestFunctionName

# Skip integration tests (long-running, need network)
go test -short ./...

# Run tests with race detector
go test -race ./...

# Tidy modules
go mod tidy

# Release (bumps patch version + goreleaser)
make release
```

There is no linter configured. Use `go vet ./...` for static analysis.
No formatter config exists — use `gofmt` or `goimports`.

## Project Architecture

```
main.go              — Entry point, imports plugin/ and runs Caddy
modules/             — Core implementation (business logic)
  vfs/               — Archive-based virtual filesystem (tar.gz/zip, S3, HTTP)
  localfs/           — Simple local filesystem module
  prerender/         — SEO prerender middleware
plugin/              — Caddy module registration only (init.go files)
  vfs/init.go        — Registers vfs.Vfs with Caddy
  localfs/init.go    — Registers localfs.Localfs with Caddy
  prerender/init.go  — Registers prerender.Prerender + Caddyfile directive
```

Registration chain: `main.go` → `plugin/init.go` → `plugin/*/init.go` → `modules/*/`.
Implementation lives in `modules/`; registration lives in `plugin/`. Keep them separate.

## Code Style Guidelines

### Imports

Two groups separated by a blank line: stdlib first, then all third-party together.
No sub-grouping between Caddy, AWS, afero, zap, or internal packages.

```go
import (
    "fmt"
    "strings"

    "github.com/caddyserver/caddy/v2"
    "github.com/spf13/afero"
    "go.uber.org/zap"
)
```

Use blank imports (`_`) for side-effect registration in `plugin/` init files.
Use named imports only when needed (e.g., `caddycmd` for `caddy/v2/cmd`).

### Naming Conventions

- **Types**: Short PascalCase. Acronyms are NOT fully capitalized: `Vfs`, `Localfs` (not `VFS`, `LocalFS`).
- **Receivers**: Single-letter or two-letter, matching the type: `s *Vfs`, `o *Overlay`, `p *Prerender`, `c CrawlerUserAgents`.
- **Unexported helpers**: camelCase: `filetypeFromName`, `headerOrEnv`, `filesystemFromReader`.
- **Unexported struct fields**: Short names: `a afero.Fs`, `log *zap.Logger`, `closers []func()`.
- **Struct field order**: Exported config fields (with JSON tags) first, then unexported runtime fields, then embedded interfaces, then cleanup infrastructure.

### Struct Tags

JSON tags use `snake_case`. Apply `omitempty` to optional fields only:

```go
type Overlay struct {
    Root    string            `json:"root,omitempty"`
    WorkDir string            `json:"workdir,omitempty"`
    Type    string            `json:"type,omitempty"`
    Headers map[string]string `json:"request_headers"`
}
```

### Error Handling

- Return errors directly (bare `return nil, err`) in most cases.
- Wrap with `fmt.Errorf("context: %w", err)` only at meaningful semantic boundaries.
- Use `fmt.Errorf("description: %s", value)` (no `%w`) for creating new error messages.
- No sentinel errors or custom error types — keep it simple.

```go
// At a meaningful boundary — wrap with %w
return fmt.Errorf("initialize overlay %s: %w", s.Overlay.String(), err)

// Creating a new error — no wrapping
return nil, fmt.Errorf("unrecognized scheme: %s", u.Scheme)
```

### Logging

- Get logger from Caddy context: `s.log = ctx.Logger()` in `Provision()`.
- Store as `log *zap.Logger` (unexported field).
- Use `Debug` level only. Use structured fields (`zap.Any`, `zap.Duration`), not string formatting.
- Log messages are lowercase: `"initializing vfs"`, `"provisioned localfs"`.

### Comments

- Minimal and informal. No godoc on exported types or methods.
- Inline comments are lowercase, no trailing period.
- Use `// TODO:` for future work.

### Interfaces

- Use compile-time interface assertions: `var _ fs.FS = (*Vfs)(nil)`.
- Embed `fs.FS` in structs, override `Open()` for path normalization.
- Use `afero.Fs` as internal filesystem abstraction, convert via `afero.NewIOFS()`.

### Caddy Module Pattern

Every module implements this pattern:

```go
func (s *TypeName) CaddyModule() caddy.ModuleInfo {
    return caddy.ModuleInfo{
        ID: "caddy.fs.modulename",
        New: func() caddy.Module { return new(TypeName) },
    }
}

func (s *TypeName) Provision(ctx caddy.Context) error { ... }
func (s *TypeName) Cleanup() error { ... }
func (s *TypeName) UnmarshalCaddyfile(d *caddyfile.Dispenser) error { ... }
```

Module IDs follow Caddy namespace: `caddy.fs.*` for filesystems, `http.handlers.*` for middleware.

### Caddyfile Parsing

In `UnmarshalCaddyfile`:
- Consume positional args with `d.NextArg()` / `d.Args()`.
- Parse block options with `for nesting := d.Nesting(); d.NextBlock(nesting);` and `switch strings.ToLower(key)`.
- Return `d.ArgErr()` for missing args, `d.SyntaxErr(...)` for invalid options.

### Testing

- Use external test packages (`package vfs_test`, not `package vfs`).
- Table-driven tests with anonymous struct slices and `t.Run` subtests.
- Use `tt` as the test case iterator variable.
- Use `github.com/stretchr/testify/require` (not `assert`) — fail immediately on first failure.
- Guard integration tests with `testing.Short()`:

```go
func TestSomething(t *testing.T) {
    if testing.Short() {
        t.Skip("Skipping integration test in short mode")
    }
    tests := []struct {
        name    string
        input   string
        wantErr bool
    }{...}
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            require.NoError(t, err)
        })
    }
}
```

### Filesystem Composition Pattern

Layer afero filesystems: BasePathFs → CacheOnReadFs (with MemMapFs) → ReadOnlyFs:

```go
ofs := afero.NewBasePathFs(fs, wd)
ofs = afero.NewReadOnlyFs(afero.NewCacheOnReadFs(ofs, afero.NewMemMapFs(), 0))
```

### Path Normalization

Both `Vfs.Open()` and `Localfs.Open()` strip leading/trailing slashes:
```go
name = strings.Trim(name, "/")
```

### Placeholder Resolution

Use `caddy.NewReplacer()` to resolve `{env.*}` and other Caddy placeholders in config values during `Provision()`.
