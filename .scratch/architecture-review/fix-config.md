# fix-config — C4: split internal/config/config.go along its seams

Task: task-4. Owner: fix-config. Write scope: internal/config only.

## What changed

Deleted `internal/config/config.go` (575 lines) and split it into focused files in the
same package, plus two new files for resolved defaults and tests:

| file | lines | responsibility |
|---|---|---|
| `schema.go` | 65 | package doc, imports, `ErrInvalid`, `Duration` + YAML/string methods, `Config`, reply-policy consts |
| `sections.go` | 189 | every section struct (`Store` … `Shutdown`) |
| `defaults.go` | 83 | named default constants, `Default()`, `Duration.Or` |
| `effective.go` | 127 | default-resolving accessors (`Effective*` / `*Or`) |
| `load.go` | 32 | `Load`, `Parse` |
| `validate.go` | 180 | `Problem`, `ValidationError`, `Validate` |
| `secrets.go` | 97 | one secret-field table, `expandSecrets`, `expandSecret`, `Redact`, `Redacted`, `RedactedYAML` |
| `defaults_test.go` | 213 | new tests (accessors + secret table); `config_test.go` untouched |

No file in internal/config exceeds 250 lines (`config_test.go` is a pre-existing 359;
it was not touched). `Duration` and its YAML methods live in `schema.go` with the schema.

### External seam
Unchanged: `Load`, `Parse`, `Config`, every section type/field/tag, `Validate`,
`ValidationError`, `Problem`, `Redacted`/`RedactedYAML`, `Redact`, `ErrInvalid`,
`Duration`, reply constants. No signature changed, nothing removed, no other package
needs to change.

### Requirement 4 — one secret table
`secrets.go` now has one `secretFields` table (`path` + `func(*Config) **string`).
`expandSecrets` and `Redacted` both iterate it; `RedactedYAML` calls `Redacted`.
Adding a secret-bearing field is a one-line table edit.

### Requirement 3 — accessors added (for the Lead to adopt in main.go)
Zero-argument accessors resolve the module default (constants in `defaults.go`); the
`*Or(fallback)` forms take the default from the owning package so config never imports it.

- `(*Duration).Or(fallback) time.Duration` — nil **or** <=0 → fallback.
- `Store.BusyTimeoutOr(fallback)`
- `History.EffectiveRetention()` → 400
- `Agent.MaxIterationsOr(fallback)` / `Agent.StepTimeoutOr(fallback)` / `Agent.ApprovalTimeoutOr(fallback)`
- `Agent.EffectiveMemory()` → true, `Agent.EffectiveVirtualActions()` → true, `Agent.EffectiveMemoryMax()` → 64
- `MemoryJudge.EffectiveEnabled()` → true, `ProactiveMemory.EffectiveEnabled()` → true, `ToolHint.EffectiveEnabled()` → true
- `Behavior.EffectivePrivate()` → always, `Behavior.EffectiveGroup()` → on_mention
- `Behavior.EffectiveSplitOnBlankLine()` → true, `Behavior.EffectiveSplitDelay()` → 400ms, `Behavior.EffectiveMaxSegments()` → 4
- `Transport.EffectiveSelfID()` → 0
- `LLM.EffectiveTimeout()` → 30s, `LLM.EffectiveHistoryTurns()` → 20, `LLM.AmbientTokenBudgetOr(fallback)`, `LLM.AmbientMaxCharsOr(fallback)`
- `Log.EffectiveQueueSize()` → 1024, `Shutdown.EffectiveTimeout()` → 10s

Adoption map for cmd/server/main.go (Lead's step):
`durationOr(cfg.Store.BusyTimeout, store.DefaultBusyTimeout)` → `cfg.Store.BusyTimeoutOr(store.DefaultBusyTimeout)`;
`shutdownTimeout(cfg)` → `cfg.Shutdown.EffectiveTimeout()`; `queueSize(cfg)` → `cfg.Log.EffectiveQueueSize()`;
`configuredSelfID`/`int64Or(cfg.Transport.SelfID, 0)` → `cfg.Transport.EffectiveSelfID()`;
`historyTurns(cfg)` → `cfg.LLM.EffectiveHistoryTurns()`; `llmTimeout(cfg)` → `cfg.LLM.EffectiveTimeout()`;
`intOr(cfg.Agent.MaxIterations, agent.DefaultMaxIterations)` → `cfg.Agent.MaxIterationsOr(agent.DefaultMaxIterations)`;
`durationOr(cfg.Agent.StepTimeout, agent.DefaultStepTimeout)` → `cfg.Agent.StepTimeoutOr(...)`;
`durationOr(cfg.Agent.ApprovalTimeout, agent.DefaultApprovalTimeout)` → `cfg.Agent.ApprovalTimeoutOr(...)`;
`intOr(cfg.Agent.MemoryMax, 64)` → `cfg.Agent.EffectiveMemoryMax()`; `intOr(cfg.History.Retention, 400)` → `cfg.History.EffectiveRetention()`;
`intOr(cfg.LLM.AmbientTokenBudget, conversation.DefaultAmbientTokenBudget)` → `cfg.LLM.AmbientTokenBudgetOr(...)`;
`intOr(cfg.LLM.AmbientMaxChars, conversation.DefaultAmbientMaxChars)` → `cfg.LLM.AmbientMaxCharsOr(...)`;
`boolOr(cfg.Agent.Memory, true)` → `cfg.Agent.EffectiveMemory()`; `boolOr(cfg.Agent.VirtualActions, true)` → `cfg.Agent.EffectiveVirtualActions()`;
`boolOr(cfg.Agent.MemoryJudge.Enabled, true)` → `cfg.Agent.MemoryJudge.EffectiveEnabled()`;
`boolOr(cfg.Agent.ProactiveMemory.Enabled, true)` → `cfg.Agent.ProactiveMemory.EffectiveEnabled()`;
`boolOr(cfg.Agent.ToolHint.Enabled, true)` → `cfg.Agent.ToolHint.EffectiveEnabled()`;
`replyRule` empty-string fallbacks → `cfg.Behavior.EffectivePrivate()` / `cfg.Behavior.EffectiveGroup()`;
`sendShapeOf` → `cfg.Behavior.EffectiveSplitOnBlankLine()` / `EffectiveSplitDelay()` / `EffectiveMaxSegments()`.

Semantics the Lead should know before swapping:
- `Duration.Or` treats <=0 as unset, exactly matching the current `durationOr`. Do **not** use it for
  `behavior.split_delay`: explicit `0` is legal (no pause) and `Behavior.EffectiveSplitDelay()` preserves it.
- `EffectiveHistoryTurns` follows the field doc (“<=0 或未设置时用默认值”) and maps explicit
  `history_turns: 0` to 20; current main returns 0. This aligns main with the documented schema.
- `AmbientTokenBudgetOr` preserves negatives (doc: “负数表示不压缩”); the current
  `intOr(cfg.LLM.AmbientTokenBudget, ...)` silently maps negatives to the fallback. Adopting fixes that.

## Deliberately not changed
- `config_test.go` — untouched (no weakening, no renames).
- `Pricing` float fields: current callers pass fallback 0, and 0 is not knowledge owned by the
  module; adding `floatOr` wrappers would add API without removing any re-derivation.
- `stringOr` sites (api_key/access_token/system_prompt_file): no module default, nil and "" differ
  in meaning; left to the caller.
- `AutoMemory.Triggers`: default lives in `agent.NewMemoryCommand`; config must not import agent.
- `systemPrompt` file reading / `conversation.DefaultSystemPrompt`: I/O + cross-package default,
  not a pointer resolution.
- No behaviour change was made to `Load`, `Parse`, `Validate`, `RedactedYAML`; error strings,
  ordering (sort by path), and fail-fast semantics are byte-for-byte the same code.

## Commands run and results
```
gofmt -l internal/config              -> exit 0, no output
go vet ./internal/config/...          -> exit 0, clean
go test ./internal/config/... -count=1 -> exit 0, ok github.com/drysaltyfish/agentbot/internal/config
```
New tests pass individually (`-run` on the five new tests): all PASS.

Additional verification (not a repo-wide build): mechanically extracted each block copied
verbatim from `git show HEAD:internal/config/config.go` and compared code-only
(comments stripped, whitespace normalized). `Duration`, `Config`, reply constants, all
section structs, `Load`/`Parse`, `Validate`, `expandSecret`, `Redact`, `RedactedYAML`
are IDENTICAL. Only `Default()` (literals → named constants, 1:1 values) and
`expandSecrets`/`Redacted` (two hand lists → one table) were intentionally rewritten, and
both are covered by tests + the existing suite.

Note: running `go build ./...` / `go test ./...` was forbidden by the task; only the
config package was built/tested. Cross-package compilation of main.go was not verified.
