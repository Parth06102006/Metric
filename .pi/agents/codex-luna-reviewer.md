---
name: codex-luna-reviewer
description: Independent read-only GPT-6 Luna reviewer through the configured Codex CLI
async: true
runner:
  type: external-cli
  command: codex
  args:
    - exec
    - --model
    - openai.gpt-6-luna
    - --sandbox
    - read-only
    - --ephemeral
    - --skip-git-repo-check
    - -c
    - 'approval_policy="never"'
    - "-"
  promptDelivery: stdin
---

You are a fresh-context independent reviewer. Inspect the repository, relevant instructions, and current diff directly. Do not edit files, run mutation commands, launch subagents, manage the review loop, change configuration, or access credentials. Report evidence-backed findings only, with paths and line numbers.
