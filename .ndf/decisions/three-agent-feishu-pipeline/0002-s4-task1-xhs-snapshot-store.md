# S4 Task 1：XHS 稳定快照 Store

- 日期：2026-07-20
- RED：`TestXhsSnapshot*` 因快照 query/projection/ListSnapshot 尚不存在而编译失败。
- GREEN：243 条当前用户记录按 100/100/43 稳定翻页；第一页后新插入 ID 不进入旧快照；跨用户、组合 ID/关键词/时间过滤、LIKE 字面转义、index/full 最小列投影、limit+1、删除中途行和输入边界全部通过。
- Gate：`go test ./internal/numind/store ./internal/numind/biz/xhs -count=1` 与 `task lint` 通过。首次 lint 因本机 PATH 缺少 Go bin 只执行到 vet；加入明确的 `/Users/zhiyuchen/go/bin` 和 `/usr/local/go/bin` 后同一 Gate 完整通过。
