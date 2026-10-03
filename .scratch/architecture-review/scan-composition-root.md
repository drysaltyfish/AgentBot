# Scan: composition root (`cmd/server/main.go`)

Read-only scan of `cmd/server/main.go` (1581 lines, the only file in `package main`, no `_test.go` anywhere under `cmd/`). Churn confirmed: `cmd/server/main.go` appears in ~20 of the last 30 commits (`git log --oneline -30 --name-only`).

## 1. Responsibility inventory (line ranges)

| Lines | Responsibility |
|---|---|
| 1–38 | package doc, imports |
| 41 | `replyWorkers` constant (worker pool size) |
| 43–45 | `main()` → `os.Exit(run(...))` |
| 47–88 | `run`: CLI flag set, config load/validate, subcommand dispatch |
| 90–127 | `runExportMemories`: maintenance subcommand (open store, export JSONL) |
| 129–173 | `runStats`: maintenance subcommand (usage ledger, top sessions) |
| 175–194 | `callerBox`: late-bound `transport.Caller` adapter |
| 196–279 | `quotedResolver`/`quotedEntry`/`quotedCacheTTL`: quoted-message resolution + bounded TTL cache |
| 281–295 | `pendingStoreAdapter`: `*store.Store` → `session.PendingStore` adapter |
| 297–317 | `pendingSessionTarget`: session key → `outbound.Target` |
| 319–395 | `recoverPending`: restart recovery of pending records |
| 397–423 | `shutdownTimeout`, `queueSize`, `stringOr`, `configuredSelfID` |
| 425–456 | `openAIDefaultBase`, `buildLLM` |
| 458–487 | `buildJudgeLLM` (thinking-off judge client) |
| 489–502 | `baseURL` (provider default endpoint) |
| 504–524 | `systemPrompt` |
| 526–532 | `historyTurns` |
| 534–564 | `replyRule`: config → `router.Rule` |
| 566–571 | `llmTimeout` |
| 573–576 | `promptSnapshotKeep` |
| 578–611 | `int64Or`/`floatOr`/`intOr`/`boolOr`/`durationOr` |
| 613–635 | `groupScopedUserID`, `speakerDisplayName` (identity mapping) |
| 637–650 | `agentRole`: platform role → `agent.Role` |
| 652–780 | `buildAgent`: model/judge/memory/tools/gate/ReAct assembly |
| 782–840 | `replyPipeline`, `sendShape`, `sendShapeOf`, `replyJob` |
| 842–850 | `namedComponent`: closure → `bot.Component` adapter |
| 852–1282 | `serve`: full runtime wiring (logger, LLM, store, history, sessions, router, transport, outbound, bot, workers, sink, signal, shutdown) |
| 1284–1492 | `handleReply`: the reply domain logic |
| 1494–1504 | `toolNames` |
| 1506–1539 | `segmentDetail`, `segmentTypes` (log rendering) |
| 1541–1544 | `traceID` |
| 1546–1581 | `runSelfTest` |

Local types/functions that are domain logic or adapters rather than wiring: `quotedResolver`, `pendingStoreAdapter`, `pendingSessionTarget`, `recoverPending`, `replyPipeline`/`replyJob`/`sendShape`, `sendShapeOf`, `replyRule`, `buildLLM`/`buildJudgeLLM`/`baseURL`, `agentRole`, `groupScopedUserID`, `speakerDisplayName`, `namedComponent`, `callerBox`.

## 2. What makes behaviour hard to test through the current interface

- `cmd/server` has **no test surface at all**: `cmd/server/main.go` is the only file under `cmd/`; `run(args, stdout, stderr) int` is parameterized, but every subcommand body opens a real SQLite file (`store.Open` at 98, 136, 883) or a real websocket (`runSelfTest` 1551–1562), and `serve` blocks on `<-sig` (1265).
- The reply chain is reachable only by running the binary against a live platform. Ticket 41's own comment records that the M2 acceptance contract still cannot run end to end because NapCat is stopped.
- `replyPipeline` (782–798) is a field bag with zero methods — a shallow parameter object. Its invariants (append user turn before deciding; `ErrEndOfTurn` sends nothing; auto-memory before `Run`; usage keyed by `session.Key`) have no interface to test across.
- Defaults are duplicated: `config.Default()` (config.go 261–297) sets `splitDelay` 400 ms, `maxSegments` 4, `historyTurns` 20, `ReplyAlways`/`ReplyOnMention`; main re-hardcodes all of them (`sendShapeOf` 808–820, `historyTurns` 527–532, `replyRule` 538–564). Drift is silent.

## 3. Candidates

### C1 (Strong — top recommendation) · Extract the reply chain as `internal/reply`
**Files/lines**: 196–279 (`quotedResolver`), 782–840 (`replyPipeline`/`sendShape`/`replyJob`), 1284–1492 (`handleReply`), 1494–1504 (`toolNames`); call sites 1108–1171 (`serve`).

**Problem**: ~330 lines of the bot's real behaviour live in package main. The module that owns cache-first ordering (ADR-0002), auto-memory, quoted resolution, usage ledger (F-85/ADR-0003), prompt-snapshot judgement (F-89), history append and send shaping has no external interface; its only interface is "the whole binary". Cost: untestable + no locality + churn magnet — nearly every M2/M3 ticket patched main.go here.

**Solution**: new package `internal/reply`. Small external interface:
```go
type Job struct { Key session.Key; GroupID, UserID int64; Text, TraceID string; Role agent.Role; ShouldReply bool; Message event.Message; Caller transport.Caller }
type Pipeline struct{ ... }
func New(deps Deps) *Pipeline
func (p *Pipeline) Handle(ctx context.Context, j Job)
```
`quotedResolver` moves into the package as an unexported internal seam. The worker pool (1152–1171) and the enqueue route (1120–1150) stay in the composition root, which remains the only owner of goroutine lifecycle; it calls `pipeline.Handle`.

**Deletion test**: deleting the package does not remove complexity — every ordering invariant inside `handleReply` reappears at the single call site. It earns its keep. `replyPipeline`/Deps is not a pass-through: it carries behaviour, not just parameters.

**Benefits**: locality (the reply chain and its eight concerns in one file); leverage (one `Handle` for the pool and any future entry point); testability via fake `agent.Agent`, a `session.Manager` over in-memory history, a `transport.Caller` fake behind `outbound.Sender`, and an `observe` logger writing to a buffer. Tests that become possible: turn recorded when `ShouldReply=false`; `ErrEndOfTurn` sends nothing but keeps history; auto-memory written before `Run`; quoted-resolution failure still replies; usage recorded on the right `session.Key`; empty reply sends nothing.

**ADR**: no conflict. Must preserve ADR-0002 (memory position is resolved inside `agent`, not here) and ADR-0003 (usage/pending layering).

### C2 (Worth exploring) · Config→domain construction in `internal/bootstrap`
**Files/lines**: 397–423, 504–571, 578–611, 808–820, 1094–1099, and `buildAgent` 652–780.

**Problem**: `buildLLM`/`buildJudgeLLM` duplicate the same `httpx.NewClient` + `llm.NewOpenAI` + `NewRetryLLM` construction (436–452 vs 470–483); `baseURL` (493–502) encodes provider endpoint knowledge only because `config.Default()` pre-fills OpenAI's URL (config.go 279); defaults duplicate `config.Default()`. None of it is reachable from a test.

**Solution**: `internal/bootstrap` owning `SystemPrompt(cfg)`, `ReplyRule(cfg)`, `SendShape(cfg)`, `LLM(cfg, Mode, lg)`, `JudgeLLM(cfg, lg)`, `Agent(...)`, importing `config`/`router`/`agent`/`llm`/`store`. Keep `config` a leaf (it imports only stdlib + yaml, config.go 6–17) — do not hang `router.Rule` off it.

**Deletion test**: mixed. The `*Or` family (411–423, 578–611) is shallow — deleting it moves nothing, so leave it or fold into config accessors. `LLM`/`JudgeLLM`/`Agent`/`ReplyRule` are deep: their complexity (provider switch, thinking-off judge, registry ordering, gate fail-closed) reappears in main if deleted.

**Strength**: Worth exploring; move the deep group, not the `*Or` helpers.

### C3 (Strong, cheap) · Platform→domain identity into `internal/agent`/`internal/event`
**Files/lines**: 613–622 (`groupScopedUserID`), 624–635 (`speakerDisplayName`), 637–650 (`agentRole`).

**Problem**: pure mapping functions from `event.Event`/`event.Sender` to domain identity and `agent.Role` (F-45), encoding two deliberate decisions (QQ number as anchor, not nickname; unrecognized role → `RoleMember`, fail-closed). Reachable only from the binary, so neither decision has a test.

**Solution**: `agent.RoleFromEvent(*event.Event) agent.Role` and event-side display-name/scope helpers. Small interface, immediate locality.

**Deletion test**: delete them and the role switch plus the QQ-anchor rule re-derive at each caller; they encode policy, not plumbing. Keep.

**Strength**: Strong (small, low risk, compounds with C1/C2 tests).

### C4 (Worth exploring) · CLI/maintenance seams and the file split
**Files/lines**: 43–88 (`main`/`run`), 90–127, 129–173, 1546–1581.

**Problem**: `store.Open` options duplicate at 98–101, 136–139, 883–886 (path + `BusyTimeout` fallback), and `get_login_info` + unmarshal duplicates at 1214–1223 and 1562–1571. Cold paths, but a store-opening change silently misses a subcommand.

**Solution**: extract `openStore(cfg)`; optionally `internal/maintenance` with `ExportMemories(ctx, st, w)`, `Stats(ctx, st, w)`, and a shared `loginInfo(ctx, caller)` in `transport`. **Constraint**: `.golangci.yml` 55–56 exempts only `cmd/` from `forbidigo` (`os.Exit` allowed); code using `os.Exit`/`panic` must stay in `cmd/`, so `internal/maintenance` must return errors/codes.

**Deletion test**: `openStore` earns its keep at three call sites; moving the subcommands to a package is a wash on complexity — this is a **file split**, not a package extraction.

**Strength**: Worth exploring.

## 4. Proposed final layout (approximate line counts)

Split package main first (low risk, no behaviour change), then extract:

```
cmd/server/
  main.go          ~110   main, run, flags, dispatch, constants
  serve.go         ~430   serve wiring
  reply.go         ~220   handleReply, toolNames
  build.go         ~260   buildLLM/buildJudgeLLM/baseURL/systemPrompt/buildAgent
  maintenance.go   ~170   runStats, runExportMemories, runSelfTest
  configmap.go     ~110   *Or + shutdownTimeout/queueSize/replyRule/sendShapeOf
  adapters.go      ~60    callerBox, pendingStoreAdapter, namedComponent
  recover.go       ~80    recoverPending, pendingSessionTarget
  sink.go          ~90    sink body, segmentTypes, segmentDetail, traceID
internal/reply/    ~350 + tests   Pipeline.Handle, quoted resolver (C1)
internal/bootstrap/~200 + tests   config→domain construction (C2)
internal/agent     +~40           RoleFromEvent (C3)
internal/event     +~20           display/scope helpers (C3)
```

After C1+C3, package main holds only process concerns (flags, signal, goroutine lifecycle, shutdown) and thin construction calls.

## 5. Constraints to keep satisfied

- **ADR-0001**: native `tool_calls` is the single execution channel; do not add a second path when moving `buildAgent`.
- **ADR-0002**: memory stays a separate message after system, before history; `internal/reply` must not move that concern.
- **ADR-0003**: single embedded SQLite, tables layered by lifecycle; `pendingStoreAdapter`/`recoverPending` must keep using `store.Pending*`.
- **spec.md**: decision 1 (only `cmd/server` outside `internal/`), decision 16 (single binary), decision 20 (persistence layering).
- **tickets**: 41 (agent wiring — `buildAgent` returns `agent.Agent` so both paths stay same-shaped), 50 (recovery is notification, not resume), 47 (usage accumulation in SQL), 06 (shutdown order owned by `internal/bot`; don't duplicate it in `serve`).
- **lint**: `forbidigo` `os.Exit` exemption is `cmd/`-only (`.golangci.yml` 55–56).

## 6. Top recommendation

**C1: extract `internal/reply` with `Handle(ctx, Job)`, then C3.** It converts the largest untestable block of domain behaviour into a deep module with a one-method interface, owns the churn hotspot, and is the prerequisite for regression-testing the cache-first ordering that ADR-0002/F-85/F-89 depend on. Do the package-main file split first if a zero-risk intermediate step is wanted.
