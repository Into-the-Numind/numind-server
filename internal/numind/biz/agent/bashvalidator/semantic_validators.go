package bashvalidator

import (
	"path"
	"regexp"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// This file adds SEMANTIC dangerous-command checkers on top of the 8 Phase-0 obfuscation
// checkers (agent-security-hardening BLK-3 / platform bans ②③④ + ①-literal). Each
// validator implements the Validator interface and is registered in AllValidators().
//
// Design rule (不误伤): every pattern locks ONLY a dangerous form. The accompanying
// table-driven tests assert BOTH "dangerous → Deny" AND "normal → Allow".
//
// Scope note: bash runs inside the sandbox (ephemeral, --network=none, container's own
// /etc & home), so these are defense-in-depth — most valuable if the sandbox network
// policy changes or docker.sock is exposed (dev). They reuse no tenant DB; all platform-level.

// ── shared helpers ───────────────────────────────────────────────────────────

// segSplitRe splits a command line into pipeline/list segments on ; & | and newlines.
var segSplitRe = regexp.MustCompile(`[;\n&|]+`)

// splitSegments returns the non-empty, trimmed command segments.
func splitSegments(cmd string) []string {
	var out []string
	for _, p := range segSplitRe.Split(cmd, -1) {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	return out
}

var envAssignRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// firstCommand returns the basename of the first real command token in a segment plus its
// args, skipping a leading sudo/command/exec/env wrapper and VAR=value assignments.
func firstCommand(seg string) (string, []string) {
	fields := strings.Fields(seg)
	i := 0
	for i < len(fields) {
		f := fields[i]
		if f == "sudo" || f == "command" || f == "exec" || f == "env" || f == "nohup" || f == "time" {
			i++
			continue
		}
		if envAssignRe.MatchString(f) {
			i++
			continue
		}
		break
	}
	if i >= len(fields) {
		return "", nil
	}
	return path.Base(unquote(fields[i])), fields[i+1:]
}

// unquote strips a single pair of surrounding quotes from a token.
func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '\'' && s[len(s)-1] == '\'') || (s[0] == '"' && s[len(s)-1] == '"') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// ===========================================================================
// ② DestructiveRemove — rm with recursive+force against a root/home target.
// ===========================================================================

type destructiveRemoveValidator struct{}

// NewDestructiveRemoveValidator returns a Validator that denies catastrophic rm forms.
func NewDestructiveRemoveValidator() Validator { return &destructiveRemoveValidator{} }

func (v *destructiveRemoveValidator) ID() string { return "DestructiveRemove" }

// rmDangerousBare are root/home targets (after stripping a trailing /, /*, /.) whose
// recursive-force deletion is catastrophic. Variable/tilde forms can't be path.Clean'd,
// so they are matched literally.
var rmDangerousBare = map[string]bool{
	"/": true, "~": true, "$HOME": true, "${HOME}": true,
}

// rmCriticalRoots are absolute OS roots whose recursive-force deletion wrecks the system.
// Deliberately EXCLUDES /tmp, /workdir, /mnt, /data and other working dirs (不误伤).
var rmCriticalRoots = map[string]bool{
	"/home": true, "/usr": true, "/etc": true, "/var": true, "/boot": true,
	"/lib": true, "/lib64": true, "/bin": true, "/sbin": true, "/opt": true,
	"/sys": true, "/proc": true, "/dev": true, "/srv": true, "/root": true,
}

// isDangerousRmTarget reports whether a single rm target argument is a root / home /
// critical OS path. It strips a trailing glob/dot/slash, then checks the bare-literal and
// critical-root sets (path.Clean normalises // and trailing slashes).
func isDangerousRmTarget(raw string) bool {
	t := unquote(raw)
	t = strings.TrimSuffix(t, "/*")
	t = strings.TrimSuffix(t, "/.")
	for strings.HasSuffix(t, "/") && len(t) > 1 {
		t = strings.TrimSuffix(t, "/")
	}
	if t == "" {
		t = "/"
	}
	if rmDangerousBare[t] {
		return true
	}
	return rmCriticalRoots[path.Clean(t)]
}

func (v *destructiveRemoveValidator) Validate(cmd string) Result {
	for _, seg := range splitSegments(cmd) {
		name, args := firstCommand(seg)
		if name != "rm" {
			continue
		}
		recursive, force := false, false
		var targets []string
		for _, tok := range args {
			switch {
			case strings.HasPrefix(tok, "--"):
				if tok == "--recursive" {
					recursive = true
				}
				if tok == "--force" {
					force = true
				}
			case strings.HasPrefix(tok, "-") && len(tok) > 1:
				for _, c := range tok[1:] {
					if c == 'r' || c == 'R' {
						recursive = true
					}
					if c == 'f' {
						force = true
					}
				}
			default:
				targets = append(targets, unquote(tok))
			}
		}
		if !recursive || !force {
			continue
		}
		for _, t := range targets {
			if isDangerousRmTarget(t) {
				return denyResult(v.ID(),
					"recursive-force rm targets a root/critical/home path which would wipe critical data",
					"rm -rf "+t)
			}
		}
	}
	return allowResult()
}

// ===========================================================================
// ② DiskDestruct — mkfs, dd of=/dev/*, redirect to a raw block device.
// ===========================================================================

type diskDestructValidator struct {
	devRedirectRe *regexp.Regexp
}

// NewDiskDestructValidator returns a Validator for disk/device-destroying commands.
func NewDiskDestructValidator() Validator {
	return &diskDestructValidator{
		devRedirectRe: regexp.MustCompile(`>\s*/dev/(sd[a-z]|nvme\d|vd[a-z]|hd[a-z]|mmcblk\d|disk\d)`),
	}
}

func (v *diskDestructValidator) ID() string { return "DiskDestruct" }

func (v *diskDestructValidator) Validate(cmd string) Result {
	if loc := v.devRedirectRe.FindStringIndex(cmd); loc != nil {
		return denyResult(v.ID(),
			"redirect to a raw block device would destroy the disk",
			cmd[loc[0]:loc[1]])
	}
	for _, seg := range splitSegments(cmd) {
		name, args := firstCommand(seg)
		if strings.HasPrefix(name, "mkfs") {
			return denyResult(v.ID(), "mkfs would format a filesystem (destructive)", name)
		}
		if name == "dd" {
			for _, tok := range args {
				if strings.HasPrefix(unquote(tok), "of=/dev/") {
					return denyResult(v.ID(),
						"dd writing to a device file would overwrite the disk", unquote(tok))
				}
			}
		}
	}
	return allowResult()
}

// ===========================================================================
// ② ForkBomb — a self-referencing function piped to itself in the background.
// ===========================================================================

type forkBombValidator struct {
	funcDefRe *regexp.Regexp
}

// NewForkBombValidator returns a Validator that detects fork-bomb shapes.
func NewForkBombValidator() Validator {
	return &forkBombValidator{
		// function definition: <name>() {  (name may be ":" as in the classic bomb)
		funcDefRe: regexp.MustCompile(`([\w:]+)\s*\(\s*\)\s*\{`),
	}
}

func (v *forkBombValidator) ID() string { return "ForkBomb" }

func (v *forkBombValidator) Validate(cmd string) Result {
	if !strings.Contains(cmd, "&") {
		return allowResult() // a fork bomb needs backgrounding
	}
	for _, m := range v.funcDefRe.FindAllStringSubmatch(cmd, -1) {
		name := m[1]
		// self-pipe: the function name piped to itself, e.g. ":|:" or "f | f".
		selfPipe := regexp.MustCompile(regexp.QuoteMeta(name) + `\s*\|\s*` + regexp.QuoteMeta(name))
		if selfPipe.MatchString(cmd) {
			return denyResult(v.ID(),
				"self-referencing function piped to itself in the background is a fork bomb",
				name+"(){ "+name+"|"+name+"& }")
		}
	}
	return allowResult()
}

// ===========================================================================
// ③ DownloadExec — download piped into a shell, or download-then-execute.
// ===========================================================================

type downloadExecValidator struct {
	pipeToShellRe *regexp.Regexp
	base64ShellRe *regexp.Regexp
	downloadFlagO *regexp.Regexp
}

// NewDownloadExecValidator returns a Validator for download-and-execute patterns.
func NewDownloadExecValidator() Validator {
	return &downloadExecValidator{
		// curl/wget ... | (sudo) sh/bash/zsh/python -
		pipeToShellRe: regexp.MustCompile(`(?i)\b(?:curl|wget)\b[^|]*\|\s*(?:sudo\s+)?(?:bash|sh|zsh|dash|ksh|python[0-9.]*\s+-)`),
		// ... | base64 -d ... | sh
		base64ShellRe: regexp.MustCompile(`(?i)\bbase64\b[^|]*(?:-d|--decode|-D)\b[^|]*\|\s*(?:sudo\s+)?(?:bash|sh|zsh)`),
		// capture the -o/-O download target file
		downloadFlagO: regexp.MustCompile(`(?i)\b(?:curl|wget)\b[^;&|]*?-[oO]\s+(\S+)`),
	}
}

func (v *downloadExecValidator) ID() string { return "DownloadExec" }

func (v *downloadExecValidator) Validate(cmd string) Result {
	if loc := v.pipeToShellRe.FindStringIndex(cmd); loc != nil {
		return denyResult(v.ID(),
			"downloading a remote payload piped directly into a shell executes untrusted code",
			cmd[loc[0]:loc[1]])
	}
	if loc := v.base64ShellRe.FindStringIndex(cmd); loc != nil {
		return denyResult(v.ID(),
			"base64-decoding a payload piped into a shell executes untrusted code",
			cmd[loc[0]:loc[1]])
	}
	// Two-step: curl -o FILE && sh FILE  (only when the DOWNLOADED file is the one executed).
	if m := v.downloadFlagO.FindStringSubmatch(cmd); m != nil {
		file := unquote(m[1])
		if file != "" {
			execRe := regexp.MustCompile(`(?i)\b(?:bash|sh|zsh|dash|python[0-9.]*)\s+` + regexp.QuoteMeta(file) + `\b`)
			if execRe.MatchString(cmd) {
				return denyResult(v.ID(),
					"downloading a file then executing it with a shell runs untrusted code",
					"download "+file+" then exec")
			}
		}
	}
	return allowResult()
}

// ===========================================================================
// ④ CredentialFile — reading server credential/secret files.
// ===========================================================================

type credentialFileValidator struct {
	pathRe    *regexp.Regexp
	envVerbRe *regexp.Regexp
}

// NewCredentialFileValidator returns a Validator that denies reads of sensitive paths.
func NewCredentialFileValidator() Validator {
	return &credentialFileValidator{
		pathRe: regexp.MustCompile(
			`/etc/(?:shadow|gshadow|sudoers)\b` +
				// Only the SECRET files under .ssh — NOT the whole dir, so `ls ~/.ssh/` and
				// `grep Host ~/.ssh/config` (legit) are allowed; private keys are blocked.
				`|(?:/root|/home/[^/\s]+|~|\$HOME|\$\{HOME\})/\.ssh/(?:id_[a-z0-9_]+|identity|authorized_keys|[^/\s]*\.pem|[^/\s]*\.key)\b` +
				`|\.aws/credentials\b` +
				`|/proc/[^/\s]+/environ\b`),
		// file-access command verbs (for the verb-gated .env rule)
		envVerbRe: regexp.MustCompile(`(?i)^(?:cat|less|more|head|tail|source|vi|vim|nano|cp|mv|scp|base64|xxd|od|strings|grep|awk|sed|rsync|open|read)$`),
	}
}

func (v *credentialFileValidator) ID() string { return "CredentialFile" }

func (v *credentialFileValidator) Validate(cmd string) Result {
	if loc := v.pathRe.FindStringIndex(cmd); loc != nil {
		return denyResult(v.ID(),
			"accessing a server credential/secret file is not allowed",
			cmd[loc[0]:loc[1]])
	}
	// .env is verb-gated to avoid blocking informational echo/printf mentioning ".env".
	for _, seg := range splitSegments(cmd) {
		name, args := firstCommand(seg)
		if !v.envVerbRe.MatchString(name) {
			continue
		}
		for _, tok := range args {
			if isBareEnvFile(unquote(tok)) {
				return denyResult(v.ID(),
					"reading a .env credential file is not allowed", tok)
			}
		}
	}
	return allowResult()
}

// isBareEnvFile reports whether tok is exactly a ".env" file (or a path ending in /.env),
// NOT a variant like .env.example / .env.local / .envrc.
func isBareEnvFile(tok string) bool {
	return tok == ".env" || strings.HasSuffix(tok, "/.env")
}

// ===========================================================================
// ① SSRFLiteral — curl/wget hitting an internal / cloud-metadata IP literal.
// ===========================================================================

type ssrfLiteralValidator struct {
	curlWgetRe *regexp.Regexp
	internalRe *regexp.Regexp
}

// NewSSRFLiteralValidator returns a Validator that denies curl/wget to internal IP literals.
func NewSSRFLiteralValidator() Validator {
	return &ssrfLiteralValidator{
		curlWgetRe: regexp.MustCompile(`(?i)\b(?:curl|wget)\b`),
		// Anchored on the URL host position (after ://, past optional userinfo@) so that an
		// internal token appearing in a PATH or query (e.g. https://example.com/localhost)
		// is NOT a false positive. Only the host is checked.
		internalRe: regexp.MustCompile(
			`(?i)://(?:[^/@\s]*@)?(?:` +
				`169\.254\.169\.254` + // cloud metadata
				`|127(?:\.\d{1,3}){3}` + // loopback
				`|0\.0\.0\.0` + // unspecified
				`|10(?:\.\d{1,3}){3}` + // RFC1918 10/8
				`|192\.168(?:\.\d{1,3}){2}` + // RFC1918 192.168/16
				`|172\.(?:1[6-9]|2\d|3[01])(?:\.\d{1,3}){2}` + // RFC1918 172.16-31
				`|localhost` +
				`|\[::1\]` + // IPv6 loopback
				`|\[fe80:[^\]]*\]` + // IPv6 link-local
				`)(?:[:/?#\s]|$)`), // host must end at port/path/query/end
	}
}

func (v *ssrfLiteralValidator) ID() string { return "SSRFLiteral" }

func (v *ssrfLiteralValidator) Validate(cmd string) Result {
	if !v.curlWgetRe.MatchString(cmd) {
		return allowResult()
	}
	if loc := v.internalRe.FindStringIndex(cmd); loc != nil {
		return denyResult(v.ID(),
			"curl/wget to an internal or cloud-metadata address is blocked (SSRF)",
			cmd[loc[0]:loc[1]])
	}
	return allowResult()
}

// ===========================================================================
// LarkCLIRoute — hosted Feishu operations must cross the per-user boundary.
// ===========================================================================

const (
	maxLarkShellCommandBytes = 64 << 10
	maxLarkShellPayloadDepth = 8
	maxLarkFallbackHeadBytes = 16
)

type larkCLIRouteValidator struct{}

// NewLarkCLIRouteValidator returns the hosted Feishu CLI route validator.
func NewLarkCLIRouteValidator() Validator { return &larkCLIRouteValidator{} }

func (v *larkCLIRouteValidator) ID() string { return "LarkCLIRoute" }

func (v *larkCLIRouteValidator) Validate(cmd string) Result {
	if larkShellContainsExecution(cmd, 0) {
		return denyResult(v.ID(),
			"飞书操作必须使用 lark_execute，以便绑定当前用户的加密工作空间和授权恢复流程",
			"lark-cli")
	}
	return allowResult()
}

func larkShellContainsExecution(source string, depth int) bool {
	// Parsing work is bounded. When input cannot be inspected safely, fail closed
	// only for an exact lark-cli token so similarly named helpers remain usable.
	if len(source) > maxLarkShellCommandBytes || depth >= maxLarkShellPayloadDepth {
		return boundedLarkFallbackContainsExecution(source)
	}
	file, err := parseLarkShell(source)
	if err != nil {
		return boundedLarkFallbackContainsExecution(source)
	}

	blocked := false
	syntax.Walk(file, func(node syntax.Node) bool {
		if node == nil || blocked {
			return false
		}
		call, ok := node.(*syntax.CallExpr)
		if !ok {
			return true
		}
		blocked = larkCallContainsExecution(call, depth)
		return !blocked
	})
	return blocked
}

func parseLarkShell(source string) (*syntax.File, error) {
	var lastErr error
	for _, language := range []syntax.LangVariant{syntax.LangBash, syntax.LangPOSIX} {
		file, err := syntax.NewParser(syntax.Variant(language)).Parse(strings.NewReader(source), "")
		if err == nil {
			return file, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func larkCallContainsExecution(call *syntax.CallExpr, depth int) bool {
	words := make([]string, len(call.Args))
	known := make([]bool, len(call.Args))
	for index, word := range call.Args {
		words[index], known[index] = staticLarkWord(word)
	}
	return larkInvocationContainsExecution(words, known, depth)
}

// staticLarkWord accepts literals and static quoted fragments. Parameter expansion,
// aliases, and symlink targets are intentionally not guessed; those dynamic cases stay
// inside the isolated bash sandbox and cannot be proven from syntax alone.
func staticLarkWord(word *syntax.Word) (string, bool) {
	return staticLarkWordParts(word.Parts, false)
}

func staticLarkWordParts(parts []syntax.WordPart, doubleQuoted bool) (string, bool) {
	var value strings.Builder
	for _, part := range parts {
		switch part := part.(type) {
		case *syntax.Lit:
			value.WriteString(unescapeStaticLarkLiteral(part.Value, doubleQuoted))
		case *syntax.SglQuoted:
			value.WriteString(part.Value)
		case *syntax.DblQuoted:
			quoted, ok := staticLarkWordParts(part.Parts, true)
			if !ok {
				return "", false
			}
			value.WriteString(quoted)
		default:
			return "", false
		}
	}
	return value.String(), true
}

func unescapeStaticLarkLiteral(value string, doubleQuoted bool) string {
	var normalized strings.Builder
	for index := 0; index < len(value); index++ {
		if value[index] != '\\' || index+1 >= len(value) {
			normalized.WriteByte(value[index])
			continue
		}
		next := value[index+1]
		if doubleQuoted && !strings.ContainsRune("$`\"\\\n", rune(next)) {
			normalized.WriteByte(value[index])
			continue
		}
		index++
		if next != '\n' {
			normalized.WriteByte(next)
		}
	}
	return normalized.String()
}

func larkInvocationContainsExecution(words []string, known []bool, depth int) bool {
	index := 0
	for index < len(words) {
		if !known[index] {
			return false
		}
		name := path.Base(words[index])
		if name == "lark-cli" {
			return true
		}
		switch name {
		case "sudo":
			var ok bool
			index, ok = skipLarkStaticOptions(words, known, index+1, map[string]bool{
				"-u": true, "--user": true, "-g": true, "--group": true,
				"-h": true, "--host": true, "-p": true, "--prompt": true,
				"-C": true, "--close-from": true, "-T": true, "--command-timeout": true,
				"-r": true, "--role": true, "-t": true, "--type": true,
			})
			if !ok {
				return false
			}
		case "command":
			index++
			for index < len(words) {
				if !known[index] {
					return false
				}
				option := words[index]
				if option == "--" {
					index++
					break
				}
				if !strings.HasPrefix(option, "-") || option == "-" {
					break
				}
				if strings.Contains(option, "v") || strings.Contains(option, "V") {
					return false // command -v/-V only inspects the command name
				}
				index++
			}
		case "exec":
			var ok bool
			index, ok = skipLarkStaticOptions(words, known, index+1, map[string]bool{"-a": true})
			if !ok {
				return false
			}
		case "env":
			blocked, next, ok := inspectLarkEnv(words, known, index+1, depth)
			if blocked || !ok {
				return blocked
			}
			index = next
		case "nohup":
			var ok bool
			index, ok = skipLarkStaticOptions(words, known, index+1, nil)
			if !ok {
				return false
			}
		case "time":
			var ok bool
			index, ok = skipLarkStaticOptions(words, known, index+1, map[string]bool{
				"-f": true, "--format": true, "-o": true, "--output": true,
			})
			if !ok {
				return false
			}
		default:
			switch name {
			case "sh", "bash", "zsh", "dash", "ksh":
				return inspectStaticShellPayload(words, known, index+1, depth)
			case "eval":
				return inspectStaticEvalPayload(words, known, index+1, depth)
			case "xargs":
				return inspectStaticXargs(words, known, index+1, depth)
			case "find":
				return inspectStaticFindExec(words, known, index+1, depth)
			default:
				return false
			}
		}
	}
	return false
}

func skipLarkStaticOptions(words []string, known []bool, index int, valueOptions map[string]bool) (int, bool) {
	for index < len(words) {
		if !known[index] {
			return index, false
		}
		option := words[index]
		if option == "--" {
			return index + 1, true
		}
		if !strings.HasPrefix(option, "-") || option == "-" {
			return index, true
		}
		index++
		if valueOptions[option] {
			if index >= len(words) || !known[index] {
				return index, false
			}
			index++
		}
	}
	return index, true
}

func inspectLarkEnv(words []string, known []bool, index, depth int) (blocked bool, next int, ok bool) {
	for index < len(words) {
		if !known[index] {
			return false, index, false
		}
		word := words[index]
		switch {
		case envAssignRe.MatchString(word):
			index++
		case word == "--":
			return false, index + 1, true
		case word == "-S" || word == "--split-string":
			if index+1 >= len(words) || !known[index+1] {
				return false, index, false
			}
			if larkShellContainsExecution(words[index+1], depth+1) {
				return true, index, true
			}
			index += 2
		case strings.HasPrefix(word, "--split-string="):
			if larkShellContainsExecution(strings.TrimPrefix(word, "--split-string="), depth+1) {
				return true, index, true
			}
			index++
		case strings.HasPrefix(word, "-S") && len(word) > 2:
			if larkShellContainsExecution(word[2:], depth+1) {
				return true, index, true
			}
			index++
		case strings.HasPrefix(word, "-") && word != "-":
			index++
			if word == "-u" || word == "--unset" || word == "-C" || word == "--chdir" {
				if index >= len(words) || !known[index] {
					return false, index, false
				}
				index++
			}
		default:
			return false, index, true
		}
	}
	return false, index, true
}

func inspectStaticShellPayload(words []string, known []bool, index, depth int) bool {
	valueOptions := map[string]bool{"-O": true, "-o": true, "--rcfile": true, "--init-file": true}
	for index < len(words) {
		if !known[index] {
			return false
		}
		option := words[index]
		if option == "--" || !strings.HasPrefix(option, "-") || option == "-" {
			return false
		}
		if strings.HasPrefix(option, "-") && !strings.HasPrefix(option, "--") && strings.Contains(option[1:], "c") {
			return index+1 < len(words) && known[index+1] && larkShellContainsExecution(words[index+1], depth+1)
		}
		index++
		if valueOptions[option] {
			if index >= len(words) || !known[index] {
				return false
			}
			index++
		}
	}
	return false
}

func inspectStaticEvalPayload(words []string, known []bool, index, depth int) bool {
	if index >= len(words) {
		return false
	}
	if known[index] && words[index] == "--" {
		index++
	} else if known[index] && strings.HasPrefix(words[index], "-") && words[index] != "-" {
		return false // Bash eval rejects options other than -- without executing payload.
	}
	if index >= len(words) {
		return false
	}
	for i := index; i < len(words); i++ {
		if !known[i] {
			return false
		}
	}
	return larkShellContainsExecution(strings.Join(words[index:], " "), depth+1)
}

// boundedLarkFallbackContainsExecution is used only when the real parser is
// intentionally unavailable. Exact mentions default to deny; only a complete,
// static call to a small informational allowlist is proven safe. The scan is
// linear with constant extra memory and never enters AST parsing.
func boundedLarkFallbackContainsExecution(source string) bool {
	const target = "lark-cli"
	var head [2][maxLarkFallbackHeadBytes]byte
	var headLen [2]int
	var headOverflow [2]bool
	headCount := 0
	wordStarted := false
	baseIndex := 0
	baseOverflow := false
	mentionIndex := 0
	mentionPending := false
	mentionPrevName := false
	candidate := false
	unsafe := false
	var quote byte

	isNameByte := func(char byte) bool {
		return char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9' || char == '_' || char == '-'
	}
	feedMention := func(char byte) {
		boundaryBefore := !mentionPrevName
		if mentionPending {
			if char != '/' && !isNameByte(char) {
				candidate = true
			}
			mentionPending = false
			mentionIndex = 0
		}
		if mentionIndex > 0 && mentionIndex < len(target) && char == target[mentionIndex] {
			mentionIndex++
			mentionPending = mentionIndex == len(target)
		} else if char == target[0] && boundaryBefore {
			mentionIndex = 1
		} else {
			mentionIndex = 0
		}
		mentionPrevName = isNameByte(char)
	}
	feedWord := func(char byte) {
		wordStarted = true
		feedMention(char)
		if char == '/' {
			baseIndex = 0
			baseOverflow = false
		} else if !baseOverflow {
			if baseIndex < len(target) && char == target[baseIndex] {
				baseIndex++
			} else {
				baseOverflow = true
			}
		}
		if headCount < len(head) {
			if headLen[headCount] < len(head[headCount]) {
				head[headCount][headLen[headCount]] = char
				headLen[headCount]++
			} else {
				headOverflow[headCount] = true
			}
		}
	}
	flushWord := func(boundary byte) {
		feedMention(boundary)
		if !wordStarted {
			return
		}
		if !baseOverflow && baseIndex == len(target) {
			candidate = true
		}
		if headCount < len(head) {
			headCount++
		}
		wordStarted = false
		baseIndex = 0
		baseOverflow = false
	}

	for index := 0; index < len(source); index++ {
		char := source[index]
		if quote == '\'' {
			if char == '\'' {
				quote = 0
			} else {
				feedWord(char)
			}
			continue
		}
		if quote == '"' {
			if char == '"' {
				quote = 0
				continue
			}
			if char == '\\' && index+1 < len(source) && strings.ContainsRune("$`\"\\\n", rune(source[index+1])) {
				index++
				if source[index] != '\n' {
					feedWord(source[index])
				}
				continue
			}
			if char == '$' || char == '`' {
				unsafe = true
			}
			feedWord(char)
			continue
		}

		switch char {
		case '\'', '"':
			quote = char
			wordStarted = true
		case '\\':
			wordStarted = true
			if index+1 >= len(source) {
				unsafe = true
				continue
			}
			index++
			if source[index] != '\n' {
				feedWord(source[index])
			}
		case ' ', '\t', '\r':
			flushWord(char)
		case ';', '&', '|', '<', '>', '(', ')', '{', '}', '\n':
			flushWord(char)
			unsafe = true
		case '#':
			if wordStarted {
				feedWord(char)
			} else {
				flushWord(char)
				unsafe = true
			}
		case '$', '`':
			unsafe = true
			feedWord(char)
		default:
			feedWord(char)
		}
	}
	flushWord(0)
	if mentionPending {
		candidate = true
	}
	if quote != 0 {
		unsafe = true
	}
	if !candidate {
		return false
	}
	if unsafe || headCount == 0 || headOverflow[0] {
		return true
	}
	first := path.Base(string(head[0][:headLen[0]]))
	switch first {
	case "echo", "printf", "rg", "grep":
		return false
	case "command":
		if headCount >= 2 && !headOverflow[1] {
			second := string(head[1][:headLen[1]])
			return second != "-v" && second != "-V"
		}
	}
	return true
}

func inspectStaticXargs(words []string, known []bool, index, depth int) bool {
	valueOptions := map[string]bool{
		"-a": true, "--arg-file": true, "-d": true, "--delimiter": true,
		"-E": true, "--eof": true, "-I": true, "--replace": true,
		"-L": true, "--max-lines": true, "-n": true, "--max-args": true,
		"-P": true, "--max-procs": true, "-s": true, "--max-chars": true,
	}
	next, ok := skipLarkStaticOptions(words, known, index, valueOptions)
	if !ok || next >= len(words) {
		return false
	}
	return larkInvocationContainsExecution(words[next:], known[next:], depth)
}

func inspectStaticFindExec(words []string, known []bool, index, depth int) bool {
	for index < len(words) {
		if !known[index] {
			index++
			continue
		}
		if words[index] != "-exec" && words[index] != "-execdir" {
			index++
			continue
		}
		start := index + 1
		end := start
		for end < len(words) && (!known[end] || (words[end] != ";" && words[end] != "+")) {
			end++
		}
		if start < end && larkInvocationContainsExecution(words[start:end], known[start:end], depth) {
			return true
		}
		index = end + 1
	}
	return false
}
