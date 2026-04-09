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
- [ ] `pylon setup` should pull the agent image after configuring auth
- [ ] Implement `git-worktree` workspace type (currently falls back to `git-clone`)
- [ ] Implement proper job queue when at max concurrency (currently rejects)

### CLI polish

- [ ] `pylon start -d` background daemon mode
- [ ] `pylon stop` / `pylon restart` should signal the running daemon process (currently just prints a message)
- [ ] `pylon jobs` should aggregate across per-pylon DBs properly
- [ ] `pylon retry <job-id>` should re-trigger from stored payload

### Notifiers

- [ ] Slack
- [ ] Discord
- [ ] WhatsApp
- [ ] iMessage
- [ ] Generic webhook (HTTP POST)

### Telegram commands

- [ ] `/tail` -- live-updating message showing the last 8 agent actions (edits in place)

### Agents

- [ ] OpenCode -- ~~integrate as second provider~~ done (v0.1.10+)
- [ ] Codex
- [ ] Aider
- [ ] Custom command

### Triggers

- [ ] Cron (scheduled) -- trigger type exists in config but no scheduler runs it
- [ ] Chat command (trigger from Telegram/Slack message)
- [ ] API call trigger

### Workspace

- [ ] Git worktree mode (use local repo, `git worktree add` per job)
- [ ] Workspace caching / warm pools for faster cold starts

### Infrastructure

- [ ] CI/CD pipeline (build + release binaries for linux/amd64, linux/arm64, darwin/amd64, darwin/arm64)
- [ ] Cross-platform builds in release (currently only linux/amd64)
- [ ] Publish Docker image on release
- [ ] Landing page at pylon.to

## License

MIT
