# Documentation project instructions

## About this project

- This is the documentation site for [Pylon](https://github.com/pylonto/pylon), a self-hosted daemon that turns webhook and cron events into sandboxed AI agent runs.
- Pages are MDX files with YAML frontmatter. Navigation lives in `docs.json`.
- Run `mint dev` in this directory to preview, `mint broken-links` to check links.
- For Mintlify component reference (Steps, Tabs, CodeGroups, ParamField, etc.), install the Mintlify skill: `npx skills add https://mintlify.com/docs`.

## Terminology

Use these exact terms consistently:

- **pylon** -- one configured pipeline (lowercase noun). Not "pipeline", not "flow".
- **Pylon** -- the product or daemon as a whole (proper noun, capitalized).
- **trigger** -- the event source that fires a pylon. Current types: `webhook`, `cron`.
- **agent** -- the AI coding tool that runs inside Docker. Current types: `claude`, `opencode`.
- **channel** -- the notification backend (`telegram`, `slack`, `webhook`, `stdout`).
- **workspace** -- how the agent gets source code (`git-clone`, `git-worktree`, `local`, `none`).
- **job** -- one execution of a pylon, identified by a UUID.
- **Nexus** -- the interactive terminal dashboard launched with `pylon nexus`.

Avoid: "project" (means something else), "task" (ambiguous with agent prompts), "bot" (misleading for agents).

## Style preferences

- Use active voice and second person ("you").
- Keep sentences concise, one idea per sentence.
- Use sentence case for headings.
- Use double dashes (`--`) not emdashes. Never use emdashes.
- Do not use emojis.
- Code-format file names (`pylon.yaml`), commands (`pylon start`), paths (`~/.pylon/`), and code references.
- Bold for UI elements the user clicks: **Approve**, **Settings**.
- Prefer concrete, copy-pasteable YAML and shell examples over abstract description.
- When showing a command, include the expected output only when it adds clarity.

## Cross-linking

- Use relative Mintlify links: `[workspace types](/concepts/workspaces)`.
- When you reference a YAML field, link to its section in `/yaml/pylon` or `/yaml/config` (e.g. `/yaml/pylon#trigger`).
- When you reference Nexus keybindings, link to `/concepts/nexus#keybindings`.

## Content boundaries

- Document only features that ship in the current release. Roadmap or "coming soon" items get a single `<Note>` callout, no full section.
- Do not document internal implementation details (Docker networking internals, SQLite schema, goroutine layout) unless they leak into user-observable behavior.
- Keep examples realistic. Prefer real-looking values over `foo`/`bar`.
