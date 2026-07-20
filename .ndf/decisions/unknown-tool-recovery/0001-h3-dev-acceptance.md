# H3 Dev acceptance — unknown tool recovery

Date: 2026-07-20

## Clarified tool status

`run_python` exists as a platform sandbox tool and has its own implementation and regression suite. It is intentionally disabled in all three pipeline AgentDefinitions (`run_python=false`) because these workflows do not require arbitrary Python execution. Dev run 268 therefore failed because the model called a tool absent from that Agent's allowed tool list, not because the platform implementation of `run_python` was broken.

## Fix and boundaries

- RED commit `5e403159` reproduces Eino's fatal unknown-tool behavior.
- Fix commit `a19e201b` installs one shared `UnknownToolsHandler` in both streaming and non-streaming Agent Runner paths.
- The handler executes nothing, grants no permission, does not choose a fallback tool, does not replay writes, ignores model-generated arguments, and returns bounded guidance to use only currently available tools.
- The three pipeline definitions still declare `run_python=false`.

## Verification

- Focused Agent package, actual Eino ToolsNode behavior, focused race, full `go test ./... -count=1`, `task lint`, YAML/diff hygiene, and self-review passed.
- Merge/deploy commit: `787ffa1f`.
- Dev registry digest: `sha256:1d66a7e946d2b8f34755698e59af2c2fd5ed7c8534663b1c1e176bc03d098c86`.
- Dev runtime image: `sha256:62b2d7869368951c03df6a1681d91cfb1ead576cc051b1e16e9648cab40f30f6`; public and container health passed.
- Real Agent 3 run 271 chose enabled `get_current_date`, generated its own round ID, appended R3 with one complete nine-field topic, retained R1/R2, and completed. Safe metric: `agent-3`, `source_count=5`, `output_mode=append`, `status=ok`.

Prod was not tagged or deployed.
