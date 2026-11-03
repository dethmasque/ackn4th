# Configuration

[← README](../../README.md)

`<config>/config.yaml` (run `ackn4th config init` to create it, `ackn4th
config` to see the effective settings):

```yaml
workspace_roots: [~/Git/ackn4th]          # trusted directories (ackn4th trust adds here)
default_workspace_root: ~/ackn4th-workspaces   # --scratch and --repo work here
default_provider: claude                  # one provider per team shares cached context
session_ttl: 24h
providers:
  claude: {model: claude-opus-5-5, max_tokens: 64000, effort: high, compact_at: 200000}
  gemini: {model: gemini-3.1-pro-preview, max_tokens: 65536, compact_at: 200000}   # auth: key (AI Studio)
  claude-code: {model: claude-sonnet-5-5, effort: medium}   # your Claude plan via local Claude Code
  openai: {max_tokens: 32000, effort: medium}  # model: login picks the newest your key can use
  codex: {model: default}                 # "default" lets the CLI choose; also cursor, copilot
  # Vertex AI instead:  gemini: {auth: vertex, project: my-gcp-project, location: global}
agents:                                   # named per-agent presets (see internal/provider/README.md)
  fast-writer: {provider: claude, model: claude-sonnet-5-5, effort: medium}
commands:                                 # run_command: sandboxed builds and tests
  enabled: true
  network: false                          # true lets package managers fetch dependencies
  timeout: 10m
limits:
  max_rounds: 10
  max_steps_per_turn: 40
  max_file_bytes: 1048576
effects: true
tips: true                                # first-run hints; ackn4th disable-tips sets false
prefer_cli: true                          # default claude → claude-code, openai → codex when signed in
voice:                                    # the dashboard reads itself aloud (ackn4th voice on)
  enabled: false
  name: af_heart                          # ackn4th voice list
  speed: 1                                # 0.5 to 2
```

`disable-tips`, `voice on` and the OpenAI model choice at login change
just their key, keeping the rest of the file and its comments.

## Where files live

ackn4th keeps its files where each OS expects an app's files:

| | `<config>`: settings, roles, sign-ins | `<data>`: tasks, notes |
|---|---|---|
| Linux | `$XDG_CONFIG_HOME/ackn4th` (`~/.config/ackn4th`) | `$XDG_DATA_HOME/ackn4th` (`~/.local/share/ackn4th`) |
| macOS | `~/Library/Application Support/ackn4th` | the same folder |
| Windows | `%APPDATA%\ackn4th` | `%LOCALAPPDATA%\ackn4th` |

- **API keys** go in the OS keychain, not in these folders.
- **The voice**, a large download that holds nothing of yours, goes in the
  OS's cache folder ([voice](../voice/README.md#install)).
- **Temporary files** go in the OS's temp directory (`$TMPDIR`, else `/tmp`;
  `%TEMP%` on Windows), except the agents' own scratch space, which stays
  with its task.
- **Claude Code** files each agent's session with its workspace's project,
  as it does for your own sessions, so `claude --resume` in the workspace
  lists them.
- **`ACKN4TH_HOME`** moves everything but the voice into one folder
  (`config/` and `data/`), as the [dev sandbox](../../sandbox/README.md)
  does.

Task state lives in `<data>/tasks/<id>/` ([what's in it](../task/README.md#code)).
Project notes are in `<data>/projects/<hash>/notes.md`.
