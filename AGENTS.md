# AGENTS.md — Hyperledger Fabric

## Repo structure

- **Single Go module** `github.com/hyperledger/fabric` at root + a **separate module** `github.com/hyperledger/fabric/ccaas_builder` (go 1.16) in `ccaas_builder/`.
- **Entry points** (binaries): `cmd/orderer`, `cmd/peer`, `cmd/configtxgen`, `cmd/configtxlator`, `cmd/cryptogen`, `cmd/discover`, `cmd/ledgerutil`, `cmd/osnadmin`, `cmd/dbmigrator`.
- **Key internal packages**: `core/` (ledger, chaincode, container, endorser), `orderer/` (consensus/raft), `gossip/`, `discovery/`, `internal/` (command implementations).
- **Vendored dependencies** (`vendor/`). When adding deps: `go mod tidy && go mod vendor && git diff --exit-code`.
- **Docker namespace**: `hyperledger` (override via `DOCKER_NS`). Images: `fabric-peer`, `fabric-orderer`, `fabric-baseos`, `fabric-ccenv`.

## Commands

```makefile
# Full build + non-integration tests
make all

# Build native binaries only (peer, orderer, tools)
make native

# Single binary
make peer
make orderer

# Lint + format checks
make linter            # goimports, gofumpt, go vet, staticcheck
make basic-checks      # linter + license, spelling, trailing-spaces, references, swagger, metrics-docs, help-docs

# Unit tests (excludes integration/)
make unit-test

# Unit tests on only changed packages (verify)
make verify

# Single package unit test
go test -short -tags "$GO_TAGS" ./core/chaincode/... -timeout=20m

# Integration tests (Ginkgo-based, requires Docker + SoftHSM)
make integration-test INTEGRATION_TEST_SUITE="raft"
# Suites: raft, pvtdata, pvtdatapurge, e2e, ledger, lifecycle, smartbft, discovery gossip devmode pluggable, gateway idemix pkcs11 configtx configtxlator, sbe nwo msp

# Integration test prereqs (builds Docker images needed)
make integration-test-prereqs

# Proto generation
make protos

# Docs (Sphinx via tox in Docker)
make docs
```

## Linting & style

- **Formatter**: `gofumpt` (not vanilla `gofmt`). Run `gofumpt -l -w <file>` to fix.
- **Imports**: `goimports`. Fix with `goimports -l -w <file>`.
- **Staticcheck**: configured in `staticcheck.conf`. Disabled: SA1019, ST1000, ST1003, ST1016, ST1020-1022, ST1005, U1000.
- **No** `golang.org/x/net/context` — use stdlib `context`.
- **No** `github.com/gogo/protobuf` — use `google.golang.org/protobuf`.
- **License**: Every `.go` file must have `SPDX-License-Identifier: Apache-2.0`.

## Testing

- **Unit tests**: standard `go test` with `-short` flag. `gossip/...` runs serially (`-p 1`). `gossip/...` is **conditional** — only tested when packages or their deps change.
- **Integration tests**: Ginkgo v2 + Gomega. Use `. "github.com/onsi/ginkgo/v2"` and `. "github.com/onsi/gomega"` dot imports. Run via `ginkgo` binary, not `go test`.
- **PKCS11**: package `internal/peer/common` needs `-tags pkcs11` and SoftHSM installed (`ci/scripts/setup_hsm.sh`).
- **Race detector**: enabled on `x86_64` only (`-race` flag).
- **Profile**: `make profile` runs all unit tests with coverage, outputting `profile.cov` + `report.xml`.
- **Testify** is also available (`github.com/stretchr/testify`).

## CI

- CI runs `make basic-checks` first, then unit tests, then integration tests (matrix of suites).
- Workflow: `.github/workflows/verify-build.yml`. Also runs `go mod tidy` + `go mod vendor` check.
- PRs need `Signed-off-by` in commits (DCO). PR template recommends `make checks` locally.

## Quirks

- `Makefile` sets version via `FABRIC_VER=3.1.5` (edit there to change).
- Metadata (version, commit SHA) injected via `-ldflags -X` at build time.
- `build/` is the build output directory.
- `ccaas_builder/` is a standalone Go module with its own `go.mod` — treat separately.
- Integration tests start a build server (`nwo.BuildServer`) that compiles Fabric binaries for the test network.
- `GOPATH` expected at `/opt/go` in CI.

## Coding principles (Karpathy Skills)

Behavioral guidelines to reduce common LLM coding mistakes. Merge with project-specific instructions as needed.

**Tradeoff:** These guidelines bias toward caution over speed. For trivial tasks, use judgment.

### 1. Think Before Coding

**Don't assume. Don't hide confusion. Surface tradeoffs.**

Before implementing:
- State your assumptions explicitly. If uncertain, ask.
- If multiple interpretations exist, present them - don't pick silently.
- If a simpler approach exists, say so. Push back when warranted.
- If something is unclear, stop. Name what's confusing. Ask.

### 2. Simplicity First

**Minimum code that solves the problem. Nothing speculative.**

- No features beyond what was asked.
- No abstractions for single-use code.
- No "flexibility" or "configurability" that wasn't requested.
- No error handling for impossible scenarios.
- If you write 200 lines and it could be 50, rewrite it.

Ask yourself: "Would a senior engineer say this is overcomplicated?" If yes, simplify.

### 3. Surgical Changes

**Touch only what you must. Clean up only your own mess.**

When editing existing code:
- Don't "improve" adjacent code, comments, or formatting.
- Don't refactor things that aren't broken.
- Match existing style, even if you'd do it differently.
- If you notice unrelated dead code, mention it - don't delete it.

When your changes create orphans:
- Remove imports/variables/functions that YOUR changes made unused.
- Don't remove pre-existing dead code unless asked.

The test: Every changed line should trace directly to the user's request.

### 4. Goal-Driven Execution

**Define success criteria. Loop until verified.**

Transform tasks into verifiable goals:
- "Add validation" → "Write tests for invalid inputs, then make them pass"
- "Fix the bug" → "Write a test that reproduces it, then make it pass"
- "Refactor X" → "Ensure tests pass before and after"

For multi-step tasks, state a brief plan:
```
1. [Step] → verify: [check]
2. [Step] → verify: [check]
3. [Step] → verify: [check]
```

Strong success criteria let you loop independently. Weak criteria ("make it work") require constant clarification.

---

**These guidelines are working if:** fewer unnecessary changes in diffs, fewer rewrites due to overcomplication, and clarifying questions come before implementation rather than after mistakes.

---

## Agent skills

### Issue tracker

Issues and specs live as markdown files under `.scratch/<feature>/`. See `docs/agents/issue-tracker.md`.

### Triage labels

Default five-role vocabulary: needs-triage, needs-info, ready-for-agent, ready-for-human, wontfix. See `docs/agents/triage-labels.md`.

### Domain docs

Single-context: one `CONTEXT.md` + `docs/adr/` at the repo root. See `docs/agents/domain.md`.
