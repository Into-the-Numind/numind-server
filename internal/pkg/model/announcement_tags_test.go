package model

import (
	"reflect"
	"strings"
	"testing"
)

// TestNotificationUserRefColumnsAreBigint 是 notif-userid-bigint 的回归守卫。
//
// 背景（dev 实测现场复现，非 sqlite 单测可复现）：user.id 是 `bigint unsigned`
// （gorm.Model 的 uint 默认映射 bigint）。若引用 user.id 的列写成 `int unsigned`，
// MySQL 建外键 fk_annread_user / fk_sr_user 时报
// "Referencing column ... and referenced column ... are incompatible"。
// 本测试静态断言这些列的 gorm tag 为 `bigint unsigned`，防止回退到 int unsigned。
//
// 注：sqlite 单测环境不区分 int/bigint 也不强制外键列类型一致，故该 bug 只能
// 在 MySQL 集成/部署期暴露——此处用反射静态守卫作可在 CI 跑的最强回归。
func TestNotificationUserRefColumnsAreBigint(t *testing.T) {
	cases := []struct {
		typ   interface{}
		field string
	}{
		{Announcement{}, "CreatedBy"},
		{AnnouncementRead{}, "UserID"},
		{SurveyResponse{}, "UserID"},
	}
	for _, c := range cases {
		rt := reflect.TypeOf(c.typ)
		f, ok := rt.FieldByName(c.field)
		if !ok {
			t.Fatalf("%s.%s 字段不存在", rt.Name(), c.field)
		}
		tag := f.Tag.Get("gorm")
		if !strings.Contains(tag, "bigint unsigned") {
			t.Errorf("%s.%s gorm tag 必须含 'bigint unsigned'（匹配 user.id，否则 MySQL FK 不兼容），实际: %q",
				rt.Name(), c.field, tag)
		}
		if strings.Contains(tag, "int unsigned") && !strings.Contains(tag, "bigint unsigned") {
			t.Errorf("%s.%s 仍是 int unsigned（回退了 notif-userid-bigint 修复）", rt.Name(), c.field)
		}
	}
}
