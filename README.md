# Pylon

Self-hosted daemon that listens for events, spins up sandboxed agents, and reports results back to chat.

```
curl -fsSL https://pylon.to/install.sh | sh
```

## Quick start

```bash
pylon setup                              # Configure Telegram bot + Claude Code auth
pylon construct my-sentry --from sentry  # Create a pipeline from template
pylon start                              # Start the daemon
pylon test my-sentry                     # Send a test webhook
```

## TODO

### Ship blockers

- [ ] Publish `pylon/agent-claude` Docker image to Docker Hub / GHCR so `curl | sh` users don't need the source to build it
- [ ] `pylon setup` should pull the agent image after configuring auth (currently builds locally)
- [ ] Implement proper job queue when at max concurrency (currently rejects)

### CLI polish

- [ ] `pylon start -d` background daemon mode
- [ ] `pylon stop` / `pylon restart` should signal the running daemon process (currently just prints a message)
- [x] `pylon jobs` aggregates across per-pylon DBs
- [ ] `pylon retry <job-id>` should re-trigger from stored payload (currently stub)

### Channels

- [x] Slack (Socket Mode with approval buttons)
- [ ] Discord
- [ ] WhatsApp
- [ ] iMessage
- [ ] Generic webhook (HTTP POST) -- config type exists, no implementation

### Telegram commands

- [ ] `/tail` -- live-updating message showing the last 8 agent actions (edits in place)

### Agents

- [x] OpenCode (v0.1.10+)
- [ ] Codex
- [ ] Aider

### Triggers

- [x] Cron (scheduled) -- with timezone support and auto-detect
- [ ] Chat command (trigger from Telegram/Slack message)
- [ ] API call trigger

### Workspace

- [x] Git worktree mode (`git worktree add` per job)
- [x] Local workspace mode (use existing directory)
- [ ] Workspace caching / warm pools for faster cold starts

### Infrastructure

- [ ] CI/CD pipeline (build + release binaries on tag)
- [x] Release binaries (linux/amd64, linux/arm64)
- [ ] macOS support (blocked on Keychain credential storage for Claude OAuth)
- [ ] Publish Docker image on release
- [ ] Landing page at pylon.to

## License

MIT
