# task-5 / C6 — store & history dead-surface cleanup + declarative schema

Teammate: fix-deadcode. Scope: `internal/store`, `internal/history` only.

## What changed

### 1. Removed dead exported *Store methods (zero callers, including tests)

| Symbol | File | Evidence |
| --- | --- | --- |
| `(*Store).ResetUsage` | `internal/store/usage.go` | repo-wide grep: 0 hits at all (no production, no test) |
| `(*Store).DB()` | `internal/store/store.go` | repo-wide grep of `.DB()`: 0 hits |
| `(*Store).Read(ctx, fn)` | `internal/store/store.go` | repo-wide grep of `.Read(ctx`: only `internal/transport/ws.go:314` (`conn.Read`, websocket, unrelated) |

Tests in package `store` use the unexported `s.db` field directly (e.g. `store_test.go:47,95,151`), so removing the exported `*sql.DB` wrappers broke nothing. `*sql.DB` is still exposed internally at `s.db` and is still needed by the package.

### 2. Kept methods — each has a caller

| Method | Verdict | Why |
| --- | --- | --- |
| `SaveMemory` (memory.go) | KEEP | **Has a production caller**: `ImportMemoriesJSONL` at `internal/store/memory.go:429` calls `s.SaveMemory(...)`. Not dead. |
| `DeleteMessage` (message.go:193) | KEEP | Test-only callers, but `internal/store/usage_test.go:162` uses it as a deliberate seam for `Test_F85_MessageCountComesFromTableNotCounter` — it deletes a row to prove `SessionUsage.MessageCount` is read live from `messages`, not from a counter. Its own test is `message_test.go:195-202`. |
| `CountDivergedSnapshots` (prompt.go:198) | KEEP | Test-only: `prompt_test.go:42,121`. Used as an observation seam for `RecordPromptSnapshot` persistence, including the regression that `memory_changed` must not be counted as diverged. Kept by judgement. |
| `CountPending` (pending.go:167) | KEEP | Test-only: `pending_test.go:58,81,105`. Seam for `UpsertPending` idempotency, `ExpirePending`, and `PrunePending` (live record must survive pruning). Kept by judgement. |
| `ListPromptSnapshots` (prompt.go:167) | KEEP | Test-only: `prompt_test.go:75`. Seam for `PrunePromptSnapshots` ring-buffer retention (only way to observe which rows survived). Kept by judgement. |
| `history.File`, `history.Memory` | KEEP | Lead decision (message team-message-47d8c0a8). Both are the second/third adapter at the `History` seam; production wires only `NewSQLite` (`cmd/server/main.go:919`). Kept by design; `File` additionally has test callers in `internal/agent/memory_scope_test.go` (outside my scope). |

### 3. Declarative table/column registry (`internal/store`)

`schema.go` is now the single source of truth:

- `tableDef{CreateSQL, Indexes, Attached, Columns}`; `Attached` holds the FTS virtual table and the three sync triggers.
- `tables []tableDef` registers messages / sessions / memories / pending / prompt_snapshots in build order.
- `schemaVersionTableSQL` const; `schemaStatements()` flattens the registry; `declaredColumns()` aggregates declared columns.

`migrate.go` now derives everything from that registry:

- `var schemaSQL = append([]string{schemaVersionTableSQL}, schemaStatements()...)` (replaces the chained `append(append(...))` at old lines 32-38).
- `var desiredColumns = declaredColumns()` (replaces the chained assembly at old lines 51-53).
- `ensureVersionTable` reuses `schemaVersionTableSQL` instead of a duplicated inline CREATE.

Preserved exactly: idempotent `CREATE ... IF NOT EXISTS`; declared-column reconciliation (`reconcileColumns`); the version-gated `migrations` chain (kept global because it is version-ordered, not per-table) and `SchemaVersion = 1`; `Options.schema/migrations/columns/schemaVersion` test injection semantics. The `Write` retry path, single-writer mutex, WAL/checkpoint, and ADR-0003 layering are untouched.

### 4. JSONL format extracted (`internal/history`)

- New `internal/history/jsonl.go`: `fileRecord`, `marshalRecord`, `writeRecord`, `readRecord`.
- `file.go`: `fileRecord` and `writeRecord` removed; `Append`/`readAllLocked` use the helpers; unused `encoding/json` import dropped. `File` still implements `History`.
- `sqlite.go`: `ImportJSONL` now parses via `readRecord` and no longer references `fileRecord` as a File-owned type; comment updated. `NewSQLite` remains the only production `History`.

## Exact commands and results

- `gofmt -l internal/store internal/history` -> empty output (clean).
- `go test -count=1 ./internal/store/... ./internal/history/...` -> exit 0; `ok .../internal/store 0.234s`, `ok .../internal/history 0.095s`.
- `go vet ./internal/store/... ./internal/history/...` -> exit 0.
- Dead-symbol sweep after edits: `ResetUsage` 0 hits, `.DB()` 0 hits, `.Read(ctx` 1 hit (unrelated `conn.Read`).

## Deliberately NOT changed

- `cmd/server/`, `internal/conversation/`, `internal/agent/` (incl. `memory.go`): owned by the Lead / other teammates. `history.NewFile` call sites in `internal/agent/memory_scope_test.go` were left untouched.
- The `migrations` slice was NOT moved into `tableDef`: migrations are a version-ordered global chain and currently empty; nesting them per table would require re-sorting and would not preserve the version gate.
- The `History` interface, `Searcher`, `Trimmer`, `Memory`, `File`, `SQLite` public surfaces: unchanged.
- `msg`/SQL text and column definitions were copied verbatim into the registry; no SQL semantics changed.

## Notes for the Lead

- `DeleteMessage`, `CountDivergedSnapshots`, `CountPending`, `ListPromptSnapshots` remain test-only exported surface. If the review wants them gone, the prompt/pending/message tests need rewriting and that crosses into test seams; I kept them and documented the exact call sites above.
- `history.File` must stay until `internal/agent/memory_scope_test.go` stops using it; per your decision it stays as a seam adapter.