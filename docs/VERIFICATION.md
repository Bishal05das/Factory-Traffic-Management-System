# Verification record

Verified on 2026-10-08 against the implementation integrated into `pre-release`, for release version `1.0.0`.

| Check | Result |
| --- | --- |
| `go vet ./...` | Passed |
| PostgreSQL-enabled `go test -race ./...` | Passed: domain, repository, and HTTP integration tests |
| Deterministic generated domain operations | 2,000 operations preserved signal invariants |
| Sustained-green starvation regressions | Reproduced on the feature branch, corrected, and passed before integration |
| Controller model tests | Four cases passed: conflict interlock, persistent fence, expiry, compatible replay |
| `npm run check` | Passed, including generated Next.js route types |
| Next.js production build | Passed locally and in Docker |
| Compose configuration and image builds | Passed |
| Compose service readiness | PostgreSQL, Go API, and Next.js healthy; REST simulator running when enabled |
| Final HTTP scenario script with `--restart` | Passed against actual Docker services |
| Browser traffic operations | Arrival, duplicate replay, clearance, manual intent, and automatic return passed |
| Browser error handling | Invalid junction, malformed nested device response, unavailable API, stale controls, and connection recovery passed |
| Responsive rendering | Desktop 1440-pixel and mobile 375-pixel viewports checked; no horizontal overflow or uncaught browser exceptions |
| Documentation | Local links, fenced blocks, required README sections, and absence of the deleted requirements review checked |

The final end-to-end run retained audit evidence at junction `CHECK-9ca221e3`; its vehicles were cleared on completion. Earlier verification junctions retain their histories as well. These test junctions use explicitly shorter timings without changing Junction A's configured 30-second green, 5-second yellow, and 2-second clearance.

The end-to-end check verified normal priority, sensor replay and conflicting event-ID reuse, duplicate controller ACK, emergency preemption, manual resumption and automatic return, clearance/tombstones, offline controller, missing ACK timeout, explicit recovery, concurrent arrivals/commands/feedback, and persisted traffic/intent/deduplication/history after an actual backend container restart.

The verification stack uses dashboard port `13000` and API port `18080` because port 8080 was already allocated. These are local `.env` settings; the committed example retains 3000/8080. PostgreSQL runs on the private Compose network. A separate localhost PostgreSQL container was used for database/API integration tests; the demonstration database was not used for those tests.

Browser checks used headless Chrome and the Chrome DevTools Protocol. Backend/physical behavior remains covered by the Go tests, real database tests, and HTTP scenario script; a screenshot alone is not a safety proof.

Reproduction commands are in [README.md](../README.md) and [DEMO.md](DEMO.md). Hardware guarantees and unimplemented optional features are explicitly bounded in the README and architecture.
