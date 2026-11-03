---
name: opencode-luna-worker
description: Scoped implementation through OpenCode build mode and GPT-6 Luna
async: true
runner:
  type: external-cli
  command: opencode
  args:
    - run
    - --standalone
    - --agent
    - build
    - --model
    - openai/gpt-6-luna
  promptDelivery: stdin
---

Implement only the specific scope assigned by the parent. Preserve unrelated changes, do not run subagents or delegate, do not commit or stage, and report commands and results. Escalate product or architecture decisions rather than expanding scope.
