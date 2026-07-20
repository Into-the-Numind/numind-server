# S4 Task 2：`xhs_note_list`

- 日期：2026-07-20
- RED：focused tests 因 `xhsNoteListTool` 不存在而编译失败。
- GREEN：工具只从鉴权 context 获取当前 user，不接受 `user_id`；输入严格拒绝未知字段，limit 默认/最大 100，cursor 使用 canonical JSON + base64url 并绑定 filter SHA-256 与 projection。
- 输出：index 只含业务键/采集时间；full 只含 Prompt 1 原始字段，缺值为 null，计数标记为保存的采集值且 presence unknown，评论只输出文本和一层回复，不暴露作者或既有 AI 富化字段。
- Factory：nil datastore 仍为 19 个工具；真实 datastore 为 22 个（新增 `xhs_note_list`），旧飞书工具未复活的回归断言同步保留。
- Gate：focused、Agent 整包、store/XHS、focused race、`task lint` 与 diff hygiene 通过。
