package feishu

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	// CommandCatalogMaxContentBytes bounds document bodies before they reach argv.
	CommandCatalogMaxContentBytes = 512 << 10
	// CommandCatalogMaxJSONBytes bounds any structured flag payload.
	CommandCatalogMaxJSONBytes = 256 << 10
	// CommandCatalogMaxReadPageSize is the product-safe read page ceiling.
	CommandCatalogMaxReadPageSize = 100
	// CommandCatalogMaxWikiPageSize follows the fixed CLI's Wiki API ceiling.
	CommandCatalogMaxWikiPageSize = 50
	// CommandCatalogMaxWikiPages prevents page-all from becoming unbounded.
	CommandCatalogMaxWikiPages = 10
	// CommandCatalogMaxRecordWriteBatch is the no-confirmation write threshold.
	CommandCatalogMaxRecordWriteBatch = 20
	// CommandCatalogCLIRecordWriteBatchMax is lark-cli 1.0.68's hard ceiling.
	CommandCatalogCLIRecordWriteBatchMax = 200
	// CommandCatalogMaxRecordReadBatch bounds projected record reads.
	CommandCatalogMaxRecordReadBatch = 100
	// CommandCatalogMaxProjectedFields bounds output width.
	CommandCatalogMaxProjectedFields = 50
	// CommandCatalogMaxSearchFields follows the CLI search contract.
	CommandCatalogMaxSearchFields = 20
	// CommandCatalogMaxSortFields follows the CLI sort contract.
	CommandCatalogMaxSortFields = 10
	// CommandCatalogMaxBaseFields bounds a schema supplied in one command.
	CommandCatalogMaxBaseFields = 100
	// CommandCatalogMaxStdoutBytes records the controlled runner response ceiling.
	CommandCatalogMaxStdoutBytes = ControlledLarkCLIMaxStdoutBytes

	commandCatalogMaxTitleBytes      = 512
	commandCatalogMaxNameBytes       = 512
	commandCatalogMaxDescription     = 4 << 10
	commandCatalogMaxPatternBytes    = 64 << 10
	commandCatalogMaxFilterJSONBytes = 64 << 10
	commandCatalogMaxOffset          = 100000
	commandCatalogMaxJSONDepth       = 16
)

var (
	// ErrCommandDenied means a command path, flag, or capability is outside the
	// server-owned allowlist. Callers must not retry it through another CLI path.
	ErrCommandDenied = errors.New("feishu command denied")
	// ErrCommandInvalidArgument means an allowed command has malformed or
	// unsafe arguments. It is distinct from a policy denial for user feedback.
	ErrCommandInvalidArgument = errors.New("feishu command invalid argument")
)

// RiskLevel is computed solely by the catalog, never accepted from the model.
type RiskLevel string

const (
	RiskRead  RiskLevel = "read"
	RiskWrite RiskLevel = "write"
	RiskHigh  RiskLevel = "high"
)

// NormalizedCommand is the only business-command shape accepted by the
// operation layer. Argv contains no binary path and always ends in JSON output
// plus the platform-owned user identity.
type NormalizedCommand struct {
	Path                  string
	Domain                string
	Action                string
	Risk                  RiskLevel
	Scopes                []string
	Argv                  []string
	StdinJSON             []byte
	ReplaySafeOnAuthError bool
}

var docsScopes = map[string][]string{
	"docs +create": {"docx:document:create"},
	"docs +fetch":  {"docx:document:readonly"},
	"docs +update": {"docx:document:write_only", "docx:document:readonly"},
}

var baseScopes = map[string][]string{
	"base +base-create":         {"base:app:create", "base:table:read", "base:table:create", "base:table:update", "base:table:delete"},
	"base +base-get":            {"base:app:read"},
	"base +table-list":          {"base:table:read"},
	"base +table-get":           {"base:table:read"},
	"base +field-list":          {"base:field:read"},
	"base +field-get":           {"base:field:read"},
	"base +view-list":           {"base:view:read"},
	"base +view-get":            {"base:view:read"},
	"base +record-get":          {"base:record:read"},
	"base +record-list":         {"base:record:read"},
	"base +record-search":       {"base:record:read"},
	"base +table-create":        {"base:table:create"},
	"base +table-update":        {"base:table:update"},
	"base +field-create":        {"base:field:create"},
	"base +field-update":        {"base:field:update"},
	"base +record-batch-create": {"base:record:create"},
	"base +record-upsert":       {"base:record:create", "base:record:update"},
	"base +record-batch-update": {"base:record:update"},
}

var wikiScopes = map[string][]string{
	"wiki +space-create": {"wiki:space:write_only"},
	"wiki +node-create":  {"wiki:node:create", "wiki:node:read", "wiki:space:read"},
	"wiki +node-get":     {"wiki:node:retrieve"},
	"wiki +node-list":    {"wiki:node:retrieve"},
}

type flagRule struct {
	boolean    bool
	repeatable bool
	normalize  func(string) (string, error)
}

type parsedFlags struct {
	values map[string][]string
	argv   []string
}

func (p *parsedFlags) has(name string) bool { return len(p.values[name]) > 0 }
func (p *parsedFlags) one(name string) string {
	values := p.values[name]
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

type commandSpec struct {
	path     string
	domain   string
	action   string
	risk     RiskLevel
	scopes   []string
	flags    map[string]flagRule
	limits   map[string]int
	validate func(*parsedFlags) (RiskLevel, error)
}

// CommandCatalog is immutable after construction and safe for concurrent use.
type CommandCatalog struct {
	specs map[string]commandSpec
}

// NewCommandCatalog returns the fixed lark-cli 1.0.68 business command policy.
func NewCommandCatalog() *CommandCatalog {
	c := &CommandCatalog{specs: make(map[string]commandSpec)}
	c.addDocsSpecs()
	c.addBaseSpecs()
	c.addWikiSpecs()
	return c
}

// Normalize parses and validates a model-supplied argv without invoking a
// shell. Platform-owned output and identity flags are appended after all input
// validation. Non-empty stdin is intentionally closed in V1: no allowed
// business command needs stdin, and file/stdin indirection is forbidden.
func (c *CommandCatalog) Normalize(argv []string, stdinJSON []byte) (*NormalizedCommand, error) {
	if c == nil || len(argv) < 2 {
		return nil, deniedf("missing command path")
	}
	if len(stdinJSON) != 0 {
		return nil, invalidf("stdin is not supported by the command catalog")
	}
	if strings.ContainsRune(argv[0], 0) || strings.ContainsRune(argv[1], 0) {
		return nil, invalidf("command path contains NUL")
	}

	path := argv[0] + " " + argv[1]
	spec, ok := c.specs[path]
	if !ok {
		return nil, deniedf("command path is not registered")
	}
	parsed, err := parseCommandFlags(argv[2:], spec.flags)
	if err != nil {
		return nil, err
	}
	risk := spec.risk
	if spec.validate != nil {
		risk, err = spec.validate(parsed)
		if err != nil {
			return nil, err
		}
	}

	normalizedArgv := make([]string, 0, 2+len(parsed.argv)+4)
	normalizedArgv = append(normalizedArgv, argv[0], argv[1])
	normalizedArgv = append(normalizedArgv, parsed.argv...)
	normalizedArgv = append(normalizedArgv, "--format", "json", "--as", "user")

	return &NormalizedCommand{
		Path:                  spec.path,
		Domain:                spec.domain,
		Action:                spec.action,
		Risk:                  risk,
		Scopes:                append([]string(nil), spec.scopes...),
		Argv:                  normalizedArgv,
		StdinJSON:             nil,
		ReplaySafeOnAuthError: risk == RiskRead,
	}, nil
}

func parseCommandFlags(args []string, rules map[string]flagRule) (*parsedFlags, error) {
	parsed := &parsedFlags{values: make(map[string][]string), argv: make([]string, 0, len(args))}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.ContainsRune(arg, 0) {
			return nil, invalidf("argument contains NUL")
		}
		if !strings.HasPrefix(arg, "--") || arg == "--" {
			return nil, invalidf("positional and short arguments are not supported")
		}
		nameValue := strings.TrimPrefix(arg, "--")
		name, value, hasEquals := strings.Cut(nameValue, "=")
		if name == "" {
			return nil, invalidf("empty flag")
		}
		rule, ok := rules[name]
		if !ok {
			return nil, deniedf("flag --%s is not allowed for this command", name)
		}
		if len(parsed.values[name]) > 0 && !rule.repeatable {
			return nil, invalidf("duplicate flag --%s", name)
		}

		if rule.boolean {
			if hasEquals && value != "true" {
				return nil, invalidf("boolean flag --%s only accepts true", name)
			}
			parsed.values[name] = append(parsed.values[name], "true")
			parsed.argv = append(parsed.argv, "--"+name)
			continue
		}

		if !hasEquals {
			if i+1 >= len(args) {
				return nil, invalidf("flag --%s is missing its value", name)
			}
			i++
			value = args[i]
		}
		if strings.ContainsRune(value, 0) {
			return nil, invalidf("flag --%s contains NUL", name)
		}
		if rule.normalize != nil {
			var err error
			value, err = rule.normalize(value)
			if err != nil {
				return nil, err
			}
		}
		parsed.values[name] = append(parsed.values[name], value)
		parsed.argv = append(parsed.argv, "--"+name, value)
	}
	return parsed, nil
}

func (c *CommandCatalog) register(spec commandSpec) {
	if _, exists := c.specs[spec.path]; exists {
		panic("duplicate feishu command catalog path: " + spec.path)
	}
	c.specs[spec.path] = spec
}

func (c *CommandCatalog) addDocsSpecs() {
	docRef := valueRule(normalizeSupportedRef("document", map[string]bool{"docx": true, "wiki": true}, false))
	token := valueRule(normalizeOpaqueToken("token"))
	content := valueRule(normalizeInlineText("content", CommandCatalogMaxContentBytes, true))
	jsonObject := valueRule(normalizeJSONObject("JSON", CommandCatalogMaxJSONBytes))

	c.register(commandSpec{
		path: "docs +create", domain: "docs", action: "create", risk: RiskWrite, scopes: docsScopes["docs +create"],
		flags: map[string]flagRule{
			"title":           valueRule(normalizeInlineText("title", commandCatalogMaxTitleBytes, false)),
			"content":         content,
			"doc-format":      valueRule(normalizeEnum("doc-format", "xml", "markdown")),
			"parent-token":    token,
			"parent-position": valueRule(normalizeEnum("parent-position", "my_library")),
			"reference-map":   jsonObject,
		},
		limits: map[string]int{"content_bytes": CommandCatalogMaxContentBytes, "json_bytes": CommandCatalogMaxJSONBytes, "stdout_bytes": CommandCatalogMaxStdoutBytes},
		validate: func(p *parsedFlags) (RiskLevel, error) {
			if !p.has("title") && !p.has("content") {
				return "", invalidf("docs create requires title or content")
			}
			if p.has("parent-token") && p.has("parent-position") {
				return "", invalidf("parent-token and parent-position are mutually exclusive")
			}
			if p.has("reference-map") && !p.has("content") {
				return "", invalidf("reference-map requires content")
			}
			return RiskWrite, nil
		},
	})

	c.register(commandSpec{
		path: "docs +fetch", domain: "docs", action: "fetch", risk: RiskRead, scopes: docsScopes["docs +fetch"],
		flags: map[string]flagRule{
			"doc":            docRef,
			"doc-format":     valueRule(normalizeEnum("doc-format", "xml", "markdown", "im-markdown")),
			"detail":         valueRule(normalizeEnum("detail", "simple", "with-ids", "full")),
			"revision-id":    valueRule(normalizeInt("revision-id", -1, 1<<31-1)),
			"scope":          valueRule(normalizeEnum("scope", "full", "outline", "range", "keyword", "section")),
			"start-block-id": valueRule(normalizeBlockID("start-block-id", false)),
			"end-block-id":   valueRule(normalizeBlockID("end-block-id", true)),
			"keyword":        valueRule(normalizeInlineText("keyword", 4<<10, false)),
			"context-before": valueRule(normalizeInt("context-before", 0, 10)),
			"context-after":  valueRule(normalizeInt("context-after", 0, 10)),
			"max-depth":      valueRule(normalizeInt("max-depth", -1, 10)),
			"lang":           valueRule(normalizeLanguage),
		},
		limits:   map[string]int{"context_blocks": 10, "depth": 10, "stdout_bytes": CommandCatalogMaxStdoutBytes},
		validate: validateDocsFetch,
	})

	c.register(commandSpec{
		path: "docs +update", domain: "docs", action: "update", risk: RiskWrite, scopes: docsScopes["docs +update"],
		flags: map[string]flagRule{
			"doc":           docRef,
			"command":       valueRule(normalizeInlineText("command", 64, false)),
			"content":       content,
			"doc-format":    valueRule(normalizeEnum("doc-format", "xml", "markdown")),
			"reference-map": jsonObject,
			"pattern":       valueRule(normalizeInlineText("pattern", commandCatalogMaxPatternBytes, true)),
			"block-id":      valueRule(normalizeBlockID("block-id", true)),
			"revision-id":   valueRule(normalizeInt("revision-id", -1, 1<<31-1)),
		},
		limits:   map[string]int{"content_bytes": CommandCatalogMaxContentBytes, "json_bytes": CommandCatalogMaxJSONBytes, "stdout_bytes": CommandCatalogMaxStdoutBytes},
		validate: validateDocsUpdate,
	})
}

func validateDocsFetch(p *parsedFlags) (RiskLevel, error) {
	if err := requireFlags(p, "doc"); err != nil {
		return "", err
	}
	scope := p.one("scope")
	if scope == "" {
		scope = "full"
	}
	switch scope {
	case "keyword":
		if !p.has("keyword") {
			return "", invalidf("keyword scope requires keyword")
		}
	case "section":
		if !p.has("start-block-id") {
			return "", invalidf("section scope requires start-block-id")
		}
	case "range":
		if !p.has("start-block-id") && !p.has("end-block-id") {
			return "", invalidf("range scope requires a block boundary")
		}
	case "full", "outline":
	default:
		return "", invalidf("invalid docs fetch scope")
	}
	if p.has("keyword") && scope != "keyword" {
		return "", invalidf("keyword flag requires keyword scope")
	}
	if (p.has("start-block-id") || p.has("end-block-id")) && scope != "range" && scope != "section" {
		return "", invalidf("block boundaries require range or section scope")
	}
	if p.has("end-block-id") && scope != "range" {
		return "", invalidf("end-block-id requires range scope")
	}
	if (p.has("context-before") || p.has("context-after")) && scope != "range" && scope != "keyword" && scope != "section" {
		return "", invalidf("context flags require a scoped read")
	}
	return RiskRead, nil
}

func validateDocsUpdate(p *parsedFlags) (RiskLevel, error) {
	if err := requireFlags(p, "doc", "command"); err != nil {
		return "", err
	}
	if p.has("reference-map") && !p.has("content") {
		return "", invalidf("reference-map requires content")
	}
	command := p.one("command")
	switch command {
	case "append":
		if err := requireNonEmptyFlags(p, "content"); err != nil {
			return "", err
		}
		if p.has("pattern") || p.has("block-id") {
			return "", invalidf("append received unrelated selector flags")
		}
		return RiskWrite, nil
	case "overwrite":
		if err := requireNonEmptyFlags(p, "content"); err != nil {
			return "", err
		}
		if p.has("pattern") || p.has("block-id") {
			return "", invalidf("overwrite received unrelated selector flags")
		}
		return RiskHigh, nil
	case "str_replace":
		if err := requireNonEmptyFlags(p, "pattern", "content"); err != nil {
			return "", err
		}
		if p.has("block-id") {
			return "", invalidf("str_replace does not accept block-id")
		}
		return RiskWrite, nil
	case "block_insert_after", "block_replace":
		if err := requireNonEmptyFlags(p, "block-id", "content"); err != nil {
			return "", err
		}
		if p.has("pattern") {
			return "", invalidf("block operation does not accept pattern")
		}
		return RiskWrite, nil
	case "block_delete", "block_copy_insert_after", "block_move_after":
		return "", deniedf("destructive or move document command is forbidden")
	default:
		return "", deniedf("document update command is not allowed")
	}
}

func (c *CommandCatalog) addBaseSpecs() {
	baseToken := valueRule(normalizeSupportedRef("base-token", map[string]bool{"base": true, "bitable": true}, true))
	idOrName := valueRule(normalizeIDOrName)
	recordID := valueRule(normalizePrefixedID("record-id", "rec"))
	jsonPayload := valueRule(normalizeJSON("JSON", CommandCatalogMaxJSONBytes))
	filterJSON := valueRule(normalizeJSONObject("filter-json", commandCatalogMaxFilterJSONBytes))
	sortJSON := valueRule(normalizeJSON("sort-json", commandCatalogMaxFilterJSONBytes))
	readLimit := valueRule(normalizeInt("limit", 1, CommandCatalogMaxReadPageSize))
	offset := valueRule(normalizeInt("offset", 0, commandCatalogMaxOffset))

	c.register(commandSpec{
		path: "base +base-create", domain: "base", action: "base-create", risk: RiskWrite, scopes: baseScopes["base +base-create"],
		flags: map[string]flagRule{
			"name":         valueRule(normalizeInlineText("name", commandCatalogMaxNameBytes, false)),
			"time-zone":    valueRule(normalizeTimeZone),
			"folder-token": valueRule(normalizeOpaqueToken("folder-token")),
			"table-name":   valueRule(normalizeInlineText("table-name", commandCatalogMaxNameBytes, false)),
			"fields":       jsonPayload,
		},
		limits: map[string]int{"fields": CommandCatalogMaxBaseFields, "json_bytes": CommandCatalogMaxJSONBytes, "stdout_bytes": CommandCatalogMaxStdoutBytes},
		validate: func(p *parsedFlags) (RiskLevel, error) {
			if err := requireFlags(p, "name"); err != nil {
				return "", err
			}
			if p.has("table-name") != p.has("fields") {
				return "", invalidf("table-name and fields must be supplied together")
			}
			if p.has("fields") {
				if err := validateFieldArray(p.one("fields")); err != nil {
					return "", err
				}
			}
			return RiskWrite, nil
		},
	})

	c.register(simpleBaseSpec("base +base-get", "base-get", RiskRead, baseScopes["base +base-get"], map[string]flagRule{"base-token": baseToken}, []string{"base-token"}))
	c.register(simpleBaseListSpec("base +table-list", "table-list", baseScopes["base +table-list"], map[string]flagRule{"base-token": baseToken, "limit": readLimit, "offset": offset}, []string{"base-token"}))
	c.register(simpleBaseSpec("base +table-get", "table-get", RiskRead, baseScopes["base +table-get"], map[string]flagRule{"base-token": baseToken, "table-id": idOrName}, []string{"base-token", "table-id"}))
	c.register(simpleBaseListSpec("base +field-list", "field-list", baseScopes["base +field-list"], map[string]flagRule{"base-token": baseToken, "table-id": idOrName, "limit": readLimit, "offset": offset}, []string{"base-token", "table-id"}))
	c.register(simpleBaseSpec("base +field-get", "field-get", RiskRead, baseScopes["base +field-get"], map[string]flagRule{"base-token": baseToken, "table-id": idOrName, "field-id": idOrName}, []string{"base-token", "table-id", "field-id"}))
	c.register(simpleBaseListSpec("base +view-list", "view-list", baseScopes["base +view-list"], map[string]flagRule{"base-token": baseToken, "table-id": idOrName, "limit": readLimit, "offset": offset}, []string{"base-token", "table-id"}))
	c.register(simpleBaseSpec("base +view-get", "view-get", RiskRead, baseScopes["base +view-get"], map[string]flagRule{"base-token": baseToken, "table-id": idOrName, "view-id": idOrName}, []string{"base-token", "table-id", "view-id"}))

	c.register(commandSpec{
		path: "base +record-get", domain: "base", action: "record-get", risk: RiskRead, scopes: baseScopes["base +record-get"],
		flags: map[string]flagRule{
			"base-token": baseToken,
			"table-id":   idOrName,
			"record-id":  repeatable(recordID),
			"field-id":   repeatable(idOrName),
			"json":       jsonPayload,
		},
		limits:   map[string]int{"projected_fields": CommandCatalogMaxProjectedFields, "records": CommandCatalogMaxRecordReadBatch, "stdout_bytes": CommandCatalogMaxStdoutBytes},
		validate: validateRecordGet,
	})

	recordReadFlags := map[string]flagRule{
		"base-token":  baseToken,
		"table-id":    idOrName,
		"field-id":    repeatable(idOrName),
		"view-id":     idOrName,
		"filter-json": filterJSON,
		"sort-json":   sortJSON,
		"offset":      offset,
		"limit":       readLimit,
	}
	c.register(commandSpec{
		path: "base +record-list", domain: "base", action: "record-list", risk: RiskRead, scopes: baseScopes["base +record-list"],
		flags:    recordReadFlags,
		limits:   map[string]int{"page_size": CommandCatalogMaxReadPageSize, "projected_fields": CommandCatalogMaxProjectedFields, "sort_fields": CommandCatalogMaxSortFields, "stdout_bytes": CommandCatalogMaxStdoutBytes},
		validate: validateRecordList,
	})

	searchFlags := cloneFlagRules(recordReadFlags)
	searchFlags["keyword"] = valueRule(normalizeInlineText("keyword", 4<<10, false))
	searchFlags["search-field"] = repeatable(idOrName)
	searchFlags["json"] = jsonPayload
	c.register(commandSpec{
		path: "base +record-search", domain: "base", action: "record-search", risk: RiskRead, scopes: baseScopes["base +record-search"],
		flags:    searchFlags,
		limits:   map[string]int{"page_size": CommandCatalogMaxReadPageSize, "projected_fields": CommandCatalogMaxProjectedFields, "search_fields": CommandCatalogMaxSearchFields, "sort_fields": CommandCatalogMaxSortFields, "stdout_bytes": CommandCatalogMaxStdoutBytes},
		validate: validateRecordSearch,
	})

	c.register(commandSpec{
		path: "base +table-create", domain: "base", action: "table-create", risk: RiskWrite, scopes: baseScopes["base +table-create"],
		flags:  map[string]flagRule{"base-token": baseToken, "name": valueRule(normalizeInlineText("name", commandCatalogMaxNameBytes, false)), "fields": jsonPayload, "view": jsonPayload},
		limits: map[string]int{"fields": CommandCatalogMaxBaseFields, "json_bytes": CommandCatalogMaxJSONBytes, "stdout_bytes": CommandCatalogMaxStdoutBytes},
		validate: func(p *parsedFlags) (RiskLevel, error) {
			if err := requireFlags(p, "base-token", "name"); err != nil {
				return "", err
			}
			if p.has("fields") {
				if err := validateFieldArray(p.one("fields")); err != nil {
					return "", err
				}
			}
			if p.has("view") {
				if err := validateJSONObjectOrArray(p.one("view"), "view", CommandCatalogMaxBaseFields); err != nil {
					return "", err
				}
			}
			return RiskWrite, nil
		},
	})
	c.register(simpleBaseSpec("base +table-update", "table-update", RiskWrite, baseScopes["base +table-update"], map[string]flagRule{"base-token": baseToken, "table-id": idOrName, "name": valueRule(normalizeInlineText("name", commandCatalogMaxNameBytes, false))}, []string{"base-token", "table-id", "name"}))

	fieldFlags := map[string]flagRule{"base-token": baseToken, "table-id": idOrName, "json": jsonPayload}
	c.register(commandSpec{
		path: "base +field-create", domain: "base", action: "field-create", risk: RiskWrite, scopes: baseScopes["base +field-create"],
		flags:  fieldFlags,
		limits: map[string]int{"json_bytes": CommandCatalogMaxJSONBytes, "stdout_bytes": CommandCatalogMaxStdoutBytes},
		validate: func(p *parsedFlags) (RiskLevel, error) {
			if err := requireFlags(p, "base-token", "table-id", "json"); err != nil {
				return "", err
			}
			if err := validateNonEmptyObject(p.one("json"), "field"); err != nil {
				return "", err
			}
			return RiskWrite, nil
		},
	})
	fieldUpdateFlags := cloneFlagRules(fieldFlags)
	fieldUpdateFlags["field-id"] = idOrName
	c.register(commandSpec{
		path: "base +field-update", domain: "base", action: "field-update", risk: RiskHigh, scopes: baseScopes["base +field-update"],
		flags:  fieldUpdateFlags,
		limits: map[string]int{"json_bytes": CommandCatalogMaxJSONBytes, "stdout_bytes": CommandCatalogMaxStdoutBytes},
		validate: func(p *parsedFlags) (RiskLevel, error) {
			if err := requireFlags(p, "base-token", "table-id", "field-id", "json"); err != nil {
				return "", err
			}
			if err := validateNonEmptyObject(p.one("json"), "field"); err != nil {
				return "", err
			}
			// lark-cli requires --yes. Task 7 owns the confirmation gate and may
			// append it only after the stored high-risk operation is confirmed.
			return RiskHigh, nil
		},
	})

	recordWriteFlags := map[string]flagRule{"base-token": baseToken, "table-id": idOrName, "json": jsonPayload}
	c.register(commandSpec{
		path: "base +record-batch-create", domain: "base", action: "record-batch-create", risk: RiskWrite, scopes: baseScopes["base +record-batch-create"],
		flags:    recordWriteFlags,
		limits:   map[string]int{"confirmation_batch": CommandCatalogMaxRecordWriteBatch, "records": CommandCatalogCLIRecordWriteBatchMax, "json_bytes": CommandCatalogMaxJSONBytes, "stdout_bytes": CommandCatalogMaxStdoutBytes},
		validate: validateRecordBatchCreate,
	})
	upsertFlags := cloneFlagRules(recordWriteFlags)
	upsertFlags["record-id"] = recordID
	c.register(commandSpec{
		path: "base +record-upsert", domain: "base", action: "record-upsert", risk: RiskWrite, scopes: baseScopes["base +record-upsert"],
		flags:  upsertFlags,
		limits: map[string]int{"fields": CommandCatalogMaxBaseFields, "json_bytes": CommandCatalogMaxJSONBytes, "stdout_bytes": CommandCatalogMaxStdoutBytes},
		validate: func(p *parsedFlags) (RiskLevel, error) {
			if err := requireFlags(p, "base-token", "table-id", "json"); err != nil {
				return "", err
			}
			if err := validateNonEmptyObjectMax(p.one("json"), "record", CommandCatalogMaxBaseFields); err != nil {
				return "", err
			}
			return RiskWrite, nil
		},
	})
	c.register(commandSpec{
		path: "base +record-batch-update", domain: "base", action: "record-batch-update", risk: RiskWrite, scopes: baseScopes["base +record-batch-update"],
		flags:    recordWriteFlags,
		limits:   map[string]int{"confirmation_batch": CommandCatalogMaxRecordWriteBatch, "records": CommandCatalogCLIRecordWriteBatchMax, "json_bytes": CommandCatalogMaxJSONBytes, "stdout_bytes": CommandCatalogMaxStdoutBytes},
		validate: validateRecordBatchUpdate,
	})
}

func simpleBaseSpec(path, action string, risk RiskLevel, scopes []string, flags map[string]flagRule, required []string) commandSpec {
	return commandSpec{
		path: path, domain: "base", action: action, risk: risk, scopes: scopes, flags: flags,
		limits: map[string]int{"stdout_bytes": CommandCatalogMaxStdoutBytes},
		validate: func(p *parsedFlags) (RiskLevel, error) {
			if err := requireFlags(p, required...); err != nil {
				return "", err
			}
			return risk, nil
		},
	}
}

func simpleBaseListSpec(path, action string, scopes []string, flags map[string]flagRule, required []string) commandSpec {
	spec := simpleBaseSpec(path, action, RiskRead, scopes, flags, required)
	spec.limits["page_size"] = CommandCatalogMaxReadPageSize
	return spec
}

func validateRecordGet(p *parsedFlags) (RiskLevel, error) {
	if err := requireFlags(p, "base-token", "table-id"); err != nil {
		return "", err
	}
	if len(p.values["field-id"]) > CommandCatalogMaxProjectedFields {
		return "", invalidf("too many projected fields")
	}
	direct := len(p.values["record-id"])
	if direct > 0 && p.has("json") {
		return "", invalidf("record-id and json forms are mutually exclusive")
	}
	if direct > CommandCatalogMaxRecordReadBatch {
		return "", invalidf("too many record ids")
	}
	if direct == 0 && !p.has("json") {
		return "", invalidf("record-get requires record-id or json")
	}
	if p.has("json") {
		var payload struct {
			RecordIDs []string `json:"record_id_list"`
		}
		if err := decodeStrictObject(p.one("json"), &payload); err != nil {
			return "", invalidf("invalid record-get json")
		}
		if len(payload.RecordIDs) == 0 || len(payload.RecordIDs) > CommandCatalogMaxRecordReadBatch {
			return "", invalidf("record-get json record count is outside limits")
		}
		for _, id := range payload.RecordIDs {
			if _, err := normalizePrefixedID("record-id", "rec")(id); err != nil {
				return "", err
			}
		}
	}
	return RiskRead, nil
}

func validateRecordList(p *parsedFlags) (RiskLevel, error) {
	if err := requireFlags(p, "base-token", "table-id"); err != nil {
		return "", err
	}
	if len(p.values["field-id"]) > CommandCatalogMaxProjectedFields {
		return "", invalidf("too many projected fields")
	}
	if p.has("sort-json") {
		if err := validateSortJSON(p.one("sort-json")); err != nil {
			return "", err
		}
	}
	return RiskRead, nil
}

func validateRecordSearch(p *parsedFlags) (RiskLevel, error) {
	if err := requireFlags(p, "base-token", "table-id"); err != nil {
		return "", err
	}
	if p.has("json") {
		for _, name := range []string{"keyword", "search-field", "field-id", "view-id", "filter-json", "sort-json", "offset", "limit"} {
			if p.has(name) {
				return "", invalidf("record-search json form cannot be mixed with individual flags")
			}
		}
		if err := validateSearchJSON(p.one("json")); err != nil {
			return "", err
		}
		return RiskRead, nil
	}
	if err := requireFlags(p, "keyword", "search-field"); err != nil {
		return "", err
	}
	if len(p.values["search-field"]) > CommandCatalogMaxSearchFields || len(p.values["field-id"]) > CommandCatalogMaxProjectedFields {
		return "", invalidf("record-search field count exceeds limits")
	}
	if p.has("sort-json") {
		if err := validateSortJSON(p.one("sort-json")); err != nil {
			return "", err
		}
	}
	return RiskRead, nil
}

func validateRecordBatchCreate(p *parsedFlags) (RiskLevel, error) {
	if err := requireFlags(p, "base-token", "table-id", "json"); err != nil {
		return "", err
	}
	var payload struct {
		Fields []string `json:"fields"`
		Rows   [][]any  `json:"rows"`
	}
	if err := decodeStrictObject(p.one("json"), &payload); err != nil {
		return "", invalidf("invalid record batch create json")
	}
	if len(payload.Fields) == 0 || len(payload.Fields) > CommandCatalogMaxProjectedFields {
		return "", invalidf("record batch create field count is outside limits")
	}
	if len(payload.Rows) == 0 || len(payload.Rows) > CommandCatalogCLIRecordWriteBatchMax {
		return "", invalidf("record batch create row count is outside limits")
	}
	for _, field := range payload.Fields {
		if _, err := normalizeIDOrName(field); err != nil {
			return "", err
		}
	}
	for _, row := range payload.Rows {
		if len(row) != len(payload.Fields) {
			return "", invalidf("record batch create row width does not match fields")
		}
	}
	if len(payload.Rows) > CommandCatalogMaxRecordWriteBatch {
		return RiskHigh, nil
	}
	return RiskWrite, nil
}

func validateRecordBatchUpdate(p *parsedFlags) (RiskLevel, error) {
	if err := requireFlags(p, "base-token", "table-id", "json"); err != nil {
		return "", err
	}
	var payload struct {
		RecordIDs []string       `json:"record_id_list"`
		Patch     map[string]any `json:"patch"`
	}
	if err := decodeStrictObject(p.one("json"), &payload); err != nil {
		return "", invalidf("invalid record batch update json")
	}
	if len(payload.RecordIDs) == 0 || len(payload.RecordIDs) > CommandCatalogCLIRecordWriteBatchMax {
		return "", invalidf("record batch update count is outside limits")
	}
	if len(payload.Patch) == 0 || len(payload.Patch) > CommandCatalogMaxBaseFields {
		return "", invalidf("record batch update patch is empty or too wide")
	}
	for _, id := range payload.RecordIDs {
		if _, err := normalizePrefixedID("record-id", "rec")(id); err != nil {
			return "", err
		}
	}
	if len(payload.RecordIDs) > CommandCatalogMaxRecordWriteBatch {
		return RiskHigh, nil
	}
	return RiskWrite, nil
}

func (c *CommandCatalog) addWikiSpecs() {
	spaceID := valueRule(normalizeSpaceID)
	nodeToken := valueRule(normalizeSupportedRef("node-token", map[string]bool{"wiki": true, "docx": true}, false))
	opaque := valueRule(normalizeOpaqueToken("node-token"))

	c.register(commandSpec{
		path: "wiki +space-create", domain: "wiki", action: "space-create", risk: RiskWrite, scopes: wikiScopes["wiki +space-create"],
		flags: map[string]flagRule{
			"name":        valueRule(normalizeInlineText("name", commandCatalogMaxNameBytes, false)),
			"description": valueRule(normalizeInlineText("description", commandCatalogMaxDescription, true)),
		},
		limits: map[string]int{"description_bytes": commandCatalogMaxDescription, "stdout_bytes": CommandCatalogMaxStdoutBytes},
		validate: func(p *parsedFlags) (RiskLevel, error) {
			if err := requireFlags(p, "name"); err != nil {
				return "", err
			}
			return RiskWrite, nil
		},
	})
	c.register(commandSpec{
		path: "wiki +node-create", domain: "wiki", action: "node-create", risk: RiskWrite, scopes: wikiScopes["wiki +node-create"],
		flags: map[string]flagRule{
			"space-id":          spaceID,
			"parent-node-token": opaque,
			"title":             valueRule(normalizeInlineText("title", commandCatalogMaxTitleBytes, false)),
			"node-type":         valueRule(normalizeEnum("node-type", "origin")),
			"obj-type":          valueRule(normalizeEnum("obj-type", "docx")),
		},
		limits: map[string]int{"stdout_bytes": CommandCatalogMaxStdoutBytes, "title_bytes": commandCatalogMaxTitleBytes},
		validate: func(p *parsedFlags) (RiskLevel, error) {
			if err := requireFlags(p, "title"); err != nil {
				return "", err
			}
			return RiskWrite, nil
		},
	})
	c.register(commandSpec{
		path: "wiki +node-get", domain: "wiki", action: "node-get", risk: RiskRead, scopes: wikiScopes["wiki +node-get"],
		flags: map[string]flagRule{
			"node-token": nodeToken,
			"obj-type":   valueRule(normalizeEnum("obj-type", "docx")),
			"space-id":   spaceID,
		},
		limits: map[string]int{"stdout_bytes": CommandCatalogMaxStdoutBytes},
		validate: func(p *parsedFlags) (RiskLevel, error) {
			if err := requireFlags(p, "node-token"); err != nil {
				return "", err
			}
			return RiskRead, nil
		},
	})
	c.register(commandSpec{
		path: "wiki +node-list", domain: "wiki", action: "node-list", risk: RiskRead, scopes: wikiScopes["wiki +node-list"],
		flags: map[string]flagRule{
			"space-id":          spaceID,
			"parent-node-token": opaque,
			"page-all":          boolRule(),
			"page-limit":        valueRule(normalizeInt("page-limit", 1, CommandCatalogMaxWikiPages)),
			"page-size":         valueRule(normalizeInt("page-size", 1, CommandCatalogMaxWikiPageSize)),
			"page-token":        valueRule(normalizeOpaqueToken("page-token")),
		},
		limits: map[string]int{"page_size": CommandCatalogMaxWikiPageSize, "pages": CommandCatalogMaxWikiPages, "stdout_bytes": CommandCatalogMaxStdoutBytes},
		validate: func(p *parsedFlags) (RiskLevel, error) {
			if err := requireFlags(p, "space-id"); err != nil {
				return "", err
			}
			if p.has("page-token") && p.has("page-all") {
				return "", invalidf("page-token and page-all are mutually exclusive")
			}
			if p.has("page-limit") && !p.has("page-all") {
				return "", invalidf("page-limit requires page-all")
			}
			return RiskRead, nil
		},
	})
}

type catalogManifestEntry struct {
	Path         string         `json:"path"`
	Domain       string         `json:"domain"`
	Scopes       []string       `json:"scopes"`
	Risk         RiskLevel      `json:"risk"`
	Limits       map[string]int `json:"limits"`
	AllowedFlags []string       `json:"allowed_flags"`
}

func (c *CommandCatalog) manifest() []catalogManifestEntry {
	entries := make([]catalogManifestEntry, 0, len(c.specs))
	for _, spec := range c.specs {
		flags := make([]string, 0, len(spec.flags))
		for name := range spec.flags {
			flags = append(flags, "--"+name)
		}
		sort.Strings(flags)
		limits := make(map[string]int, len(spec.limits))
		for name, limit := range spec.limits {
			limits[name] = limit
		}
		entries = append(entries, catalogManifestEntry{
			Path: spec.path, Domain: spec.domain, Scopes: append([]string(nil), spec.scopes...),
			Risk: spec.risk, Limits: limits, AllowedFlags: flags,
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries
}

func valueRule(normalize func(string) (string, error)) flagRule {
	return flagRule{normalize: normalize}
}

func boolRule() flagRule { return flagRule{boolean: true} }

func repeatable(rule flagRule) flagRule {
	rule.repeatable = true
	return rule
}

func cloneFlagRules(source map[string]flagRule) map[string]flagRule {
	clone := make(map[string]flagRule, len(source))
	for name, rule := range source {
		clone[name] = rule
	}
	return clone
}

func requireFlags(p *parsedFlags, names ...string) error {
	for _, name := range names {
		if !p.has(name) {
			return invalidf("required flag --%s is missing", name)
		}
	}
	return nil
}

func requireNonEmptyFlags(p *parsedFlags, names ...string) error {
	if err := requireFlags(p, names...); err != nil {
		return err
	}
	for _, name := range names {
		if p.one(name) == "" {
			return invalidf("flag --%s cannot be empty", name)
		}
	}
	return nil
}

func normalizeInlineText(label string, maxBytes int, allowEmpty bool) func(string) (string, error) {
	return func(value string) (string, error) {
		if (!allowEmpty && value == "") || len(value) > maxBytes || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
			return "", invalidf("%s is empty, invalid, or too large", label)
		}
		for _, r := range value {
			if (r < 0x20 && r != '\n' && r != '\r' && r != '\t') || r == 0x7f {
				return "", invalidf("%s contains unsupported control characters", label)
			}
		}
		if value == "-" || strings.HasPrefix(value, "@") {
			return "", invalidf("%s cannot use file or stdin indirection", label)
		}
		return value, nil
	}
}

func normalizeEnum(label string, allowed ...string) func(string) (string, error) {
	set := make(map[string]struct{}, len(allowed))
	for _, value := range allowed {
		set[value] = struct{}{}
	}
	return func(value string) (string, error) {
		if _, ok := set[value]; !ok {
			return "", invalidf("%s has an unsupported value", label)
		}
		return value, nil
	}
}

func normalizeInt(label string, minValue, maxValue int) func(string) (string, error) {
	return func(value string) (string, error) {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < minValue || parsed > maxValue {
			return "", invalidf("%s is outside limits", label)
		}
		return strconv.Itoa(parsed), nil
	}
}

var (
	opaqueTokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)
	prefixedIDPattern  = regexp.MustCompile(`^[A-Za-z0-9_-]{5,125}$`)
	languagePattern    = regexp.MustCompile(`^[a-z]{2,3}-[A-Z]{2}$`)
	timeZonePattern    = regexp.MustCompile(`^[A-Za-z_]+(?:/[A-Za-z0-9_+.-]+)+$`)
)

func normalizeOpaqueToken(label string) func(string) (string, error) {
	return func(value string) (string, error) {
		if !opaqueTokenPattern.MatchString(value) {
			return "", invalidf("%s is not an allowed opaque token", label)
		}
		return value, nil
	}
}

func normalizePrefixedID(label, prefix string) func(string) (string, error) {
	return func(value string) (string, error) {
		if !strings.HasPrefix(value, prefix) || !prefixedIDPattern.MatchString(strings.TrimPrefix(value, prefix)) {
			return "", invalidf("%s is not a valid %s id", label, prefix)
		}
		return value, nil
	}
}

func normalizeIDOrName(value string) (string, error) {
	if value == "" || len(value) > commandCatalogMaxNameBytes || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return "", invalidf("id or name is invalid")
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return "", invalidf("id or name contains control characters")
		}
	}
	return value, nil
}

func normalizeBlockID(label string, allowEnd bool) func(string) (string, error) {
	return func(value string) (string, error) {
		if allowEnd && value == "-1" {
			return value, nil
		}
		return normalizeOpaqueToken(label)(value)
	}
}

func normalizeLanguage(value string) (string, error) {
	if !languagePattern.MatchString(value) {
		return "", invalidf("lang must be a language-region tag")
	}
	return value, nil
}

func normalizeTimeZone(value string) (string, error) {
	if len(value) > 64 || !timeZonePattern.MatchString(value) {
		return "", invalidf("time-zone is not an IANA zone")
	}
	return value, nil
}

func normalizeSpaceID(value string) (string, error) {
	if value == "my_library" {
		return value, nil
	}
	return normalizeOpaqueToken("space-id")(value)
}

func normalizeSupportedRef(label string, allowedKinds map[string]bool, extractToken bool) func(string) (string, error) {
	return func(value string) (string, error) {
		if !strings.Contains(value, "://") {
			return normalizeOpaqueToken(label)(value)
		}
		parsed, err := url.Parse(value)
		if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" || parsed.Fragment != "" || parsed.RawPath != "" {
			return "", invalidf("%s URL is unsafe", label)
		}
		host := strings.ToLower(parsed.Hostname())
		if !supportedFeishuHost(host) {
			return "", invalidf("%s URL host is unsupported", label)
		}
		escapedPath := parsed.EscapedPath()
		if !strings.HasPrefix(escapedPath, "/") || strings.HasPrefix(escapedPath, "//") || strings.HasSuffix(escapedPath, "/") {
			return "", invalidf("%s URL path is unsafe", label)
		}
		parts := strings.Split(strings.TrimPrefix(escapedPath, "/"), "/")
		if len(parts) != 2 || !allowedKinds[parts[0]] {
			return "", invalidf("%s URL path is unsupported", label)
		}
		token, err := url.PathUnescape(parts[1])
		if err != nil || !opaqueTokenPattern.MatchString(token) {
			return "", invalidf("%s URL token is invalid", label)
		}
		if extractToken {
			return token, nil
		}
		return value, nil
	}
}

func supportedFeishuHost(host string) bool {
	for _, suffix := range []string{"feishu.cn", "larksuite.com", "doubao.com"} {
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return true
		}
	}
	return false
}

func normalizeJSON(label string, maxBytes int) func(string) (string, error) {
	return func(value string) (string, error) {
		if value == "-" || strings.HasPrefix(value, "@") || len(value) == 0 || len(value) > maxBytes || !json.Valid([]byte(value)) {
			return "", invalidf("%s is invalid, too large, or uses indirection", label)
		}
		var decoded any
		decoder := json.NewDecoder(strings.NewReader(value))
		decoder.UseNumber()
		if err := decoder.Decode(&decoded); err != nil || validateJSONTree(decoded, 0) != nil {
			return "", invalidf("%s exceeds structural limits", label)
		}
		return value, nil
	}
}

func normalizeJSONObject(label string, maxBytes int) func(string) (string, error) {
	base := normalizeJSON(label, maxBytes)
	return func(value string) (string, error) {
		normalized, err := base(value)
		if err != nil {
			return "", err
		}
		var object map[string]any
		if err := json.Unmarshal([]byte(value), &object); err != nil || object == nil {
			return "", invalidf("%s must be a JSON object", label)
		}
		return normalized, nil
	}
}

func validateJSONTree(value any, depth int) error {
	if depth > commandCatalogMaxJSONDepth {
		return ErrCommandInvalidArgument
	}
	switch typed := value.(type) {
	case []any:
		if len(typed) > CommandCatalogCLIRecordWriteBatchMax {
			return ErrCommandInvalidArgument
		}
		for _, item := range typed {
			if err := validateJSONTree(item, depth+1); err != nil {
				return err
			}
		}
	case map[string]any:
		if len(typed) > CommandCatalogCLIRecordWriteBatchMax {
			return ErrCommandInvalidArgument
		}
		for key, item := range typed {
			if key == "" || len(key) > commandCatalogMaxNameBytes {
				return ErrCommandInvalidArgument
			}
			if err := validateJSONTree(item, depth+1); err != nil {
				return err
			}
		}
	case string:
		if len(typed) > CommandCatalogMaxContentBytes || !utf8.ValidString(typed) {
			return ErrCommandInvalidArgument
		}
	}
	return nil
}

func validateFieldArray(raw string) error {
	var fields []map[string]any
	if err := json.Unmarshal([]byte(raw), &fields); err != nil || len(fields) == 0 || len(fields) > CommandCatalogMaxBaseFields {
		return invalidf("fields must be a bounded non-empty JSON array")
	}
	for _, field := range fields {
		name, nameOK := field["name"].(string)
		typeName, typeOK := field["type"].(string)
		if !nameOK || !typeOK || name == "" || typeName == "" {
			return invalidf("each field requires name and type")
		}
	}
	return nil
}

func validateJSONObjectOrArray(raw, label string, maxItems int) error {
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return invalidf("%s is invalid JSON", label)
	}
	switch typed := value.(type) {
	case map[string]any:
		if len(typed) == 0 || len(typed) > maxItems {
			return invalidf("%s object size is outside limits", label)
		}
	case []any:
		if len(typed) == 0 || len(typed) > maxItems {
			return invalidf("%s array size is outside limits", label)
		}
	default:
		return invalidf("%s must be an object or array", label)
	}
	return nil
}

func validateNonEmptyObject(raw, label string) error {
	return validateNonEmptyObjectMax(raw, label, CommandCatalogCLIRecordWriteBatchMax)
}

func validateNonEmptyObjectMax(raw, label string, maxItems int) error {
	var object map[string]any
	if err := json.Unmarshal([]byte(raw), &object); err != nil || len(object) == 0 || len(object) > maxItems {
		return invalidf("%s must be a bounded non-empty JSON object", label)
	}
	return nil
}

func validateSortJSON(raw string) error {
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return invalidf("sort-json is invalid")
	}
	if object, ok := value.(map[string]any); ok {
		value = object["sort_config"]
	}
	items, ok := value.([]any)
	if !ok || len(items) > CommandCatalogMaxSortFields {
		return invalidf("sort-json exceeds sort field limits")
	}
	return nil
}

func validateSearchJSON(raw string) error {
	var payload struct {
		Keyword      string           `json:"keyword"`
		SearchFields []string         `json:"search_fields"`
		SelectFields []string         `json:"select_fields"`
		ViewID       string           `json:"view_id"`
		Filter       json.RawMessage  `json:"filter"`
		Sort         []map[string]any `json:"sort"`
		Offset       *int             `json:"offset"`
		Limit        *int             `json:"limit"`
	}
	if err := decodeStrictObject(raw, &payload); err != nil {
		return invalidf("record-search json has unsupported fields")
	}
	if payload.Keyword == "" || len(payload.Keyword) > 4<<10 || len(payload.SearchFields) == 0 || len(payload.SearchFields) > CommandCatalogMaxSearchFields || len(payload.SelectFields) > CommandCatalogMaxProjectedFields || len(payload.Sort) > CommandCatalogMaxSortFields {
		return invalidf("record-search json exceeds limits")
	}
	if payload.Offset != nil && (*payload.Offset < 0 || *payload.Offset > commandCatalogMaxOffset) {
		return invalidf("record-search offset is outside limits")
	}
	if payload.Limit != nil && (*payload.Limit < 1 || *payload.Limit > CommandCatalogMaxReadPageSize) {
		return invalidf("record-search limit is outside limits")
	}
	for _, field := range append(append([]string(nil), payload.SearchFields...), payload.SelectFields...) {
		if _, err := normalizeIDOrName(field); err != nil {
			return err
		}
	}
	if len(payload.Filter) > 0 && !bytes.Equal(payload.Filter, []byte("null")) {
		var filter map[string]any
		if err := json.Unmarshal(payload.Filter, &filter); err != nil {
			return invalidf("record-search filter must be an object")
		}
	}
	return nil
}

func decodeStrictObject(raw string, target any) error {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.More() {
		return errors.New("multiple JSON values")
	}
	return nil
}

func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrCommandInvalidArgument, fmt.Sprintf(format, args...))
}

func deniedf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrCommandDenied, fmt.Sprintf(format, args...))
}
