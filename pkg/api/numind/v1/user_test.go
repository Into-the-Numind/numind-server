package v1

import (
	"strings"
	"testing"

	"github.com/asaskevich/govalidator"
)

func strPtr(s string) *string { return &s }

// TestUpdateUserProfileRequest_NicknameLength 验证昵称校验上限为 10 个字符（按 rune 计数）。
// 这是 nickname-edit 功能的后端回归保护，核心确认产品约束「10 个字符以内」按字符（rune）
// 而非字节计：10 个汉字（30 字节但 10 rune）应放行，11 个汉字应被拒。
//
// govalidator 陷阱（已坐实）：stringlength(1|10) 对**空串**（非 nil 指针指向 ""）会**跳过**校验
// —— govalidator 对非 required 字段把空值当"未提供"放行，故 min=1 对空串无效（等价 0|10）。
// 因此空昵称不被此 tag 拦截；空昵称的必填防护在前端弹窗（confirmNicknameEdit 必填守卫）完成。
func TestUpdateUserProfileRequest_NicknameLength(t *testing.T) {
	cases := []struct {
		name      string
		nickname  *string
		wantValid bool
	}{
		{"nil 不修改昵称 → 通过", nil, true},
		{"空串 → 通过（govalidator 跳过空值校验；空由前端拦截）", strPtr(""), true},
		{"单字符通过", strPtr("a"), true},
		{"10 个 ASCII 字符通过", strPtr(strings.Repeat("a", 10)), true},
		{"11 个 ASCII 字符被拒", strPtr(strings.Repeat("a", 11)), false},
		{"10 个汉字通过（rune 计数）", strPtr("一二三四五六七八九十"), true},
		{"11 个汉字被拒（rune 计数）", strPtr("一二三四五六七八九十一"), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := UpdateUserProfileRequest{Nickname: tc.nickname}
			ok, err := govalidator.ValidateStruct(req)
			if ok != tc.wantValid {
				t.Errorf("ValidateStruct nickname=%v: got valid=%v (err=%v), want valid=%v",
					tc.nickname, ok, err, tc.wantValid)
			}
		})
	}
}
