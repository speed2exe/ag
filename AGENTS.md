# AGENTS.md

## Build

```sh
nix-shell -p lua pkg-config readline --run 'go build'
```

### macOS (Homebrew)

Requires Go plus the C dependencies for cgo (`readline` and `lua`, resolved via
`pkg-config`):

```sh
export PKG_CONFIG_PATH="/opt/homebrew/opt/readline/lib/pkgconfig:$PKG_CONFIG_PATH"
brew install go pkg-config readline lua
```
