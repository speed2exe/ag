# AGENTS.md

## Build

```sh
nix-shell -p lua pkg-config readline --run 'go build'
```

### macOS (Homebrew)

Requires Go plus the C dependencies for cgo (`readline` and `lua`, resolved via
`pkg-config`):

```sh
brew install go pkg-config readline lua
```
