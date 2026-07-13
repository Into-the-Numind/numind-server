package feishu

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCommandCatalog_AllowedPathsAndExactContracts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		argv   []string
		path   string
		domain string
		risk   RiskLevel
		scopes []string
		replay bool
	}{
		{name: "docs create", argv: []string{"docs", "+create", "--title", "Sales report"}, path: "docs +create", domain: "docs", risk: RiskWrite, scopes: []string{"docx:document:create"}},
		{name: "docs fetch", argv: []string{"docs", "+fetch", "--doc", "doxcnABCDEFG123"}, path: "docs +fetch", domain: "docs", risk: RiskRead, scopes: []string{"docx:document:readonly"}, replay: true},
		{name: "docs update", argv: []string{"docs", "+update", "--doc", "doxcnABCDEFG123", "--command", "append", "--content", "hello"}, path: "docs +update", domain: "docs", risk: RiskWrite, scopes: []string{"docx:document:write_only", "docx:document:readonly"}},

		{name: "base create", argv: []string{"base", "+base-create", "--name", "Pipeline"}, path: "base +base-create", domain: "base", risk: RiskWrite, scopes: []string{"base:app:create", "base:table:read", "base:table:create", "base:table:update", "base:table:delete"}},
		{name: "base get", argv: []string{"base", "+base-get", "--base-token", "bascnABCDEFG123"}, path: "base +base-get", domain: "base", risk: RiskRead, scopes: []string{"base:app:read"}, replay: true},
		{name: "table list", argv: []string{"base", "+table-list", "--base-token", "bascnABCDEFG123"}, path: "base +table-list", domain: "base", risk: RiskRead, scopes: []string{"base:table:read"}, replay: true},
		{name: "table get", argv: []string{"base", "+table-get", "--base-token", "bascnABCDEFG123", "--table-id", "tblABCDEFG123"}, path: "base +table-get", domain: "base", risk: RiskRead, scopes: []string{"base:table:read"}, replay: true},
		{name: "field list", argv: []string{"base", "+field-list", "--base-token", "bascnABCDEFG123", "--table-id", "Tasks"}, path: "base +field-list", domain: "base", risk: RiskRead, scopes: []string{"base:field:read"}, replay: true},
		{name: "field get", argv: []string{"base", "+field-get", "--base-token", "bascnABCDEFG123", "--table-id", "Tasks", "--field-id", "Status"}, path: "base +field-get", domain: "base", risk: RiskRead, scopes: []string{"base:field:read"}, replay: true},
		{name: "view list", argv: []string{"base", "+view-list", "--base-token", "bascnABCDEFG123", "--table-id", "Tasks"}, path: "base +view-list", domain: "base", risk: RiskRead, scopes: []string{"base:view:read"}, replay: true},
		{name: "view get", argv: []string{"base", "+view-get", "--base-token", "bascnABCDEFG123", "--table-id", "Tasks", "--view-id", "All"}, path: "base +view-get", domain: "base", risk: RiskRead, scopes: []string{"base:view:read"}, replay: true},
		{name: "record get", argv: []string{"base", "+record-get", "--base-token", "bascnABCDEFG123", "--table-id", "Tasks", "--record-id", "recABCDEFG123"}, path: "base +record-get", domain: "base", risk: RiskRead, scopes: []string{"base:record:read"}, replay: true},
		{name: "record list", argv: []string{"base", "+record-list", "--base-token", "bascnABCDEFG123", "--table-id", "Tasks"}, path: "base +record-list", domain: "base", risk: RiskRead, scopes: []string{"base:record:read"}, replay: true},
		{name: "record search", argv: []string{"base", "+record-search", "--base-token", "bascnABCDEFG123", "--table-id", "Tasks", "--keyword", "Alice", "--search-field", "Name"}, path: "base +record-search", domain: "base", risk: RiskRead, scopes: []string{"base:record:read"}, replay: true},
		{name: "table create", argv: []string{"base", "+table-create", "--base-token", "bascnABCDEFG123", "--name", "Tasks"}, path: "base +table-create", domain: "base", risk: RiskWrite, scopes: []string{"base:table:create"}},
		{name: "table update", argv: []string{"base", "+table-update", "--base-token", "bascnABCDEFG123", "--table-id", "Tasks", "--name", "Deals"}, path: "base +table-update", domain: "base", risk: RiskWrite, scopes: []string{"base:table:update"}},
		{name: "field create", argv: []string{"base", "+field-create", "--base-token", "bascnABCDEFG123", "--table-id", "Tasks", "--json", `{"name":"Status","type":"text"}`}, path: "base +field-create", domain: "base", risk: RiskWrite, scopes: []string{"base:field:create"}},
		{name: "field update", argv: []string{"base", "+field-update", "--base-token", "bascnABCDEFG123", "--table-id", "Tasks", "--field-id", "Status", "--json", `{"name":"Status","type":"text"}`}, path: "base +field-update", domain: "base", risk: RiskHigh, scopes: []string{"base:field:update"}},
		{name: "record batch create", argv: []string{"base", "+record-batch-create", "--base-token", "bascnABCDEFG123", "--table-id", "Tasks", "--json", `{"fields":["Name"],"rows":[["Alice"]]}`}, path: "base +record-batch-create", domain: "base", risk: RiskWrite, scopes: []string{"base:record:create"}},
		{name: "record upsert", argv: []string{"base", "+record-upsert", "--base-token", "bascnABCDEFG123", "--table-id", "Tasks", "--json", `{"Name":"Alice"}`}, path: "base +record-upsert", domain: "base", risk: RiskWrite, scopes: []string{"base:record:create", "base:record:update"}},
		{name: "record batch update", argv: []string{"base", "+record-batch-update", "--base-token", "bascnABCDEFG123", "--table-id", "Tasks", "--json", `{"record_id_list":["recABCDEFG123"],"patch":{"Status":"Done"}}`}, path: "base +record-batch-update", domain: "base", risk: RiskWrite, scopes: []string{"base:record:update"}},

		{name: "wiki space create", argv: []string{"wiki", "+space-create", "--name", "Sales"}, path: "wiki +space-create", domain: "wiki", risk: RiskWrite, scopes: []string{"wiki:space:write_only"}},
		{name: "wiki node create", argv: []string{"wiki", "+node-create", "--space-id", "my_library", "--title", "Playbook"}, path: "wiki +node-create", domain: "wiki", risk: RiskWrite, scopes: []string{"wiki:node:create", "wiki:node:read", "wiki:space:read"}},
		{name: "wiki node get", argv: []string{"wiki", "+node-get", "--node-token", "wikcnABCDEFG123"}, path: "wiki +node-get", domain: "wiki", risk: RiskRead, scopes: []string{"wiki:node:retrieve"}, replay: true},
		{name: "wiki node list", argv: []string{"wiki", "+node-list", "--space-id", "my_library"}, path: "wiki +node-list", domain: "wiki", risk: RiskRead, scopes: []string{"wiki:node:retrieve"}, replay: true},
	}

	catalog := NewCommandCatalog()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := catalog.Normalize(tt.argv, nil)
			require.NoError(t, err)
			require.Equal(t, tt.path, got.Path)
			require.Equal(t, tt.domain, got.Domain)
			require.Equal(t, tt.risk, got.Risk)
			require.Equal(t, tt.scopes, got.Scopes)
			require.Equal(t, tt.replay, got.ReplaySafeOnAuthError)
			require.Equal(t, []string{"--format", "json", "--as", "user"}, got.Argv[len(got.Argv)-4:])
			require.Nil(t, got.StdinJSON)
		})
	}
}

func TestCommandCatalog_PermanentDenials(t *testing.T) {
	t.Parallel()

	tests := [][]string{
		{"im", "+messages-send"},
		{"api", "post", "/x"},
		{"auth", "status"},
		{"config", "set", "x", "y"},
		{"docs", "+delete", "--doc", "doxcnABCDEFG123"},
		{"docs", "+update", "--doc", "doxcnABCDEFG123", "--command", "block_delete", "--block-id", "blkABCDEFG123"},
		{"docs", "+update", "--doc", "doxcnABCDEFG123", "--command", "block_copy_insert_after", "--block-id", "blkABCDEFG123", "--src-block-ids", "blkHIJKLMN123"},
		{"docs", "+update", "--doc", "doxcnABCDEFG123", "--command", "block_move_after", "--block-id", "blkABCDEFG123", "--src-block-ids", "blkHIJKLMN123"},
		{"base", "+record-delete", "--base-token", "bascnABCDEFG123"},
		{"base", "+table-remove", "--base-token", "bascnABCDEFG123"},
		{"wiki", "+delete-space", "--space-id", "123"},
		{"wiki", "+member-add", "--space-id", "123"},
		{"wiki", "+node-copy", "--node-token", "wikcnABCDEFG123"},
		{"wiki", "+move", "--node-token", "wikcnABCDEFG123"},
	}

	catalog := NewCommandCatalog()
	for _, argv := range tests {
		_, err := catalog.Normalize(argv, nil)
		require.ErrorIs(t, err, ErrCommandDenied, "%v", argv)
	}
}

func TestCommandCatalog_DeniesPlatformOwnedAndUnknownFlags(t *testing.T) {
	t.Parallel()

	tests := [][]string{
		{"docs", "+fetch", "--doc", "doxcnABCDEFG123", "--as", "bot"},
		{"docs", "+fetch", "--doc", "doxcnABCDEFG123", "--home", "/tmp/x"},
		{"docs", "+fetch", "--doc", "doxcnABCDEFG123", "--profile", "other"},
		{"docs", "+fetch", "--doc", "doxcnABCDEFG123", "--brand", "lark"},
		{"docs", "+fetch", "--doc", "doxcnABCDEFG123", "--yes"},
		{"docs", "+fetch", "--doc", "doxcnABCDEFG123", "--dry-run"},
		{"docs", "+fetch", "--doc", "doxcnABCDEFG123", "--jq", ".token"},
		{"docs", "+fetch", "--doc", "doxcnABCDEFG123", "--format", "pretty"},
		{"docs", "+fetch", "--doc", "doxcnABCDEFG123", "--unknown", "x"},
		{"docs", "+fetch", "doxcnABCDEFG123"},
		{"docs", "+fetch", "--doc", "doxcnABCDEFG123", "--doc", "doxcnHIJKLMN123"},
	}

	catalog := NewCommandCatalog()
	for _, argv := range tests {
		_, err := catalog.Normalize(argv, nil)
		require.Error(t, err, "%v", argv)
	}
}

func TestCommandCatalog_NormalizesEqualsSyntaxAndPreservesValues(t *testing.T) {
	t.Parallel()

	got, err := NewCommandCatalog().Normalize([]string{
		"docs", "+create",
		"--title=销售 分析",
		"--content", "第一行\n--这不是 flag",
		"--doc-format=markdown",
	}, nil)
	require.NoError(t, err)
	require.Equal(t, []string{
		"docs", "+create",
		"--title", "销售 分析",
		"--content", "第一行\n--这不是 flag",
		"--doc-format", "markdown",
		"--format", "json",
		"--as", "user",
	}, got.Argv)
}

func TestCommandCatalog_DeniesStdinAndFileIndirection(t *testing.T) {
	t.Parallel()

	catalog := NewCommandCatalog()
	_, err := catalog.Normalize([]string{"docs", "+create", "--content", "hello"}, []byte(`{"x":1}`))
	require.ErrorIs(t, err, ErrCommandInvalidArgument)

	for _, value := range []string{"-", "@payload.json"} {
		_, err = catalog.Normalize([]string{"docs", "+create", "--content", value}, nil)
		require.ErrorIs(t, err, ErrCommandInvalidArgument)
		_, err = catalog.Normalize([]string{"base", "+field-create", "--base-token", "bascnABCDEFG123", "--table-id", "Tasks", "--json", value}, nil)
		require.ErrorIs(t, err, ErrCommandInvalidArgument)
	}
}

func TestCommandCatalog_DocsArgumentAndRiskPolicy(t *testing.T) {
	t.Parallel()

	catalog := NewCommandCatalog()
	tests := []struct {
		name string
		argv []string
		risk RiskLevel
	}{
		{name: "append", argv: []string{"docs", "+update", "--doc", "doxcnABCDEFG123", "--command", "append", "--content", "hello"}, risk: RiskWrite},
		{name: "replace", argv: []string{"docs", "+update", "--doc", "doxcnABCDEFG123", "--command", "str_replace", "--pattern", "old", "--content", "new"}, risk: RiskWrite},
		{name: "insert", argv: []string{"docs", "+update", "--doc", "doxcnABCDEFG123", "--command", "block_insert_after", "--block-id", "blkABCDEFG123", "--content", "new"}, risk: RiskWrite},
		{name: "block replace", argv: []string{"docs", "+update", "--doc", "doxcnABCDEFG123", "--command", "block_replace", "--block-id", "blkABCDEFG123", "--content", "new"}, risk: RiskWrite},
		{name: "overwrite", argv: []string{"docs", "+update", "--doc", "doxcnABCDEFG123", "--command", "overwrite", "--content", "new"}, risk: RiskHigh},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := catalog.Normalize(tt.argv, nil)
			require.NoError(t, err)
			require.Equal(t, tt.risk, got.Risk)
		})
	}

	invalid := [][]string{
		{"docs", "+create"},
		{"docs", "+create", "--title", "x", "--parent-token", "fldcnABCDEFG123", "--parent-position", "my_library"},
		{"docs", "+fetch"},
		{"docs", "+fetch", "--doc", "doxcnABCDEFG123", "--scope", "keyword"},
		{"docs", "+fetch", "--doc", "doxcnABCDEFG123", "--scope", "section"},
		{"docs", "+fetch", "--doc", "doxcnABCDEFG123", "--scope", "range"},
		{"docs", "+fetch", "--doc", "doxcnABCDEFG123", "--scope", "full", "--limit", "10"},
		{"docs", "+update", "--doc", "doxcnABCDEFG123", "--command", "append"},
		{"docs", "+update", "--doc", "doxcnABCDEFG123", "--command", "str_replace", "--pattern", "old", "--content", ""},
		{"docs", "+update", "--doc", "doxcnABCDEFG123", "--command", "str_replace", "--content", "new"},
		{"docs", "+update", "--doc", "doxcnABCDEFG123", "--command", "block_replace", "--content", "new"},
	}
	for _, argv := range invalid {
		_, err := catalog.Normalize(argv, nil)
		require.Error(t, err, "%v", argv)
	}
}

func TestCommandCatalog_URLAndOpaqueTokenPolicy(t *testing.T) {
	t.Parallel()

	catalog := NewCommandCatalog()
	valid := [][]string{
		{"docs", "+fetch", "--doc", "https://acme.feishu.cn/docx/doxcnABCDEFG123"},
		{"docs", "+fetch", "--doc", "https://acme.larksuite.com/wiki/wikcnABCDEFG123"},
		{"docs", "+fetch", "--doc", "https://www.doubao.com/docx/doxcnABCDEFG123"},
		{"base", "+base-get", "--base-token", "https://acme.feishu.cn/base/bascnABCDEFG123?table=tblABCDEFG123"},
		{"wiki", "+node-get", "--node-token", "https://acme.feishu.cn/wiki/wikcnABCDEFG123"},
		{"wiki", "+node-get", "--node-token", "https://acme.feishu.cn/docx/doxcnABCDEFG123", "--obj-type", "docx"},
	}
	for _, argv := range valid {
		_, err := catalog.Normalize(argv, nil)
		require.NoError(t, err, "%v", argv)
	}
	baseURL, err := catalog.Normalize(valid[3], nil)
	require.NoError(t, err)
	require.Contains(t, baseURL.Argv, "bascnABCDEFG123")
	require.NotContains(t, baseURL.Argv, valid[3][3])

	invalid := [][]string{
		{"docs", "+fetch", "--doc", "http://acme.feishu.cn/docx/doxcnABCDEFG123"},
		{"docs", "+fetch", "--doc", "https://feishu.cn.evil.example/docx/doxcnABCDEFG123"},
		{"docs", "+fetch", "--doc", "https://user@acme.feishu.cn/docx/doxcnABCDEFG123"},
		{"docs", "+fetch", "--doc", "https://acme.feishu.cn:443/docx/doxcnABCDEFG123"},
		{"docs", "+fetch", "--doc", "https://acme.feishu.cn/docx/%2e%2e%2fsecret"},
		{"docs", "+fetch", "--doc", "https://acme.feishu.cn//docx/doxcnABCDEFG123"},
		{"docs", "+fetch", "--doc", "https://acme.feishu.cn/docx/doxcnABCDEFG123/"},
		{"docs", "+fetch", "--doc", "https://acme.feishu.cn/open-apis/docx/doxcnABCDEFG123"},
		{"base", "+base-get", "--base-token", "https://acme.feishu.cn/docx/doxcnABCDEFG123"},
		{"wiki", "+node-get", "--node-token", "https://acme.feishu.cn/sheets/shtcnABCDEFG123"},
		{"docs", "+fetch", "--doc", "short"},
	}
	for _, argv := range invalid {
		_, err := catalog.Normalize(argv, nil)
		require.Error(t, err, "%v", argv)
	}
}

func TestCommandCatalog_BaseRecordLimitsAndJSONShapes(t *testing.T) {
	t.Parallel()

	catalog := NewCommandCatalog()
	createJSON := func(rows int) string {
		payload := map[string]any{"fields": []string{"Name"}, "rows": make([][]any, rows)}
		for i := range payload["rows"].([][]any) {
			payload["rows"].([][]any)[i] = []any{"Alice"}
		}
		encoded, err := json.Marshal(payload)
		require.NoError(t, err)
		return string(encoded)
	}
	updateJSON := func(records int) string {
		ids := make([]string, records)
		for i := range ids {
			ids[i] = "recABCDEFG" + string(rune('A'+i%26)) + "123"
		}
		encoded, err := json.Marshal(map[string]any{"record_id_list": ids, "patch": map[string]any{"Status": "Done"}})
		require.NoError(t, err)
		return string(encoded)
	}

	for _, tc := range []struct {
		path string
		json string
		risk RiskLevel
	}{
		{path: "+record-batch-create", json: createJSON(20), risk: RiskWrite},
		{path: "+record-batch-create", json: createJSON(21), risk: RiskHigh},
		{path: "+record-batch-create", json: createJSON(200), risk: RiskHigh},
		{path: "+record-batch-update", json: updateJSON(20), risk: RiskWrite},
		{path: "+record-batch-update", json: updateJSON(21), risk: RiskHigh},
		{path: "+record-batch-update", json: updateJSON(200), risk: RiskHigh},
	} {
		got, err := catalog.Normalize([]string{"base", tc.path, "--base-token", "bascnABCDEFG123", "--table-id", "Tasks", "--json", tc.json}, nil)
		require.NoError(t, err)
		require.Equal(t, tc.risk, got.Risk)
	}

	invalid := [][]string{
		{"base", "+record-batch-create", "--base-token", "bascnABCDEFG123", "--table-id", "Tasks", "--json", createJSON(0)},
		{"base", "+record-batch-create", "--base-token", "bascnABCDEFG123", "--table-id", "Tasks", "--json", createJSON(201)},
		{"base", "+record-batch-create", "--base-token", "bascnABCDEFG123", "--table-id", "Tasks", "--json", `{"fields":["Name","Status"],"rows":[["Alice"]]}`},
		{"base", "+record-batch-update", "--base-token", "bascnABCDEFG123", "--table-id", "Tasks", "--json", updateJSON(0)},
		{"base", "+record-batch-update", "--base-token", "bascnABCDEFG123", "--table-id", "Tasks", "--json", updateJSON(201)},
		{"base", "+record-batch-update", "--base-token", "bascnABCDEFG123", "--table-id", "Tasks", "--json", `{"record_id_list":["recABCDEFG123"],"patch":{}}`},
		{"base", "+record-upsert", "--base-token", "bascnABCDEFG123", "--table-id", "Tasks", "--json", `{}`},
		{"base", "+record-upsert", "--base-token", "bascnABCDEFG123", "--table-id", "Tasks", "--json", `[]`},
	}
	for _, argv := range invalid {
		_, err := catalog.Normalize(argv, nil)
		require.Error(t, err, "%v", argv)
	}
}

func TestCommandCatalog_BaseReadPaginationAndRepeatableFlags(t *testing.T) {
	t.Parallel()

	catalog := NewCommandCatalog()
	got, err := catalog.Normalize([]string{
		"base", "+record-search", "--base-token", "bascnABCDEFG123", "--table-id", "Tasks",
		"--keyword", "Alice", "--search-field", "Name", "--search-field=Email",
		"--field-id", "Name", "--field-id=Status", "--limit", "100", "--offset", "0",
	}, nil)
	require.NoError(t, err)
	require.Contains(t, got.Argv, "Email")

	invalid := [][]string{
		{"base", "+table-list", "--base-token", "bascnABCDEFG123", "--limit", "0"},
		{"base", "+table-list", "--base-token", "bascnABCDEFG123", "--limit", "101"},
		{"base", "+field-list", "--base-token", "bascnABCDEFG123", "--table-id", "Tasks", "--offset", "-1"},
		{"base", "+record-list", "--base-token", "bascnABCDEFG123", "--table-id", "Tasks", "--limit", "200"},
		{"base", "+record-search", "--base-token", "bascnABCDEFG123", "--table-id", "Tasks", "--keyword", "Alice"},
		{"base", "+record-search", "--base-token", "bascnABCDEFG123", "--table-id", "Tasks", "--keyword", "Alice", "--search-field", "Name", "--json", `{"keyword":"Alice","search_fields":["Name"]}`},
		{"base", "+record-list", "--base-token", "bascnABCDEFG123", "--table-id", "Tasks", "--sort-json", `[{"field":"A"},{"field":"B"},{"field":"C"},{"field":"D"},{"field":"E"},{"field":"F"},{"field":"G"},{"field":"H"},{"field":"I"},{"field":"J"},{"field":"K"}]`},
	}
	for _, argv := range invalid {
		_, err := catalog.Normalize(argv, nil)
		require.Error(t, err, "%v", argv)
	}
}

func TestCommandCatalog_BaseSchemaAndWikiConstraints(t *testing.T) {
	t.Parallel()

	catalog := NewCommandCatalog()
	invalid := [][]string{
		{"base", "+base-create", "--name", "Pipeline", "--table-name", "Tasks"},
		{"base", "+base-create", "--name", "Pipeline", "--fields", `[{"name":"Title","type":"text"}]`},
		{"base", "+field-create", "--base-token", "bascnABCDEFG123", "--table-id", "Tasks", "--json", `[]`},
		{"base", "+field-update", "--base-token", "bascnABCDEFG123", "--table-id", "Tasks", "--field-id", "Status", "--json", `{"name":"Status","type":"text"}`, "--yes"},
		{"wiki", "+space-create"},
		{"wiki", "+node-create", "--space-id", "my_library", "--node-type", "shortcut", "--origin-node-token", "wikcnABCDEFG123"},
		{"wiki", "+node-create", "--space-id", "my_library", "--obj-type", "sheet"},
		{"wiki", "+node-create", "--space-id", "my_library", "--origin-node-token", "wikcnABCDEFG123"},
		{"wiki", "+node-get", "--node-token", "wikcnABCDEFG123", "--obj-type", "sheet"},
		{"wiki", "+node-list", "--space-id", "my_library", "--page-all", "--page-limit", "0"},
		{"wiki", "+node-list", "--space-id", "my_library", "--page-all", "--page-limit", "11"},
		{"wiki", "+node-list", "--space-id", "my_library", "--page-token", "pagcnABCDEFG123", "--page-all"},
		{"wiki", "+node-list", "--space-id", "my_library", "--page-limit", "5"},
	}
	for _, argv := range invalid {
		_, err := catalog.Normalize(argv, nil)
		require.Error(t, err, "%v", argv)
	}

	got, err := catalog.Normalize([]string{"wiki", "+node-list", "--space-id", "my_library", "--page-all=true", "--page-limit=5", "--page-size=50"}, nil)
	require.NoError(t, err)
	require.Contains(t, got.Argv, "--page-all")
}

func TestCommandCatalogManifest_1068(t *testing.T) {
	t.Parallel()

	got, err := json.MarshalIndent(NewCommandCatalog().manifest(), "", "  ")
	require.NoError(t, err)
	got = append(got, '\n')

	want, err := os.ReadFile(filepath.Join("testdata", "command_catalog_1.0.68.json"))
	require.NoError(t, err)
	require.Equal(t, string(want), string(got))
}
