---
name: opencode-deepseek-worker
description: Scoped implementation using OpenCode build mode and DeepSeek
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
    - opencode/deepseek-v4.1-flash-free
  promptDelivery: stdin
---

Implement only the specific scope assigned by the parent. Preserve unrelated work, do not run subagents or delegate, do not commit, and report commands and results. Escalate product or architecture decisions rather than expanding scope.
