---
description: Claude and Codex discuss requirements over multiple rounds, Codex implements after confirmation, and Claude verifies
---

Run the five phases in order for the request below. **Do not skip phases.**

$ARGUMENTS

## Ground rules (apply to every phase)

- **Working dir** — pass `cd=<project root absolute path>` on every `codex` call (your current working directory; run `pwd` if unsure), so Codex reads the project's own `AGENTS.md`.
- **Language** — speak to the user in Chinese; write every `codex` PROMPT in English. *But* always carry the user's original requirement to Codex **verbatim and untranslated** (every round, whatever language they wrote it in) — only your own analysis is in English. Data (filenames, code, commands, logs, quoted text) stays verbatim. The server can't force Codex's reply language; if it replies otherwise, read it and relay in Chinese.
- **Don't echo the trace** — don't re-paste Codex's reasoning in the conversation; the user can open the tool-call return themselves when needed.
- **Live streaming** — to watch Codex think live, run it read-only via Bash: `codex exec --json … | <codex-collab binary> fmt` (the MCP tool only returns after it finishes).
- **Plan = numbered criteria** — the Final Plan is the single source of truth: written in Chinese as stable IDs `SC-1, SC-2, …`. The English implementation prompt is a throwaway translation of those IDs (IDs preserved); verification maps results back by ID.

## 1 · Decompose

- Form your own preliminary plan and stance first (key points, assumptions, tradeoff rationale) as the working draft to debate with Codex, not a task to hand off to it.
- State assumptions explicitly. When there's ambiguity or multiple viable approaches, lay them out; when a simpler approach exists, say so.
- When something is genuinely unclear, stop — don't guess.

## 2 · Debate — read-only

- `sandbox="read-only"`, pass `cd` on every call. Round 1 omits `session_id` (new session); reuse the returned `session_id` for Round 2+.
- Use a different communication structure per round; the goal is to mature the plan through debate and converge quickly.

**Round 1 — get an independent view.** The PROMPT must:
1. Restate the request — the user's original wording, verbatim (don't translate).
2. Present your own preliminary plan and stance.
3. Ask Codex to first set your plan aside, analyze the original request independently, and give its own plan; then respond to yours point by point — agree (say why) or rebut (with technical reasons), not just criticize.

**Round 2+ — respond with judgment, converge.** No one-way "please implement/analyze X". The PROMPT must:
1. Take a stance and adjudicate — for each of Codex's points, agree (say why) or rebut (with technical reasons) to narrow the disagreement; only the points you and Codex both agree on settle into the plan.
2. Show Codex the iterated plan — the current version integrating both Codex's and Claude's ideas, carrying your stance and this round's iteration.
3. Keep narrowing the open points — say which of Codex's points you rejected and why. This is a joint synthesis, not Codex reasoning solo: contribute your own stronger alternatives as input, merge the best of both lines of thinking, and let the better idea win on merit, whoever proposed it.

**The plan is co-developed by you and Codex; you face the user, but you're not a switchboard.** When a real requirement ambiguity surfaces — from your own analysis or from Codex's challenge — and only the human can decide it, pause and ask the human in Chinese, in your own words (don't forward Codex's wording). Then weigh their answer together with Codex's doubt, revise the plan if warranted, and carry the updated position into the next round with Codex. Answer on your own only for requirement points the user has already fixed; any requirement point the user hasn't fixed goes back to the user to confirm — don't fill requirement gaps with your own inference.

**Forced convergence.**
- Stop as soon as both sides agree on the core approach.
- `CODEX_MCP_MAX_ROUNDS` caps the rounds (the server hard-rejects past it); use the returned `rounds_remaining` to converge early, never hard-max it.
- **Escalate deadlocks** — a real deadlock only when BOTH hold: (1) the latest round added nothing new from either side, just restated positions; (2) it's a subjective tradeoff with no objective tiebreaker (if code/docs/a test could settle it, check first — don't escalate). Only then stop and hand the user a tally — **points already agreed / points still open / points needing your decision (each with both sides' positions and rationale)** — for the user to decide; otherwise keep debating.
- Trivial tasks may shorten the debate — shorten, don't skip: every phase still runs, especially the pre-write confirmation (Phase 3) and post-write verification (Phase 5).

## 3 · Finalize

- Synthesize the discussion into the Chinese **Final Plan**.
- State which of Codex's suggestions you accepted and which positions you kept.
- The plan must be written as verifiable numbered success criteria: `SC-1`, `SC-2`, … — no vagueness.
- **Stop and wait for the user's confirmation; no implementation before confirmation.**
- **If the user objects instead of confirming, don't settle it alone — the plan was co-developed with Codex. Roll back by granularity, then resume the phases in order (don't skip):**
  - Minor wording, or a requirement the user is now fixing with the approach unchanged → revise the Final Plan yourself and re-present it for confirmation (still Phase 3).
  - The objection touches the technical approach or business logic → back to Phase 2 (reuse the debate `session_id`) to re-debate with Codex, then carry on Phase 3 → 4 → 5.

## 4 · Implement — write

- After the user confirms, call Codex to implement the **Final Plan**.
- Parameters: **no `session_id`** (new session), `sandbox=workspace-write`, `confirmed=true`. The PROMPT is the English translation of the confirmed `SC-*` plan (IDs preserved).
- Prefix the PROMPT with coding rules:
  - **Simplicity** — minimum code that solves it; no unrequested features/abstractions/config.
  - **Surgical** — change only what's needed; don't refactor unrelated code; match existing style.
  - **Goal-driven** — self-check against every `SC-*` when done.

## 5 · Verify — read-only

Check Codex's output against the Final Plan through three lenses:
- **Goal** — is every `SC-*` met?
- **Simplicity** — any unrequested feature/abstraction/config, or over-engineering?
- **Surgical** — does every changed line trace to the plan? any out-of-scope refactor?

**Roll back by granularity when it falls short, then resume the phases in order (don't skip).**
- Compile error / obvious bug / scope-creep refactor → back to Phase 4, rewrite with logs, then re-verify (Phase 5).
- Deep business-logic flaw (the plan itself is flawed) → back to Phase 2 (reuse the debate `session_id`) to re-debate and repair the plan, then carry on through Phase 3 → 4 → 5.
