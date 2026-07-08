---
name: revyl-cli
description: Base CLI skill for Revyl command-driven workflows. Use when users want shell-command setup, execution, test authoring, or run triage without MCP tool calls.
---

# Revyl CLI Skill

Use this as the default Revyl skill when workflows should be expressed as `revyl` commands.

## Native Agent Behavior

- Ask at most 1-3 concise clarification questions only when the target app, platform, session, URL, or sensitive action cannot be inferred from the repo or Revyl CLI.
- Prefer safe defaults and keep moving when `revyl init --detect`, `revyl dev list`, `revyl app list`, screenshots, or reports can answer the question.
- When Revyl prints a viewer, editor, report, or local app URL, open it in the native browser/tool surface when available: Codex Browser/in-app browser for local URLs, Revyl URLs, screenshots, and page checks; Claude Code `.claude/skills` slash-command discovery plus WebFetch/WebSearch or configured MCP/browser tools; Cursor `.cursor/skills` plus `.cursor/rules/revyl-skills.mdc` and available MCP/browser tools.
- If no browser tool is exposed, report the URL and verify through `revyl device screenshot`, `revyl device report`, or `revyl test report` instead of claiming browser access.
- Confirm before entering sensitive data, submitting forms, uploading files, accepting browser permissions, changing sharing/access, or deleting data.

## Route to Specific CLI Skills

- Use `revyl-cli-dev-loop` for local dev loop workflows and exploratory path capture.
- Use `revyl-cli-create` for authoring robust YAML tests.
- Use `revyl-cli-auth-bypass` for test-only authenticated app state setup.
- Use `revyl-cli-analyze` for failed run triage.

## Operating Rules

1. Prefer explicit command sequences.
2. Keep secrets in env vars or test variables.
3. Keep steps deterministic and avoid hidden assumptions.
4. With multiple sessions, address each by label, not index, and pass
   `--json` on device commands for compact output.

## Multiple Sessions: Label Everything

Give each session a label naming its purpose, and use labels (not indices)
everywhere a session is addressed — `-s`, `device use` — so scripts stay
readable and survive context churn instead of breaking on a stale index:

```bash
revyl device start --platform ios --label checkout-ios --json
revyl device label 0 logged-in        # label a session after the fact
revyl device list --json              # indices, labels, and state
revyl device screenshot -s checkout-ios
```

With more than one session active, a bare device command warns which session
it targeted, so a wrong-session action surfaces instead of passing silently.

## Acquire Sessions with `ensure`, Not Check-Then-Start

Sessions die from idle timeouts while you work. Do not screenshot first and
handle the error, and do not run `device list` then `device start` — both
race against expiry. Acquire idempotently in one call and act on the result:

```bash
revyl device ensure --platform ios --label checkout --json
# {"index":0,"platform":"ios","label":"checkout","session_id":"...","reused":true}
```

`ensure` reuses a matching healthy session, replaces a matching dead one, or
starts a new one. Session-resolution errors also include the live roster
inline (e.g. `no session at index 6. Active: 0=ios "checkout", 1=android`),
so recover from the error text directly instead of running `device list`.

## Baseline Checks

```bash
export PATH="$HOME/.revyl/bin:$HOME/.local/bin:$PATH"
revyl auth status
revyl version
revyl test list
```

For headless agents, set `REVYL_API_KEY` and run:

```bash
revyl auth login --api-key "$REVYL_API_KEY"
```
