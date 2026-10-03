# scan-agent-runtime — deepening opportunities in agent / event / conversation / router

Read-only scan. Task task-3. Vocabulary: module, interface, seam, depth, adapter, locality, leverage, deletion test.

## Context that frames every candidate

- `cmd/server/main.go` is 1581 lines, has **zero tests** (`cmd/**/*_test.go` glob returns nothing), and was touched by **23 of the last 30 commits** (git churn, confirmed). Every other package in this slice is exercised only through `main.go`, so main.go is the de-facto interface of the whole runtime.
- `internal/router` (1950 lines) is imported by exactly one file, `cmd/server/main.go:32`, which registers exactly **one** route: `routes.OnMessage(router.Always())` (main.go:1120). Command/keyword/regex/prefix rules, `StateKeyCommand`/`StateKeyArgs`, `UsePre/Mid/Post`, and `BindFlags` are reachable only from tests.
- `internal/conversation.Assembler.Build` is called only from tests (`conversation_test.go:38-213`, `ambient_test.go:124`). Production calls only `conversation.ToMessages` (main.go:1380) and `Assembler.PrefixHash` (main.go:998, 1455).

---

## Candidate 1 — Message assembly is duplicated; the deep Assembler is off the production path (Strong; TOP)

**Files + lines**
- `internal/conversation/conversation.go:87-139` (`Build`, `compress`, `finalizeAmbient`), `:225-243` (`trimHistory`), `ambient.go:60-95` (`CompressAmbient`)
- `internal/agent/react.go:91-110` (system + memory + history + query, in-loop)
- `internal/agent/agent.go:99-104` (system + history + query — no memory)
- `cmd/server/main.go:990-996`, `:1111-1115`, `:1377-1383`, `:1455`

**Problem.** The three-segment layout from ADR-0002 / the cache-first package doc (`conversation.go:1-20`) is implemented in three places with three different rules:
1. `Assembler.Build`: system + trimmed/compressed history + query, with `trimHistory` window sliding and ambient token-budget compression.
2. `ReactAgent.Run`: system + memory + `in.History` + query, **no ambient compression, no window**.
3. `DirectAgent.Run`: system + `in.History` + query, **no memory**, no compression.

Production uses path 2/3, so `Assembler.MaxHistory` (main.go:992), `AmbientTokenBudget` (994) and `AmbientMaxChars` (995) are configured, logged, and documented as active (main.go:972-975, 997-998) while **never being applied to a real request**. `history.Item.Ambient` is written (main.go:1336) and persisted (`history/sqlite.go:67,200`) but read only inside `Assembler.compress`, which never runs. The "environment messages are compressed by token budget so group chatter cannot crowd out real dialogue" guarantee (ambient.go:49-59) is dead in the running binary.

Worse, the logged `prefix_hash` (main.go:1455) is `p.asm.PrefixHash()` — a hash of the Assembler's private copy of the prefix — not of the messages the agent actually sent. The only real evidence is `out.PromptDigest` (agent.go:65). A change to segment order (the exact thing ADR-0002 pins) requires edits in `conversation.go`, `react.go`, and `agent.go`, and the hash log may not notice.

Supporting evidence of split ownership: the prefix-relation semantics are duplicated between `internal/llm/prefix.go:72-95` (`ComparePrefix`) and `internal/store/prompt.go:110-140` (`compareDigest`), and the latter's comment at `prompt.go:102` still cites a nonexistent `conversation.ComparePrefix`.

**Solution.** Make the Assembler the only module that turns history into the message list, and put the memory slot inside it. Concretely: give `agent.Input` a `History []history.Item` (it already carries `SessionKey`), inject the Assembler (or a one-method interface `Build([]history.Item, query string, memory []string) []llm.Message`) into `ReactAgent`/`DirectAgent`, and delete the assembly code at react.go:91-110 / agent.go:99-104. The Assembler's resulting interface stays small: `Build` + `PrefixHash`; it absorbs windowing, ambient compression, and ADR-0002 ordering. Alternatively, invert it: main.go calls `asm.Build` and passes ready-made messages, with the Agent taking `Input.Messages` — smaller diff, same locality.

**Benefits.** Locality: window + ambient compression + memory position + prefix hash all live where they are tested. Leverage: the existing `Test_CacheFirst_PreviousTurnIsPrefixOfNext` (conversation_test.go:33) starts guarding the real path instead of a path nobody calls. Tests become possible: a runtime test asserting the actual request's message sequence contains a compressed ambient block and a bounded window.

**Before/after.** Before: three shallow assemblers agree by convention; the deep one is test-only. After: one deep Assembler whose interface is the ADR-0002 layout; agents are consumers, not co-authors, of the message list.

**ADR.** ADR-0002 constrains *position* (system → memory → history → current input), not ownership. Moving the memory slot into the Assembler **preserves** the decision; leaving three copies is the real ADR risk. Callout: whichever module owns it must keep `MemoryDigest` populated (react.go:106) or the expected-vs-unexpected divergence classification in `store/prompt.go:126-140` degrades.

---

## Candidate 2 — The router is a deep module with one production caller; reply policy lives in main.go (Strong)

**Files + lines**
- `internal/router/router.go:170-211` (`On` + 8 one-line `OnX` pass-throughs), `:26-42` (`Route` exported fields + private state)
- `internal/router/engine.go:36-45, 101-158` (pre/mid/post phases, `Dispatch`)
- `internal/router/rules.go:32-179` (`Kind`, `Command`, `Prefix`, `Suffix`, `Keyword`, `FullMatch`, `Regexp`, `AtMe`)
- `internal/router/command.go:81-183` (`BindFlags`, `BindFlagsOpt`, `BindOptions`, `filterKnownFlags`) — no production caller
- `cmd/server/main.go:538-564` (`replyRule`), `:1120-1150` (route + handler), `:1138-1142`, `:637-650` (`agentRole`)

**Problem.** Production routes *every* message through `router.Always()`, then decides "do we reply?" inside the handler with a freshly constructed closure `replyRule(cfg)(c)` (main.go:1140), stores it as `replyJob.shouldReply` (main.go:834-835), and only later converts it to `history.Item.Ambient` (main.go:1336). So the bot's reply policy is not a `router.Rule`; it is a config-to-closure translator in main.go plus a job field plus a handler branch. To answer "when does the bot reply?" a maintainer reads main.go, not the router — even though the router owns `AtMe`, `OnlyGroup`, `OnlyPrivate`, `Never/Always`, and the whole rule-composition surface.

Meanwhile `BindFlags` (command.go:81-183, ~100 lines, reflection-based) has **no production caller at all** — only `command_test.go`. Its interface (3 exported funcs, 3 errors, a `BindOptions` struct) is nearly as complex as its body, and it is not about routing. The deletion test is clean here: deleting it removes complexity and nothing else.

The `Route` interface is also leaky: `Name`, `Handlers`, `Rules`, `Kind`, `Block`, `Break` are exported fields while `priority`/`once`/`owner` are private and must be kept consistent with the `Router` (`Named` triggers `noteName`, `Priority` triggers `markDirty`, but `rt.Name = "x"` silently bypasses the duplicate warning). Callers can violate invariants the module then depends on.

**Solution.** Register the reply policy as routes: `routes.OnMessage(replyRule(cfg))` (with a second `Always()` route for record-only if needed), and move `replyRule` / `agentRole` / `speakerDisplayName` / `groupScopedUserID` into a small module that returns `router.Rule`s over `*router.Ctx` — the router's existing seam. Hide `Route`'s mutable fields behind the chained methods (or make the zero-value invariants internal). Delete `command.go:81-183` until a command-flag caller exists (keep `ParseCommandArgs`, which `Command` uses).

**Benefits.** Locality: reply policy is one testable rule instead of a handler side effect; `replyJob.shouldReply` disappears. Leverage: the router's ~1950 lines start paying for themselves at more than one route. Tests: rule tests can cover "private always / group on-mention / never reply to self" without a server or a `cmd` test harness.

**ADR.** None of ADR-0001/0002/0003 constrain routing; no conflict.

---

## Candidate 3 — Extract the reply turn from the composition root (Worth exploring)

**Files + lines**
- `cmd/server/main.go:786-840` (`replyPipeline`, `sendShape`, `sendShapeOf`, `replyJob`)
- `cmd/server/main.go:1288-1492` (`handleReply`, ~205 lines): auto-memory extract, quoted-message resolution, history append, agent run, usage ledger, prompt-snapshot classification, paragraph split, send
- `cmd/server/main.go:807-820`, `:1494-1539` (log helpers)

**Problem.** The single most consequential runtime policy — what a reply turn does and in what order — lives in an untested 205-line function in the composition root. main.go carries 23/30 recent commits and 1581 lines. Ordering invariants are stated only in comments (`Append` the user turn *before* deciding whether to reply, main.go:1344-1353; resolve quotes *before* re-rendering text, 1320-1324; record user turn before running the model so the query is not duplicated, 1360-1363). There is no seam: a test cannot run one turn without a store, sessions, sender, and clock.

**Solution.** Move `handleReply` + `replyJob` + `replyPipeline` + `sendShapeOf` into a `reply` module with an explicit dependency struct (the fields already exist as `replyPipeline`) and one exported method `Run(ctx, Turn) error` (or `Handle`). main.go keeps only construction and wiring. The module's interface is the turn contract; the store, sender, agent, and resolver stay behind their existing interfaces (`history.History`, `agent.Agent`, `outbound.Sender`).

**Benefits.** Locality: turn ordering and the usage/snapshot side effects get one home and become unit-testable with fakes that already exist (`llm.NewFakeLLM`, in-memory `history`). Leverage: any future entry point (self-test, admin command) gets the same turn semantics. This is deliberately coupled to Candidate 1/2 — order the extraction after assembly is unified so the new module does not bake in the bypass.

**ADR.** None; ADR-0003 only fixes the persistence layer beneath it.

---

## Candidate 4 — The memory scope key is owned by `tool`, forcing memory→tool coupling (Worth exploring)

**Files + lines**
- `internal/tool/scope.go:5-24` (key + `WithScope`/`ScopeFrom`)
- `internal/agent/memory.go:50-57` (`WithMemoryScope`/`MemoryScopeFrom` — pure aliases)
- `internal/memory/store.go:79, 156, 172, 180, 188, 196` (imports `tool` only for `ScopeFrom`)
- `internal/tool/builtin/history.go:126`, `userinfo.go:59`

**Problem.** The ctx value that means "which conversation this turn belongs to" is defined in the tool registry package. The agent's `Memory` implementations read it through an alias (`MemoryScopeFrom`), while the real `memory.Store` adapter reads it via `tool.ScopeFrom` (store.go:79) — a package that has nothing to do with tools. Two names for one key, two owners for one invariant. `agent.WithMemoryScope` (memory.go:50-52) is a pass-through: delete it and only two call sites change; it adds a name, not behavior.

**Solution.** Give the scope contract its own module (or move it to `session`/`history`), expose it as a small interface consumed by both `agent`/`memory` and `builtin`, and have `agent.MemoryScopeFrom` be the only alias — or drop the alias and use the owner directly. This is small and mechanical; its value is preventing a future third copy of the key.

**ADR.** ADR-0002 requires scope isolation; this refactor preserves it.

---

## Top recommendation in this slice

**Candidate 1.** It is the only candidate with a *live correctness gap*: configured ambient compression and the presentation window never execute, and the emitted `prefix_hash` does not describe the request actually sent. The deep module already exists and is already tested — production simply routes around it. Re-pointing the agent at `conversation.Assembler` converts test-only coverage into runtime coverage, removes two duplicate assembly sites, and gives ADR-0002 a single enforcer. Do Candidate 1 before Candidate 3 so the extracted turn module consumes the unified assembly rather than cementing the bypass.
