package feishu

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	pkgcrypto "numind-server/internal/pkg/crypto"
)

const (
	// SkillReaderPageBytes is the maximum UTF-8 content in one tool page.
	SkillReaderPageBytes = 32 << 10
	// SkillReaderDefaultTTL bounds cursor and receipt reuse within one run.
	SkillReaderDefaultTTL = 15 * time.Minute
	// SkillReaderMinTTL prevents unusably short production tokens.
	SkillReaderMinTTL = time.Minute
	// SkillReaderMaxTTL limits replay lifetime if a receipt is exposed.
	SkillReaderMaxTTL = time.Hour
	// SkillReaderMaxConcurrentProcesses is the process-wide embedded skill read
	// ceiling shared by all reader instances.
	SkillReaderMaxConcurrentProcesses = 4

	// SkillDomainDocs requires lark-shared and lark-doc.
	SkillDomainDocs = "docs"
	// SkillDomainBase requires lark-shared and lark-base.
	SkillDomainBase = "base"
	// SkillDomainWiki requires lark-shared and lark-wiki.
	SkillDomainWiki = "wiki"
	// SkillDomainWikiContent requires shared, Wiki, and Docs receipts because
	// Wiki node content is manipulated through Docs commands.
	SkillDomainWikiContent = "wiki_content"

	skillReaderTokenVersion  = 1
	skillReaderMaxTokenBytes = 4096
	skillReaderMaxPathBytes  = 512

	skillCursorKind  = "skill_cursor"
	skillReceiptKind = "skill_receipt"
)

var (
	// ErrSkillReadInvalid is returned for malformed requests without echoing the
	// supplied path, cursor, or receipt.
	ErrSkillReadInvalid = errors.New("feishu skill request rejected")
	// ErrSkillReadFailed is a safe process/envelope failure without CLI stderr or
	// embedded skill content.
	ErrSkillReadFailed = errors.New("feishu skill read failed")
	// ErrSkillReceiptInvalid deliberately collapses malformed, expired, and
	// mismatched receipt details into one non-sensitive result.
	ErrSkillReceiptInvalid = errors.New("feishu skill receipt rejected")

	markdownLinkPattern = regexp.MustCompile(`\]\(([^)\s]+)`)
	skillProcessSlots   = make(chan struct{}, SkillReaderMaxConcurrentProcesses)
)

// SkillReadRequest asks for one page of a fixed embedded lark-cli skill.
type SkillReadRequest struct {
	AgentRunID uint64
	Skill      string
	Reference  string
	Cursor     string
}

// SkillReadPage is one immutable-by-value view of an embedded resource.
type SkillReadPage struct {
	Skill      string
	Path       string
	Content    string
	References []string
	Cursor     string
	Receipt    string
}

// SkillReader reads only fixed lark-cli resources and verifies opaque receipts.
// All fields are immutable after construction; methods are concurrent-safe.
type SkillReader struct {
	runner          *ControlledLarkCLIRunner
	cursorKey       []byte
	receiptKey      []byte
	now             func() time.Time
	ttl             time.Duration
	pageBytes       int
	maxOutputBytes  int
	processStarted  func()
	processFinished func()
}

type skillReaderOptions struct {
	binary          string
	now             func() time.Time
	ttl             time.Duration
	pageBytes       int
	maxOutputBytes  int
	processStarted  func()
	processFinished func()
}

type skillCLIResource struct {
	Skill    string
	Path     string
	Content  string
	Guidance *string
}

type skillTokenPayload struct {
	Kind       string `json:"kind"`
	Version    int    `json:"version"`
	RunID      uint64 `json:"run_id"`
	Skill      string `json:"skill"`
	Reference  string `json:"reference,omitempty"`
	Offset     int64  `json:"offset,omitempty"`
	Digest     string `json:"digest"`
	CLIVersion string `json:"cli_version,omitempty"`
	ExpiresAt  int64  `json:"expires_at"`
}

// NewSkillReader derives dedicated receipt/cursor keys from the existing
// base64-encoded 32-byte security.thirdparty_token_key.
func NewSkillReader(thirdPartyTokenKeyBase64 string) (*SkillReader, error) {
	return newSkillReaderWithOptions(thirdPartyTokenKeyBase64, skillReaderOptions{})
}

func newSkillReaderWithOptions(thirdPartyTokenKeyBase64 string, options skillReaderOptions) (*SkillReader, error) {
	rawKey, err := base64.StdEncoding.DecodeString(thirdPartyTokenKeyBase64)
	if err != nil || len(rawKey) != pkgcrypto.KeyLen {
		return nil, fmt.Errorf("feishu: initialize skill reader key: %w", ErrSkillReadInvalid)
	}
	ttl := options.ttl
	if ttl == 0 {
		ttl = SkillReaderDefaultTTL
	}
	if ttl < SkillReaderMinTTL || ttl > SkillReaderMaxTTL {
		return nil, fmt.Errorf("feishu: initialize skill reader ttl: %w", ErrSkillReadInvalid)
	}
	pageBytes := options.pageBytes
	if pageBytes == 0 {
		pageBytes = SkillReaderPageBytes
	}
	if pageBytes < utf8.UTFMax || pageBytes > SkillReaderPageBytes {
		return nil, fmt.Errorf("feishu: initialize skill reader page size: %w", ErrSkillReadInvalid)
	}
	maxOutputBytes := options.maxOutputBytes
	if maxOutputBytes == 0 {
		maxOutputBytes = ControlledLarkCLIMaxStdoutBytes
	}
	if maxOutputBytes <= 0 || maxOutputBytes > ControlledLarkCLIMaxStdoutBytes {
		return nil, fmt.Errorf("feishu: initialize skill reader output limit: %w", ErrSkillReadInvalid)
	}
	now := options.now
	if now == nil {
		now = time.Now
	}
	runner := &ControlledLarkCLIRunner{binary: options.binary}
	if _, err := runner.binaryPath(); err != nil {
		return nil, fmt.Errorf("feishu: initialize skill reader runner: %w", ErrSkillReadInvalid)
	}
	return &SkillReader{
		runner:          runner,
		cursorKey:       deriveSkillKey(rawKey, "feishu-skill-cursor-v1"),
		receiptKey:      deriveSkillKey(rawKey, "feishu-skill-receipt-v1"),
		now:             now,
		ttl:             ttl,
		pageBytes:       pageBytes,
		maxOutputBytes:  maxOutputBytes,
		processStarted:  options.processStarted,
		processFinished: options.processFinished,
	}, nil
}

// Read re-reads the complete fixed resource on every page, binds continuation
// to its digest, and mints a receipt only after the main SKILL.md final page.
// The receipt proves delivery of that main file only. References remain
// instruction-driven reads and intentionally never mint or extend a receipt.
func (r *SkillReader) Read(ctx context.Context, request SkillReadRequest) (*SkillReadPage, error) {
	if r == nil || ctx == nil || request.AgentRunID == 0 || !allowedSkill(request.Skill) {
		return nil, ErrSkillReadInvalid
	}
	if request.Reference != "" && !validSkillReference(request.Reference) {
		return nil, ErrSkillReadInvalid
	}

	offset := int64(0)
	expectedDigest := ""
	if request.Cursor != "" {
		payload, err := r.decodeToken(request.Cursor, skillCursorKind)
		if err != nil || payload.RunID != request.AgentRunID || payload.Skill != request.Skill || payload.Reference != request.Reference {
			return nil, ErrSkillReadInvalid
		}
		if payload.Offset <= 0 {
			return nil, ErrSkillReadInvalid
		}
		offset = payload.Offset
		expectedDigest = payload.Digest
	}

	var references []string
	if request.Reference != "" {
		main, err := r.readResource(ctx, request.Skill, "")
		if err != nil {
			return nil, err
		}
		references = declaredSkillReferences(main.Content)
		if !containsExactString(references, request.Reference) {
			return nil, ErrSkillReadInvalid
		}
	}
	resource, err := r.readResource(ctx, request.Skill, request.Reference)
	if err != nil {
		return nil, err
	}
	if request.Reference == "" {
		references = declaredSkillReferences(resource.Content)
	}

	content := []byte(resource.Content)
	digestRaw := sha256.Sum256(content)
	digest := base64.RawURLEncoding.EncodeToString(digestRaw[:])
	if expectedDigest != "" && !hmac.Equal([]byte(expectedDigest), []byte(digest)) {
		return nil, ErrSkillReadInvalid
	}
	if offset < 0 || offset >= int64(len(content)) || (offset > 0 && !utf8.RuneStart(content[offset])) {
		if !(offset == 0 && len(content) == 0) {
			return nil, ErrSkillReadInvalid
		}
	}
	start := int(offset)
	end := min(start+r.pageBytes, len(content))
	for end < len(content) && end > start && !utf8.RuneStart(content[end]) {
		end--
	}
	if end == start && end < len(content) {
		return nil, ErrSkillReadFailed
	}

	page := &SkillReadPage{
		Skill:      request.Skill,
		Path:       resource.Path,
		Content:    string(append([]byte(nil), content[start:end]...)),
		References: append([]string(nil), references...),
	}
	expiresAt := r.now().Add(r.ttl).Unix()
	if end < len(content) {
		page.Cursor, err = r.encodeToken(skillTokenPayload{
			Kind:      skillCursorKind,
			Version:   skillReaderTokenVersion,
			RunID:     request.AgentRunID,
			Skill:     request.Skill,
			Reference: request.Reference,
			Offset:    int64(end),
			Digest:    digest,
			ExpiresAt: expiresAt,
		})
		if err != nil {
			return nil, ErrSkillReadFailed
		}
		return page, nil
	}
	if request.Reference == "" {
		page.Receipt, err = r.encodeToken(skillTokenPayload{
			Kind:       skillReceiptKind,
			Version:    skillReaderTokenVersion,
			RunID:      request.AgentRunID,
			Skill:      request.Skill,
			Digest:     digest,
			CLIVersion: LarkCLIVersion,
			ExpiresAt:  expiresAt,
		})
		if err != nil {
			return nil, ErrSkillReadFailed
		}
	}
	return page, nil
}

// Verify validates a receipt against one run, skill, and CLI version.
func (r *SkillReader) Verify(receipt string, runID uint64, skill, cliVersion string) error {
	if r == nil || runID == 0 || !allowedSkill(skill) || cliVersion == "" {
		return ErrSkillReceiptInvalid
	}
	payload, err := r.decodeToken(receipt, skillReceiptKind)
	if err != nil || payload.RunID != runID || payload.Skill != skill || payload.CLIVersion != cliVersion || payload.Reference != "" || payload.Offset != 0 {
		return ErrSkillReceiptInvalid
	}
	return nil
}

// VerifyRequired validates the exact main-skill receipt set for a command
// domain. These receipts do not claim that task-specific references were read;
// the main skill instructs the Agent to fetch those references when needed.
// Wiki content therefore has an explicit composite domain for Task 7 to select.
func (r *SkillReader) VerifyRequired(receipts []string, runID uint64, domain string) error {
	var required map[string]struct{}
	switch domain {
	case SkillDomainDocs:
		required = map[string]struct{}{"lark-shared": {}, "lark-doc": {}}
	case SkillDomainBase:
		required = map[string]struct{}{"lark-shared": {}, "lark-base": {}}
	case SkillDomainWiki:
		required = map[string]struct{}{"lark-shared": {}, "lark-wiki": {}}
	case SkillDomainWikiContent:
		required = map[string]struct{}{"lark-shared": {}, "lark-wiki": {}, "lark-doc": {}}
	default:
		return ErrSkillReceiptInvalid
	}
	if r == nil || runID == 0 || len(receipts) != len(required) {
		return ErrSkillReceiptInvalid
	}
	seen := make(map[string]struct{}, len(receipts))
	for _, receipt := range receipts {
		payload, err := r.decodeToken(receipt, skillReceiptKind)
		if err != nil || payload.RunID != runID || payload.CLIVersion != LarkCLIVersion || payload.Reference != "" || payload.Offset != 0 {
			return ErrSkillReceiptInvalid
		}
		if _, needed := required[payload.Skill]; !needed {
			return ErrSkillReceiptInvalid
		}
		if _, duplicate := seen[payload.Skill]; duplicate {
			return ErrSkillReceiptInvalid
		}
		seen[payload.Skill] = struct{}{}
	}
	if len(seen) != len(required) {
		return ErrSkillReceiptInvalid
	}
	return nil
}

func (r *SkillReader) readResource(ctx context.Context, skill, reference string) (*skillCLIResource, error) {
	argv := []string{"skills", "read", skill}
	expectedPath := "SKILL.md"
	if reference != "" {
		argv = append(argv, reference)
		expectedPath = reference
	}
	argv = append(argv, "--json")
	if err := validateControlledCLIInput(argv, nil); err != nil {
		return nil, ErrSkillReadInvalid
	}
	if !acquireSkillProcessSlot(ctx) {
		return nil, ErrSkillReadFailed
	}
	defer releaseSkillProcessSlot()
	if r.processStarted != nil {
		if r.processFinished != nil {
			defer r.processFinished()
		}
		r.processStarted()
	}
	binary, err := r.runner.binaryPath()
	if err != nil {
		return nil, ErrSkillReadFailed
	}
	result, waitErr, processErr := r.runner.runProcess(ctx, binary, argv, nil, "", ControlledLarkCLITimeout)
	if processErr != nil || waitErr != nil || result.StdoutTruncated || result.StderrTruncated || result.ExitCode != 0 {
		return nil, ErrSkillReadFailed
	}
	if len(result.Stdout) == 0 || len(result.Stdout) > r.maxOutputBytes {
		return nil, ErrSkillReadFailed
	}
	resource, err := decodeSkillCLIResource(result.Stdout)
	if err != nil || resource.Skill != skill || resource.Path != expectedPath || !utf8.ValidString(resource.Content) || strings.TrimSpace(resource.Content) == "" {
		return nil, ErrSkillReadFailed
	}
	if reference == "" && (resource.Guidance == nil || strings.TrimSpace(*resource.Guidance) == "") {
		return nil, ErrSkillReadFailed
	}
	return resource, nil
}

func decodeSkillCLIResource(raw []byte) (*skillCLIResource, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var wire struct {
		Skill    *string `json:"skill"`
		Path     *string `json:"path"`
		Content  *string `json:"content"`
		Guidance *string `json:"guidance,omitempty"`
	}
	if err := decoder.Decode(&wire); err != nil {
		return nil, ErrSkillReadFailed
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, ErrSkillReadFailed
	}
	if wire.Skill == nil || wire.Path == nil || wire.Content == nil || *wire.Skill == "" || *wire.Path == "" {
		return nil, ErrSkillReadFailed
	}
	return &skillCLIResource{Skill: *wire.Skill, Path: *wire.Path, Content: *wire.Content, Guidance: wire.Guidance}, nil
}

func allowedSkill(skill string) bool {
	switch skill {
	case "lark-shared", "lark-doc", "lark-base", "lark-wiki":
		return true
	default:
		return false
	}
}

// validSkillReference is deliberately syntactic. Production passes the path to
// the fixed CLI's embedded resource registry and never resolves, stats, or
// follows it through the server OS filesystem.
func validSkillReference(reference string) bool {
	if reference == "" || !strings.HasPrefix(reference, "references/") || len(reference) > skillReaderMaxPathBytes || !utf8.ValidString(reference) || strings.IndexByte(reference, 0) >= 0 || strings.Contains(reference, `\`) || strings.HasPrefix(reference, "/") || path.IsAbs(reference) || path.Clean(reference) != reference {
		return false
	}
	for _, segment := range strings.Split(reference, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	for _, char := range reference {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.' || char == '/' {
			continue
		}
		return false
	}
	return true
}

func declaredSkillReferences(content string) []string {
	unique := make(map[string]struct{})
	for _, match := range markdownLinkPattern.FindAllStringSubmatch(content, -1) {
		if len(match) != 2 || !validSkillReference(match[1]) {
			continue
		}
		unique[match[1]] = struct{}{}
	}
	references := make([]string, 0, len(unique))
	for reference := range unique {
		references = append(references, reference)
	}
	sort.Strings(references)
	return references
}

func containsExactString(values []string, expected string) bool {
	index := sort.SearchStrings(values, expected)
	return index < len(values) && values[index] == expected
}

func deriveSkillKey(rawKey []byte, label string) []byte {
	mac := hmac.New(sha256.New, rawKey)
	_, _ = mac.Write([]byte(label))
	return mac.Sum(nil)
}

func (r *SkillReader) encodeToken(payload skillTokenPayload) (string, error) {
	encodedPayload, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	key := r.tokenKey(payload.Kind)
	if len(key) == 0 {
		return "", ErrSkillReadFailed
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(encodedPayload)
	signature := mac.Sum(nil)
	return base64.RawURLEncoding.EncodeToString(encodedPayload) + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func (r *SkillReader) decodeToken(token, expectedKind string) (*skillTokenPayload, error) {
	if r == nil || token == "" || len(token) > skillReaderMaxTokenBytes || strings.ContainsAny(token, "\r\n") || strings.Count(token, ".") != 1 {
		return nil, ErrSkillReceiptInvalid
	}
	payloadPart, signaturePart, _ := strings.Cut(token, ".")
	if payloadPart == "" || signaturePart == "" {
		return nil, ErrSkillReceiptInvalid
	}
	payloadRaw, err := base64.RawURLEncoding.DecodeString(payloadPart)
	if err != nil || len(payloadRaw) == 0 || len(payloadRaw) > skillReaderMaxTokenBytes || base64.RawURLEncoding.EncodeToString(payloadRaw) != payloadPart {
		return nil, ErrSkillReceiptInvalid
	}
	signature, err := base64.RawURLEncoding.DecodeString(signaturePart)
	if err != nil || len(signature) != sha256.Size || base64.RawURLEncoding.EncodeToString(signature) != signaturePart {
		return nil, ErrSkillReceiptInvalid
	}
	key := r.tokenKey(expectedKind)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(payloadRaw)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return nil, ErrSkillReceiptInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(payloadRaw))
	decoder.DisallowUnknownFields()
	var payload skillTokenPayload
	if err := decoder.Decode(&payload); err != nil {
		return nil, ErrSkillReceiptInvalid
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, ErrSkillReceiptInvalid
	}
	if payload.Kind != expectedKind || payload.Version != skillReaderTokenVersion || payload.RunID == 0 || !allowedSkill(payload.Skill) || payload.ExpiresAt <= r.now().Unix() {
		return nil, ErrSkillReceiptInvalid
	}
	digest, err := base64.RawURLEncoding.DecodeString(payload.Digest)
	if err != nil || len(digest) != sha256.Size {
		return nil, ErrSkillReceiptInvalid
	}
	if expectedKind == skillCursorKind {
		if payload.Offset <= 0 || payload.CLIVersion != "" || (payload.Reference != "" && !validSkillReference(payload.Reference)) {
			return nil, ErrSkillReceiptInvalid
		}
	} else if payload.Offset != 0 || payload.Reference != "" || payload.CLIVersion == "" {
		return nil, ErrSkillReceiptInvalid
	}
	return &payload, nil
}

func acquireSkillProcessSlot(ctx context.Context) bool {
	if ctx == nil || ctx.Err() != nil {
		return false
	}
	select {
	case skillProcessSlots <- struct{}{}:
		if ctx.Err() != nil {
			<-skillProcessSlots
			return false
		}
		return true
	case <-ctx.Done():
		return false
	}
}

func releaseSkillProcessSlot() {
	<-skillProcessSlots
}

func (r *SkillReader) tokenKey(kind string) []byte {
	switch kind {
	case skillCursorKind:
		return r.cursorKey
	case skillReceiptKind:
		return r.receiptKey
	default:
		return nil
	}
}
