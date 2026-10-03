# scan-persistence — architecture deepening findings

Scope: `internal/store`, `internal/config`, `internal/history`, `internal/memory`, `internal/session`.
Read-only scan. Vocabulary per `codebase-design`: **Module**, **Interface**, **Seam**, **Depth**, **Adapter**, **Locality**, **Leverage**, deletion test.

> Note on sizes: current files exceed the briefing's counts — `store/store.go` 287 (not 205), `store/message.go` 445 (not 373), `store/memory.go` 509 (not 425), `history/sqlite.go` 296 (not 252), `config/config.go` 576 (not 431). Production lines: store 2298, config 576, history 924, memory 389, session 772; tests: 1313 / 360 / 526 / 330 / 615.

---

## Candidate 1 — Split `internal/config/config.go` along the schema/semantics/secrets seams, and pull default-resolution behind the module

**Recommendation: STRONG.**

**Files + lines:** `internal/config/config.go` (576 lines) — `Config` struct 49-60; section structs 73-258 (~185 lines of field declarations); `Duration` 22-46; `Default()` 261-297; `Load/Parse` 300-320; `expandSecrets/expandSecret` 322-365; `Problem/ValidationError` 367-398; `Validate` 401-536 (135 lines); `Redact/Redacted/RedactedYAML` 538-575. Test surface: `internal/config/config_test.go` (360 lines).

**Problem (concrete cost).** One file holds five responsibilities: schema (structs), defaulting, parsing/env expansion, validation, redaction. The module's Interface is the `*Config` value itself — ~40 exported fields callers must each learn, including nil-vs-zero convention and units. It answers no questions, so default resolution leaks to every caller: `cmd/server/main.go` defines `stringOr` (411), `int64Or` (578), `intOr` (592), `boolOr` (599), `durationOr` (606), each reaching into the pointer and `.D` field (`.D` appears at 22 sites repo-wide). Adding one option touches 4-7 places: section field, `Default()`, the matching `Validate()` branch, plus `expandSecrets` 323-343 and `Redacted` 551-565 if it is a secret, plus main.go. Example trace: `AmbientMaxChars` appears at config.go:231 (field), 479 (validate), main.go:995 (consume), plus `conversation` — 4-5 touchpoints with no single table saying "this option exists".

**Solution.** Keep `Load/Parse/Validate/RedactedYAML` unchanged as the external Seam (all existing tests cross it). Internally: (a) `schema.go` = structs + `Duration`; (b) one defaults table + effective-value accessors (`Duration.Or(fallback)`, `Int.Or`) so main.go's five helpers shrink to calls on the field; (c) `validate.go` = `Validate` iterating a table of `(path, getter, rule)`, replacing the 135-line switch chain; (d) `secrets.go` = one table driving both `expandSecrets` and `Redacted` (today a 4th secret must be added to two separate hard-coded lists). Decision 6 in `.scratch/agentbot/spec.md`:26 mandates `*T` for unset-vs-zero — accessors must read pointers, not replace them.

**Benefits.** Locality: adding an option has known touchpoints in one table. Leverage: callers ask `cfg.LLM.Timeout.Or(30*time.Second)` instead of re-deriving. Testability: each validation rule and the secret list become individually addressable; redaction coverage stops depending on remembering two lists.

**Before/after.** Before: one 576-line Module whose Interface is a 40-field struct, semantics split across four methods in one file, defaults re-derived outside. After: same public Interface; defaults, validation, and secret handling each behind a small internal table/seam.

**ADR conflicts:** none. ADR-0002/0003 are unrelated (memory injection position; persistence layering).

---

## Candidate 2 — Retire the JSONL `History` adapters; keep only the JSONL record format

**Recommendation: WORTH EXPLORING.**

**Files + lines:** `internal/history/history.go` 70-75 (`History` iface), 80-82 (`Trimmer`), 85-173 (`Window`/`HighWater`); `history/memory.go` 13-130; `history/file.go` 16-244; `history/sqlite.go` 33-294; wiring `cmd/server/main.go`:919-923.

**Problem (deletion test).** The `History` Seam has three Adapters (`Memory`, `File`, `SQLite`) but production wires exactly one: `history.NewSQLite` at main.go:919 then `var hist history.History = sqliteHist` (923). Grep evidence: `NewMemory(` has no non-test caller (history_test.go:22,37,113,140,164,202,216,247,290; session_test.go:197); `NewFile(` is used only as a JSONL *fixture writer* (sqlite_test.go:125) and by agent tests (memory_scope_test.go:123,129,148,178,194). Delete `history.Memory`: production behaviour is unchanged and the complexity does not reappear across callers — it was a pass-through. `history.File` is nearly as cheap to retire, except `SQLite.ImportJSONL` reuses `fileRecord` defined at file.go:24 (comment sqlite.go:212; use at sqlite.go:241) and F-84's idempotency test builds a real fixture with `NewFile` (sqlite_test.go:124-137). So `file.go`'s record/writer is load-bearing for the import path even though its `History` adapter is not.

**Solution.** Extract `fileRecord` + `writeRecord` into a small `jsonl.go` (the *format* module). Keep `sqlite.go` as the only `History` Adapter in production. Either delete `history.Memory`/`history.File` or demote them to a test fake in `testutil` (the repo's fake-first convention). Capability required by ADR-0003 for troubleshooting is "export JSONL", not "a live JSONL History".

**Benefits.** Locality: one production Adapter to keep correct against F-84/F-86/F-89. Leverage: the `History` Interface stops advertising three Adapters when only one varies. Tests: import-format tests can run without a full storage implementation.

**Before/after.** Before: History Seam with 3 Adapters, 2 never chosen in production; SQLite's importer type-coupled to `File`'s struct. After: History Seam with 1 production Adapter; JSONL reduced to a record format used by importer + tests.

**ADR conflict callout:** ADR-0003:31-32,69-70 says files "degrade to import/export format, no longer primary storage"; `.scratch/agentbot/spec.md`:155-156 says JSONL implementations are kept "until M3 wraps up, then decide whether to remove". This is that decision becoming due — removing the Adapters while preserving the format stays *inside* ADR-0003. Do not touch the SQLite choice itself.

---

## Candidate 3 — Narrow `store.Store`'s public surface and make the table registry declarative (do NOT re-layer SQLite)

**Recommendation: WORTH EXPLORING.** Explicit negative finding: `Store` is a **deep Module**, not a shallow facade.

**Files + lines:** `store/store.go` 28-283; `store/schema.go` 7-173; `store/migrate.go` 32-59, 184-213; per-table files `message.go` 445, `memory.go` 509, `prompt.go` 230, `usage.go` 194, `pending.go` 175.

**Depth verdict (evidence).** `Write` (store.go:177-206) hides single-writer serialization, jittered retry (`retryDelay` 51-63), retry-safe transaction (`writeOnce` 208-222) and periodic PASSIVE checkpoint (`afterWrite` 225-236). SQL does **not** leak past the Seam: `QueryContext|QueryRowContext|ExecContext|BeginTx` has 0 occurrences outside `internal/store`. `DB()` (store.go:159) and `Read()` (store.go:239) expose `*sql.DB` but have **zero external callers** — an open hole around the single-writer discipline with no caller. Do not re-layer this; the per-table files are cohesive.

**Problem (surface + registry churn).** `*store.Store` carries **40 exported methods**; 6 have zero external callers: `DeleteMessage` (message.go:193), `CountDivergedSnapshots` (prompt.go:198), `CountPending` (pending.go:167), `ListPromptSnapshots` (prompt.go:167), `ResetUsage` (usage.go:185), `SaveMemory` (memory.go:104, superseded by `SaveMemoryWith`). Wide Interface with little added Leverage. Table registry is spread: five schema slices in `schema.go` are assembled by chained `append(append(...))` at `migrate.go`:32-38, columns likewise at 51-53 — adding one table edits `schema.go` **and** the assembly expression, which bends further with each table. `Options` also publishes test-only fields `schema/migrations/columns/schemaVersion` (store.go:92-96), widening the public Interface for a test Seam.

**Solution.** Keep `Write`/`Read` as the Seam. (a) One `tableDef{createSQL, indexes, columns}` slice in `schema.go`; `migrate.go` iterates it — one place to add a table. (b) Move test-only fields off `Options` to a package-internal `openForTest` (same-package tests can set unexported fields on the returned `*Store`). (c) Remove the zero-caller methods; if `DB()`/`Read()` must stay, they need a stated invariant, otherwise they invite bypassing `Write`.

**Benefits.** Locality: table additions stop editing two files; migration invariants visible in one table. Leverage: smaller Interface for every caller/test. Testability: iterate `tableDef` instead of opening a Store with injected `Options`.

**Before/after.** Before: deep `Write` Seam, wide partly-unused Interface, schema split across schema.go + nested appends. After: same deep Seam, narrower public surface, one declarative table registry.

**Must keep green (from `.scratch/agentbot/issues`):** F-83 (45) migration idempotent, declarative column-add idempotent, `Store.Write` fn retry-safe/pure — no side effects; F-85 (47) `UPDATE ... SET x = x + ?` atomic accumulate, COALESCE on empty tables; F-89 (51) "read previous + write current" in one transaction, ring prune keeps newest; F-86 (50) records retained (status change, not delete), same-id upsert idempotent.

**ADR conflict:** none. This is file organisation inside ADR-0003's SQLite layer; it preserves the single-writer/short-busy-timeout/migration discipline argued at ADR-0003:44-53.

---

## Candidate 4 — Collapse three pending record shapes to one at the store Seam

**Recommendation: SPECULATIVE.**

**Files + lines:** `session/temp.go` 47-54 (`PendingMeta`), 56-62 (`PendingStore`), 64-72 (`PendingRecord`), 119-157 (`Register`), 159-168 (`FinishPending`), 174-227 (`Offer`), 230-251 (`RemoveKey`); `store/pending.go` 18-31 (`Pending`), 34-39 (status constants), 42-72 (`UpsertPending`); adapter `cmd/server/main.go` 284-295.

**Problem (concrete cost).** The Seam is well-placed — `session.PendingStore` is a 2-method Interface (`SavePending`/`FinishPending`) so the session layer does not know SQL, and it has two Adapters (main.go:284 and `temp_test.go`:281 fake), so it is a real Seam. The cost is triplication: `PendingMeta` (ID, Kind, Payload), `PendingRecord` (those + SessionKey, CreatedAt, ExpiresAt) and `store.Pending` (those + Status, Note) carry the same data; main.go:286-291 maps field-by-field. Adding one attribute touches three types, the adapter, and the `pending` SQL. Status vocabulary also exists twice: store constants `PendingStatus*` (pending.go:34-39) vs bare literals `"done"`/`"expired"`/`"orphaned"` at temp.go:151,200,244.

**Solution.** Let the store own the record shape and a constructor; session keeps only `PendingMeta` as the caller-facing descriptor and the 2-method `PendingStore` Interface (direction of dependency unchanged). Share the status constants through the Interface or a small enum.

**Benefits.** Locality: one definition of a pending row and its states. Leverage: an added attribute is one struct + one SQL column. Tests: idempotent same-id upsert and orphan/expire transitions assertable without a mapping layer.

**Before/after.** Before: one logical record expressed three times plus a mapping Adapter and duplicated status names. After: one record type, one status vocabulary, same 2-method Seam.

**Must keep green (F-86, issue 50):** record retained after completion (status change, not delete); same-id upsert idempotent (pending.go:59-65); persistence failure must not block registration but must warn (temp.go:141-143); expired/orphaned transitions (temp.go:200,244); F-45 approval wait must not consume the step timeout.

**ADR conflict:** none (ADR-0002 is memory-injection position; unrelated).

---

## Top recommendation for this slice

**Candidate 1 — split `internal/config/config.go` and move default-resolution behind the module.** It is the only finding with a clean, self-contained Interface that can stay byte-identical while the implementation is reorganised; it directly answers the "adding one option touches many places" test (4-7 touchpoints today); it removes the largest concentration of unrelated responsibilities in the slice; and it has zero ADR friction. Second priority: Candidate 2 (delete the unused JSONL History Adapter) — cheapest measurable surface reduction with the M3 retention decision now due.

**Rejected/reassuring:** `store.Store` is deep — do not re-layer it; `history.SQLite` is a justified thin Adapter (deleting it would push `store.Message`↔`history.Item` mapping into session/agent/tool callers); `memory.Store` adds a real privacy Seam over `tool.ScopeFrom(ctx)` so its pass-through methods earn their keep.
