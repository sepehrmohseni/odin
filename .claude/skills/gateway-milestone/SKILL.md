---
name: gateway-milestone
description: Advance Odin's gateway roadmap by one milestone toward Apache APISIX / Kong / NGINX parity (gap analysis in docs/COMPARISON.md, then build the first unfinished milestone in ROADMAP.md). Use when asked to "run the gateway-milestone skill", advance the gateway roadmap, or work milestones M0-M4.
---

# Advance the gateway roadmap by one milestone

Goal: grow Odin step by step into a gateway that can stand next to Apache APISIX, Kong and F5
NGINX in capability, reliability and polish, with a docs site, real releases and demo setups.
Each run moves one milestone forward.

## Ground rules

1. Read `~/.claude/CLAUDE.md`, the repo's `CLAUDE.md`, `docs/AUDIT.md` and `ROADMAP.md` first,
   and follow them. If any of the repo files is missing, stop and say so.
2. One logical change per commit, Conventional Commits, `scripts/docker-check.sh` green before
   every commit, push after every commit, no force-push, **no AI attribution** (no
   Co-Authored-By or "Generated with"; check `git log -1 --format=%B`).
3. Depth over breadth. A feature is done only with an implementation, unit and integration
   tests, a config example, a docs page and, on the request path, a benchmark of its overhead.
   No stubs, placeholder flags or half-features.
4. Learn from the reference gateways' docs, designs and public behaviour; don't copy their code.
   Anything adapted from an Apache-2.0 project keeps its notice and is recorded in `NOTICE`.
5. Public claims must be true: the comparison, README and site list only what works today;
   everything else is "planned". Benchmarks are published only with hardware, config, tool and
   exact command.
6. Ask the owner before: breaking config or API changes (if approved, ship a migration path and
   a CHANGELOG note), enabling GitHub Pages, the first release tag, the first public image, or
   anything that costs money. Prepare everything, then ask; meanwhile continue with work that
   does not depend on the answer.
7. Check the current docs of APISIX, Kong and NGINX when comparing (web search or fetch); don't
   rely on memory.

## Step 1: gap analysis (first run; refresh later)

Re-read the code with a gateway lens and write `docs/COMPARISON.md`: a matrix of Odin vs APISIX,
Kong (OSS) and NGINX, each row done / partial / missing for Odin, with `file:line` evidence for
every "done". Rows:

- Proxy core: host/path/method/header routing, upstreams, load balancing (round robin,
  weighted, least-conn, consistent hash), active and passive health checks, timeouts, retries,
  circuit breaking, HTTP/1.1, HTTP/2, gRPC, WebSocket, TLS termination with SNI, upstream mTLS,
  HTTP/3.
- Traffic control: local and distributed rate limiting, quotas, request size limits,
  weighted/canary splits, traffic mirroring.
- Security: key-auth, JWT, OAuth2/OIDC, basic auth, client mTLS, IP allow/deny, CORS, WAF
  hooks, an Admin API secure by default.
- Transformations: header/body/URL rewrite, response caching, compression.
- Observability: structured access logs, Prometheus, OpenTelemetry, request IDs.
- Extensibility: plugin interface with ordered phases, per-plugin config schema, priority,
  global/service/route scope; external or Wasm plugins later.
- Control plane: declarative config (DB-less), Admin API with OpenAPI spec, schema validation,
  hot reload without dropping connections, pluggable config store, CP/DP split later.
- Platform: Helm chart, Kubernetes Ingress and Gateway API, service discovery (DNS, Consul,
  Kubernetes).
- DX: CLI, examples, dashboard UI later.

Turn the gaps into milestones in `ROADMAP.md`, in this default order adjusted to what exists:

- **M0 Foundations**: plugin pipeline, config schema and validation, declarative config, secured
  Admin API, hot reload, metrics and structured logs.
- **M1 Showcase**: docs site on GitHub Pages (MkDocs Material, Docusaurus or Starlight; config
  reference generated from the schema, Admin API reference from the OpenAPI spec, Mermaid
  diagrams, broken-link check), release pipeline (GoReleaser, multi-arch GHCR images signed with
  cosign, SBOM and provenance, Helm chart as an OCI artifact), `examples/` Compose demos with CI
  smoke tests (quickstart, observability, auth, canary), `bench/` harness with a regression job.
- **M2 Traffic and security essentials**: LB algorithms, health checks, retries and circuit
  breaking, rate limiting, key-auth, JWT, CORS, IP restriction.
- **M3 Kubernetes**: Helm chart, then Gateway API validated with the upstream conformance tests.
- **M4 Advanced**: OIDC, distributed rate limiting, caching, mirroring, external/Wasm plugins,
  CP/DP split, HTTP/3, dashboard.

Commit: `docs: add gateway comparison and milestone roadmap`.

## Step 2: build the current milestone

Take the first unfinished milestone in `ROADMAP.md`. For each item, in dependency order:

1. a short design note in `docs/design/` for anything non-trivial;
2. failing tests;
3. the implementation;
4. an integration test through the real proxy in Docker (`test/unit/pkg/gateway` style, plus the
   Compose stack where it matters);
5. a benchmark if it is on the request path;
6. a docs page;
7. update `docs/COMPARISON.md` and `ROADMAP.md`;
8. commit and push.

Finish at most one milestone per run and keep the tree green at every commit. Keep `CLAUDE.md`
and `.claude/skills` current with any new workflow (writing a plugin, adding a docs page, cutting
a release, running benchmarks).

## Project hygiene (first run, then maintain)

README badges (CI, release, image, docs, license, OpenSSF Scorecard) linking into the docs;
CONTRIBUTING, CODE_OF_CONDUCT, SECURITY with a private disclosure path; issue and PR templates;
Dependabot or Renovate; CodeQL; OpenSSF Scorecard workflow; fuzz tests for routing rules, config
parsing and header handling.

## End-of-run report

Milestone worked on and its status; commits pushed (hash and message); what moved from missing
or partial to done in COMPARISON.md; benchmark results against the previous run; decisions
waiting on the owner (rule 6); the recommended next milestone.
