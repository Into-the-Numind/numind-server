# S6 real Dev acceptance — three-Agent Feishu pipeline

Date: 2026-07-20

## Runtime and definitions

- Backend source merged at `5d0d3b45`; artifact-read Hotfix merged at `07dd9247` and deployed to Dev as `develop-07dd9247`. Web had no source change.
- Current-organization definitions are active and unique: Agent 1 `100011`, Agent 2 `100012`, Agent 3 `100013`. Their exact prompts, tool flags, versions, starters, descriptions, and history were read back after creation.
- Feishu connection and Docs/Base/Drive/Wiki capabilities were available for the Dev acceptance user.

## Real workflow results

| Agent | Run(s) | Result |
|---|---:|---|
| Agent 1 | 259, 262 | Created a dedicated Base with five complete analysed rows, then re-ran against the same Base and skipped all five as already completed. No manual selection or duplicate rows. |
| Agent 2 | 263, 266 | Run 263 exposed the transient artifact visibility bug after the real document write. After Hotfix deployment, run 266 fully read v1 + v2, updated the same `profile/v1` document to revision v8, preserved unknown price/refund/parking facts as pending, and did not create another document. |
| Agent 3 | 267, 269, 270 | Created one `topics/v1` document and R1 with three nine-field topics; appended R2 with two new topics while retaining R1; then precisely replaced named R1 while retaining R2 and both unique marker pairs. |

## Observability and controlled failures

- Successful structured metrics contained only `schema`, `agent`, `source_count`, `output_mode`, and `status`; no customer, topic, source text, URL, or Feishu identifier appeared.
- Run 268 attempted disabled `run_python` solely to generate a random round ID. Eino returned fatal `tool run_python not found` before any write. Supplying the round ID explicitly made run 269 pass. This is a residual P1 platform-recovery issue; recommend a small `UnknownToolsHandler` Hotfix before Prod.
- Run 270 was intentionally asked to emit noncanonical `replace_named_round`; the strict parser safely degraded it to `status=unavailable`. The canonical Agent 3 metric mode remains `replace-round`.
- Run 265 used immediate `attachment_ids` for a plain-text fixture and received a pending fallback placeholder; the equivalent owned upload URL path in run 266 exercised `file_read` successfully. Production upload UX should continue supplying the normal model key/fallback readiness context.

## Gate

The three business workflows pass real isolated Dev acceptance. Stage remains S6 because Prod was not authorized and the unknown-tool recovery P1 should be explicitly triaged before any Prod action.
