# Pylon

You must construct additional pylons.

Self-hosted daemon that turns events into sandboxed AI agent runs.

![demo](demo.gif)

## Install

```
curl -fsSL https://pylon.to/install.sh | sh
```

## Quick start

```bash
pylon setup                              # Configure channel + agent auth
pylon construct my-sentry --from sentry  # Create a pipeline from template
pylon start                              # Start the daemon
pylon test my-sentry                     # Send a test webhook
```

## What it does

- Responds to triggers (webhooks, cron schedules, chat commands)
- Spins up AI coding agents in sandboxed Docker containers with your codebase
- Reports results back to your chat channel, with optional human approval
- Runs entirely on your machine -- no SaaS, no data leaving your network

## Docs

Full documentation at [pylon.to](https://pylon.to)

## License

MIT
