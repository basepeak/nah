# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build & Development Commands

```bash
# Run all tests
make test            # or: go test ./...

# Run a single test
go test ./pkg/apply/ -run TestSpecificName

# Lint
make validate        # runs golangci-lint

# Full CI validation (generate, tidy, check for dirty repo)
make validate-ci

# Code generation (deepcopy)
go generate
```

## What This Is

`nah` is a Go library for building Kubernetes controllers using an HTTP-style handler/middleware routing pattern on top of `controller-runtime`. It is not a standalone application — it is imported by other projects (notably in the obot-platform ecosystem).

Module: `github.com/obot-platform/nah` (Go 1.25.4)

## Architecture

### Core Abstraction: Router + Handler

The central pattern mirrors `net/http`: handlers receive a `Request` (wrapping a K8s object, client, and context) and write to a `Response`. Middleware wraps handlers for cross-cutting concerns.

```
router.Handler     → func(Request, Response) error
router.Middleware  → func(Handler) Handler
router.Request     → { Client, Object, Ctx, GVK, Namespace, Name, Key, FromTrigger }
router.Response    → { Attributes(), RetryAfter() }
```

Routes are built with `RouteBuilder` (fluent API) and registered on a `Router`.

### Package Dependency Flow

```
nah (top-level)          Entry points: DefaultRouter(), NewRouter(), DefaultOptions()
  └── pkg/router         Route matching, handler orchestration, triggers, finalizers, healthz
        ├── pkg/backend  Interface: Trigger, Watcher, CacheFactory, client.WithWatch
        ├── pkg/runtime  Backend implementation: shared controllers, worker queues, caching
        ├── pkg/apply    Declarative object reconciliation with ownership and pruning
        └── pkg/leader   Leader election (Lease-based or file-based locks)
```

### Key Packages

- **`router`** — Core routing engine. `Handler`/`HandlerFunc`/`Middleware`/`ErrorHandler` types. `RouteBuilder` for declarative route registration. Trigger management for cross-GVK dependencies.
- **`router/tester`** — Test harness for handler unit tests. Uses YAML fixtures (`input.yaml`, `existing.yaml`, `expected.yaml`) and golden file testing via `autogold`. `Harness.Invoke()` runs a handler against a fake client without a real cluster.
- **`backend`** — Interface layer abstracting K8s cache, triggers, and watch operations. `Backend` is the main interface consumed by the router.
- **`runtime`** — Implements `Backend` using controller-runtime. Manages `SharedControllerFactory`, worker queues, rate limiting, and `WorkerQueueSplitter` for parallelism.
- **`apply`** — Kubernetes-style declarative state management. `Apply` interface provides `Ensure()` (idempotent create), `Apply()` (ownership-based reconciliation with pruning), `WithOwnerSubContext()` for hierarchical ownership.
- **`leader`** — Leader election. `NewDefaultElectionConfig()` for lease-based (production), `NewFileElectionConfig()` for file-based (local dev). TTL defaults: 1 min production, 1 hour with `NAH_DEV_MODE`.
- **`conditions`** — `ErrTerminal` for non-recoverable errors. `ErrorMiddleware()` automatically sets K8s `Condition` status on handler objects.
- **`typed`** — Generic utilities for slices, maps, channels, and functions.
- **`watcher`** — Resumable K8s watch with bookmark tracking and terminal/non-terminal error distinction.

### Environment Variables

- `NAH_THREADINESS` — Default worker count per GVK (default: 10)
- `NAH_DEV_MODE` — Enables dev mode (longer leader election TTL)

### Testing Patterns

No test files exist in this repo currently. The `router/tester` package provides a test harness for downstream consumers:
- `tester.DefaultTest(t, scheme, "testdata/case1", handlerFunc)` — loads YAML fixtures from a directory
- `tester.Harness` — configurable test runner with `Existing`, `ExpectedOutput`, and golden file support
- `tester.NewRequest()` — creates a `router.Request` with a fake client for direct handler invocation
- Golden files use `autogold` (`expected.golden`); assertions use `testify`

### Linting

Uses `golangci-lint` v1.64.6 with: `dupl`, `errcheck`, `ginkgolinter`, `gocyclo`, `govet`, `ineffassign`, `misspell`, `nakedret`, `prealloc`, `staticcheck`, `unconvert`, `unparam`, `unused`. Formatters: `gofmt`, `goimports`.

### Vendored Dependencies

This project vendors its dependencies (`vendor/` directory). Run `go mod tidy` and `go mod vendor` after dependency changes.
