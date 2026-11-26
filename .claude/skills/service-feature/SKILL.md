---
name: service-feature
description: Add or change a per-service gateway option in Odin (a new services[].x setting that affects proxying, auth, limits, caching or headers). Use when the request path or a service's YAML config gains behaviour.
---

# Add a per-service feature

Odin has two service types: `config.ServiceConfig` (YAML) and `service.Config` (runtime), copied
field by field in `pkg/gateway/gateway.go`. A field that is not copied silently does nothing.

1. **Test first.** In `test/unit/pkg/gateway/` (helpers in `request_path_test.go`):
   ```go
   be := newBackend(t, func(w http.ResponseWriter, r *http.Request) { ... })
   h := newGateway(t, &config.Config{Services: []config.ServiceConfig{{
       Name: "svc", BasePath: "/svc", Targets: []string{be.URL}, /* new option */ }}})
   rec := serve(h, "GET", "/svc/x", nil, nil)
   ```
   For YAML parsing use `startGateway(t, loadConfig(t, yamlText))` (see `features_test.go`). Run it
   and watch it fail: `scripts/docker-check.sh -- go test -run TestName ./test/unit/pkg/gateway/`.
2. **Schema**: add the field to `config.ServiceConfig` in `pkg/config/config.go` with a yaml tag
   and a doc comment saying what it does and its default.
3. **Runtime**: add the matching field to `service.Config` (`pkg/service/service.go`), copy it in
   the service loop of `gateway.New`, and apply defaults there (see `circuitSettings`,
   `serviceRateLimit`).
4. **Behaviour**: request-path logic goes in `pkg/routing/handler.go` (per attempt or response)
   or as group middleware in `pkg/routing/router.go`. Order there is: service label, auth, rate
   limit, cache, handler. Nothing may answer before auth.
5. **Docs**: document the option in `docs/configuration.md` with a YAML example; add it to the
   README feature list only once it works end to end.
6. `scripts/docker-check.sh`, then commit (`feat: ...`, no AI attribution) and push.
