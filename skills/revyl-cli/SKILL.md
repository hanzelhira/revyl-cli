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
4. Minimize round trips: when running 2+ device actions in a row, use one
   `revyl device batch` call instead of separate `tap`/`type`/`swipe`
   commands, and pass `--json` on device commands for compact output.

## Token-Efficient Device Control

Batch consecutive device actions into a single invocation. Steps are a JSON
array (or JSON Lines); output is one compact JSON line per step plus a
summary:

```bash
revyl device batch --steps '[
  {"action":"tap","target":"Sign In"},
  {"action":"type","target":"email field","text":"user@example.com"},
  {"action":"key","key":"ENTER"},
  {"action":"screenshot","out":"after-login.png"}
]'
```

Supported actions: tap, double_tap, long_press, type, clear_text, swipe,
drag, pinch, key, wait, back, home, shake, kill_app, launch, open_app,
screenshot, hierarchy. Each step accepts `"s": <index or label>` to target a
specific session, so one batch can drive several devices. Add
`--continue-on-error` to run all steps regardless of failures.

## Multiple Sessions: Label Everything

Start multiple sessions in one parallel call and give each a label naming its
purpose. Use labels (not indices) everywhere a session is addressed — `-s`,
per-step `"s"`, `device use` — so scripts stay readable and cannot silently
hit the wrong device:

```bash
revyl device start --platform ios,android --label checkout-ios,checkout-droid --json
revyl device label 0 logged-in        # label a session after the fact
revyl device list --json              # indices, labels, and state

revyl device batch --steps '[
  {"action":"tap","s":"checkout-ios","target":"Sign In"},
  {"action":"tap","s":"checkout-droid","target":"Sign In"},
  {"action":"screenshot","s":"checkout-droid","out":"droid.png"}
]'
```

When more than one session is active, `device batch` refuses steps without an
explicit `"s"` (or a `-s` default) instead of guessing, and bare device
commands print a warning naming the session they targeted. Every batch result
line echoes the resolved session index and label, so the transcript
self-documents which device each action hit.

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
