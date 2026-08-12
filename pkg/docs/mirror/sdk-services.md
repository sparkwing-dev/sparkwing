<!-- GENERATED from the `sparkwing` package via go/doc (internal/sdkref). Do not edit by hand; regenerate with `bash bin/gen-sdk-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# SDK API reference: `sparkwing/services`

Package services is the sparkwing SDK's sidecar-container helper: spin up postgres/redis/etc.

Import as `swservices "github.com/sparkwing-dev/sparkwing/sparkwing/services"`. The root package and the other subpackages are indexed in [sdk-reference.md](sdk-reference.md).

## Functions

- `func WithServices(ctx context.Context, services []Service, fn func(context.Context) error) error` -- WithServices starts every given Service, waits for each to become ready, invokes fn, and then tears the services down.

## Types

### type Service

Service describes a sidecar container to spin up via `docker run -d --network=host`.

```
type Service struct {
    // Image is the fully-qualified image reference, e.g. "postgres:15-alpine".
    // Required.
    Image string

    // Name is the container name. Optional; derived from the image's
    // last path segment plus a short random suffix to prevent
    // collisions when the same pipeline runs concurrently.
    Name string

    // Port is the container port the service listens on. When set, it is
    // published to 127.0.0.1:<Port> so a host process (the test) reaches
    // it at localhost:<Port> on every platform incl. Docker Desktop.
    // When zero, the container uses host networking (Linux only).
    Port int

    // Env is the set of environment variables to pass to the container.
    Env map[string]string

    // ReadyCmd is a shell command run inside the container via
    // `docker exec`. The service is ready when this exits 0. If
    // empty, WithServices falls back to a fixed 2s sleep.
    ReadyCmd string

    // ReadyTimeout bounds how long WithServices will wait for ReadyCmd
    // to succeed. Zero means DefaultReadyTimeout (30s).
    ReadyTimeout time.Duration
}
```


## Constants

```
const DefaultReadyTimeout = 30 * time.Second
```

## Variables

```
var ErrDockerUnavailable = docker.ErrDockerUnavailable
```
