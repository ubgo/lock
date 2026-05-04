# Contributing to ubgo/locker

Thanks for your interest in `ubgo/locker`. This repository is licensed under the **Apache License 2.0**. Pull requests are welcome.

## Scope

This package is **deliberately tiny**. The interface is:

```go
type Locker interface {
    Acquire(ctx context.Context, name string) (Holder, error)
}
type Holder interface {
    Release() error
}
var ErrLocked = errors.New("locker: locked")
```

PRs that add methods, options, or types to the public surface need a strong rationale — typically: a real consumer needs polymorphism over a feature that more than one of `filelock`/`flock`/`etcdlock`/`pglock`/`redislock` supports.

PRs in scope:

- Documentation improvements (godoc, examples, README)
- Tests of the interface contract
- Bug fixes (rare for an interface-only package)

PRs out of scope:

- Adding fencing tokens, auto-renewal, max-concurrent, sweep, etc. to the interface — these belong on the concrete backend's typed surface
- Implementations of `Locker` — those go in their own modules (`ubgo/filelock`, etc.)
- Retry / backoff helpers — caller's responsibility

## Workflow

1. Open an issue first for anything beyond a tiny fix.
2. Fork + branch named after the issue.
3. Run local checks: `task ci`.
4. Use Conventional Commits for the PR title.

## Code conventions

- **Stdlib-only.** `go.mod` must have zero third-party `require` lines.
- **Race detector mandatory.** Every test must pass under `-race`.
- **Public API stability.** Once the module reaches v1.0.0, breaking changes require a major version bump and a strong rationale.

## License of contributions

By submitting a pull request, you agree that your contribution is provided under the same Apache License 2.0 as the rest of the repository.
