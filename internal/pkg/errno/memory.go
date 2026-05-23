package errno

// ErrLayerBNotSupported is returned when V1.5 store layer receives a write
// (Create / BatchCreate) for user_memory_facts with a non-nil SubjectID.
//
// V1.5 = Layer A (对使用 agent 的真实 user 画像) → SubjectID 必须为 nil。
// V2   = Layer B (对使用者关注的对象画像) → SubjectID 填业务实体 ID。
//
// schema 字段 subject_id 已在 V1.5 预留好（避免 V2 时破坏性 ALTER），
// 但 V1.5 store 层显式拒绝非 nil 写入，等 V2 启用 Layer B 时去除此校验。
var ErrLayerBNotSupported = &Errno{
	HTTP:    400,
	Code:    "InvalidParameter.LayerBNotSupported",
	Message: "user_memory_facts.subject_id must be NULL in V1.5 (Layer B is reserved for V2).",
}
