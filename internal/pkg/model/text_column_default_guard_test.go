package model

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// textColumnDefaultRe 匹配单个 gorm tag 内同时含 TEXT-family 列类型 + 字面 default 的反模式。
//
// MySQL 不允许 BLOB/TEXT/JSON/GEOMETRY 列带字面 DEFAULT 值。带此类 tag 的 GORM 模型，
// 在 *已存在该表* 的库上 AutoMigrate 不重建、不报错；但在 *没有该表的全新库*（如 prod）
// 上首次 CREATE TABLE 会报：
//
//	Error 1101 (42000): BLOB, TEXT, GEOMETRY or JSON column 'xxx' can't have a default value
//
// → 服务启动失败。2026-06-02 部署 develop admin 后端到 prod 即因 agent_definition.system_prompt
// 的 tag 含 mediumtext + 字面 default（空串）触发此错回滚。
//
// 正则与人工巡检 grep 同源：
//
//	grep -rnoE 'gorm:"[^"]*(mediumtext|longtext|[^a-z]text)[^"]*default' internal/pkg/model/
//
// [^"]* 保证匹配落在单个 gorm:"..." tag 内（双引号不跨越）。[^a-z]text 匹配裸 text 类型
// （如 :text / ;text）而不重复命中 mediumtext/longtext。
var textColumnDefaultRe = regexp.MustCompile(`gorm:"[^"]*(mediumtext|longtext|[^a-z]text)[^"]*default`)

// TestModelTags_NoTextColumnHasLiteralDefault 扫描整个 model 包源码，断言没有任何
// TEXT-family（mediumtext/longtext/text）列带字面 default。这是 2026-06-02 prod
// 回滚事故的永久回归守卫——覆盖全包及将来新增的任何 model，无需维护结构体清单。
func TestModelTags_NoTextColumnHasLiteralDefault(t *testing.T) {
	// go test 以包源码目录为工作目录运行，"." 即 internal/pkg/model。
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read model dir: %v", err)
	}

	var violations []string
	scanned := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		scanned++
		data, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for i, line := range strings.Split(string(data), "\n") {
			if textColumnDefaultRe.MatchString(line) {
				violations = append(violations, name+":"+strconv.Itoa(i+1)+" — "+strings.TrimSpace(line))
			}
		}
	}

	if scanned == 0 {
		t.Fatal("scanned 0 model source files — guard would never catch anything; check working directory")
	}

	if len(violations) > 0 {
		t.Fatalf("TEXT/BLOB/JSON 列不能带字面 default（MySQL Error 1101，会导致全新库 AutoMigrate 失败）。\n"+
			"去掉 gorm tag 里的 ;default:... 即可（GORM 对非指针 string 字段仍会写空串 '' 满足 NOT NULL）。\n命中 %d 处：\n  %s",
			len(violations), strings.Join(violations, "\n  "))
	}
}
