package feishu

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func TestSkillReader_UsesExactControlledCLIAndDeclaredReferences(t *testing.T) {
	t.Parallel()

	h := newSkillReaderHarness(t, skillReaderOptions{})
	main := "# Doc\n[Fetch](references/fetch.md)\n[Style](references/style/guide.md)\n" +
		"[Cross](../lark-base/references/no.md)\n[Absolute](/tmp/no.md)\n"
	h.writeResource("lark-doc", "SKILL.md", main, true)
	h.writeResource("lark-doc", "references/fetch.md", "fetch reference", false)

	page, err := h.reader.Read(h.context(), SkillReadRequest{AgentRunID: 11, Skill: "lark-doc"})
	require.NoError(t, err)
	require.Equal(t, main, page.Content)
	require.Equal(t, []string{"references/fetch.md", "references/style/guide.md"}, page.References)
	require.Empty(t, page.Cursor)
	require.NotEmpty(t, page.Receipt)
	require.Equal(t, []string{"HOME=unset|4|skills|read|lark-doc|--json"}, h.invocations())

	reference, err := h.reader.Read(h.context(), SkillReadRequest{
		AgentRunID: 11,
		Skill:      "lark-doc",
		Reference:  "references/fetch.md",
	})
	require.NoError(t, err)
	require.Equal(t, "fetch reference", reference.Content)
	require.Empty(t, reference.Receipt, "a reference can never mint a skill receipt")
	require.Equal(t, []string{
		"HOME=unset|4|skills|read|lark-doc|--json",
		"HOME=unset|4|skills|read|lark-doc|--json",
		"HOME=unset|5|skills|read|lark-doc|references/fetch.md|--json",
	}, h.invocations())

	page.References[0] = "mutated"
	again, err := h.reader.Read(h.context(), SkillReadRequest{AgentRunID: 11, Skill: "lark-doc"})
	require.NoError(t, err)
	require.Equal(t, "references/fetch.md", again.References[0])
}

func TestSkillReader_ResolvesDeclaredReferenceBasename(t *testing.T) {
	t.Parallel()

	h := newSkillReaderHarness(t, skillReaderOptions{})
	h.writeResource(
		"lark-doc",
		"SKILL.md",
		"# Doc\n[Update](references/style/lark-doc-update.md)",
		true,
	)
	h.writeResource(
		"lark-doc",
		"references/style/lark-doc-update.md",
		"update reference",
		false,
	)

	page, err := h.reader.Read(h.context(), SkillReadRequest{
		AgentRunID: 224,
		Skill:      "lark-doc",
		Reference:  "lark-doc-update.md",
	})
	require.NoError(t, err)
	require.Equal(t, "references/style/lark-doc-update.md", page.Path)
	require.Equal(t, "update reference", page.Content)
	require.Equal(t, []string{
		"HOME=unset|4|skills|read|lark-doc|--json",
		"HOME=unset|5|skills|read|lark-doc|references/style/lark-doc-update.md|--json",
	}, h.invocations())
}

// Customer regression (Dev run 227): the Agent selected the correct declared
// Drive/Docs references but placed each reference in the legacy cursor field.
// This is an unambiguous field-placement mistake, not an attempt to bypass the
// current-skill declared-reference boundary.
func TestSkillReader_RepairsDeclaredMarkdownReferencePlacedInCursor(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		skill     string
		reference string
	}{
		{skill: "lark-drive", reference: "references/lark-drive-search.md"},
		{skill: "lark-doc", reference: "references/lark-doc-fetch.md"},
	} {
		testCase := testCase
		t.Run(testCase.skill, func(t *testing.T) {
			t.Parallel()

			h := newSkillReaderHarness(t, skillReaderOptions{})
			h.writeResource(testCase.skill, "SKILL.md", "[Read]("+testCase.reference+")", true)
			h.writeResource(testCase.skill, testCase.reference, "controlled reference", false)

			page, err := h.reader.Read(h.context(), SkillReadRequest{
				AgentRunID: 227,
				Skill:      testCase.skill,
				Cursor:     testCase.reference,
			})
			require.NoError(t, err)
			require.Equal(t, testCase.reference, page.Path)
			require.Equal(t, "controlled reference", page.Content)
			require.Equal(t, []string{
				"HOME=unset|4|skills|read|" + testCase.skill + "|--json",
				"HOME=unset|5|skills|read|" + testCase.skill + "|" + testCase.reference + "|--json",
			}, h.invocations())
		})
	}
}

func TestSkillReader_ReferenceBasenameResolutionFailsClosed(t *testing.T) {
	t.Parallel()

	t.Run("root-level unique basename", func(t *testing.T) {
		t.Parallel()
		h := newSkillReaderHarness(t, skillReaderOptions{})
		h.writeResource("lark-doc", "SKILL.md", "[Fetch](references/fetch.md)", true)
		h.writeResource("lark-doc", "references/fetch.md", "fetch", false)

		page, err := h.reader.Read(h.context(), SkillReadRequest{AgentRunID: 1, Skill: "lark-doc", Reference: "fetch.md"})
		require.NoError(t, err)
		require.Equal(t, "references/fetch.md", page.Path)
	})

	t.Run("undeclared basename", func(t *testing.T) {
		t.Parallel()
		h := newSkillReaderHarness(t, skillReaderOptions{})
		h.writeResource("lark-doc", "SKILL.md", "[Allowed](references/allowed.md)", true)

		_, err := h.reader.Read(h.context(), SkillReadRequest{AgentRunID: 2, Skill: "lark-doc", Reference: "missing.md"})
		require.ErrorIs(t, err, ErrSkillReadInvalid)
		require.Len(t, h.invocations(), 1, "only the fixed main skill may be read to derive the allowlist")
	})

	t.Run("ambiguous basename", func(t *testing.T) {
		t.Parallel()
		h := newSkillReaderHarness(t, skillReaderOptions{})
		h.writeResource(
			"lark-doc",
			"SKILL.md",
			"[A](references/a/shared.md)\n[B](references/b/shared.md)",
			true,
		)

		_, err := h.reader.Read(h.context(), SkillReadRequest{AgentRunID: 3, Skill: "lark-doc", Reference: "shared.md"})
		require.ErrorIs(t, err, ErrSkillReadInvalid)
		require.Len(t, h.invocations(), 1, "ambiguity must fail before either reference is opened")
	})

	t.Run("ambiguous basename hidden beyond metadata cap", func(t *testing.T) {
		t.Parallel()
		h := newSkillReaderHarness(t, skillReaderOptions{})
		var main strings.Builder
		main.WriteString("[A](references/a/shared.md)\n")
		for index := 0; index < skillReaderMaxReferences+5; index++ {
			fmt.Fprintf(&main, "[Middle](references/middle/%03d.md)\n", index)
		}
		main.WriteString("[Z](references/z/shared.md)\n")
		h.writeResource("lark-doc", "SKILL.md", main.String(), true)
		h.writeResource("lark-doc", "references/a/shared.md", "must not be selected", false)

		_, err := h.reader.Read(h.context(), SkillReadRequest{AgentRunID: 33, Skill: "lark-doc", Reference: "shared.md"})
		require.ErrorIs(t, err, ErrSkillReadInvalid)
		require.Len(t, h.invocations(), 1, "a duplicate beyond metadata bounds must still make the basename ambiguous")
	})

	t.Run("does not search another skill", func(t *testing.T) {
		t.Parallel()
		h := newSkillReaderHarness(t, skillReaderOptions{})
		h.writeResource("lark-doc", "SKILL.md", "[Doc](references/doc.md)", true)
		h.writeResource("lark-base", "SKILL.md", "[Shared](references/shared.md)", true)

		_, err := h.reader.Read(h.context(), SkillReadRequest{AgentRunID: 4, Skill: "lark-doc", Reference: "shared.md"})
		require.ErrorIs(t, err, ErrSkillReadInvalid)
		require.Equal(t, []string{"HOME=unset|4|skills|read|lark-doc|--json"}, h.invocations())
	})
}

func TestSkillReader_ReferenceBasenameCursorBindsCanonicalResource(t *testing.T) {
	t.Parallel()

	h := newSkillReaderHarness(t, skillReaderOptions{pageBytes: 12})
	h.writeResource(
		"lark-doc",
		"SKILL.md",
		"[Update](references/style/lark-doc-update.md)\n[Other](references/other.md)",
		true,
	)
	h.writeResource("lark-doc", "references/style/lark-doc-update.md", strings.Repeat("update-", 12), false)
	h.writeResource("lark-doc", "references/other.md", strings.Repeat("other-", 12), false)
	h.writeResource("lark-base", "SKILL.md", "[Update](references/lark-doc-update.md)", true)
	h.writeResource("lark-base", "references/lark-doc-update.md", strings.Repeat("base-", 12), false)

	first, err := h.reader.Read(h.context(), SkillReadRequest{
		AgentRunID: 5,
		Skill:      "lark-doc",
		Reference:  "lark-doc-update.md",
	})
	require.NoError(t, err)
	require.NotEmpty(t, first.Cursor)

	_, err = h.reader.Read(h.context(), SkillReadRequest{
		AgentRunID: 5,
		Skill:      "lark-doc",
		Reference:  "references/style/lark-doc-update.md",
		Cursor:     first.Cursor,
	})
	require.NoError(t, err, "canonical spelling must continue a shorthand-created cursor")

	_, err = h.reader.Read(h.context(), SkillReadRequest{
		AgentRunID: 5,
		Skill:      "lark-doc",
		Reference:  "lark-doc-update.md",
		Cursor:     first.Cursor,
	})
	require.NoError(t, err, "shorthand spelling must continue its canonical cursor")

	_, err = h.reader.Read(h.context(), SkillReadRequest{
		AgentRunID: 5,
		Skill:      "lark-doc",
		Reference:  "other.md",
		Cursor:     first.Cursor,
	})
	require.ErrorIs(t, err, ErrSkillReadInvalid)

	_, err = h.reader.Read(h.context(), SkillReadRequest{
		AgentRunID: 5,
		Skill:      "lark-base",
		Reference:  "lark-doc-update.md",
		Cursor:     first.Cursor,
	})
	require.ErrorIs(t, err, ErrSkillReadInvalid)
}

func TestSkillReader_InvalidRequestDoesNotStartCLI(t *testing.T) {
	t.Parallel()

	h := newSkillReaderHarness(t, skillReaderOptions{})
	h.writeResource("lark-doc", "SKILL.md", "# Doc", true)
	tests := []SkillReadRequest{
		{AgentRunID: 0, Skill: "lark-doc"},
		{AgentRunID: 1, Skill: "lark-im"},
		{AgentRunID: 1, Skill: "lark-doc", Reference: "/absolute.md"},
		{AgentRunID: 1, Skill: "lark-doc", Reference: "../lark-base/x.md"},
		{AgentRunID: 1, Skill: "lark-doc", Reference: "references/../x.md"},
		{AgentRunID: 1, Skill: "lark-doc", Reference: `references\x.md`},
		{AgentRunID: 1, Skill: "lark-doc", Reference: "references//x.md"},
		{AgentRunID: 1, Skill: "lark-doc", Reference: "./references/x.md"},
		{AgentRunID: 1, Skill: "lark-doc", Reference: "references/x\x00.md"},
		{AgentRunID: 1, Skill: "lark-doc", Reference: "nested/x.md"},
		{AgentRunID: 1, Skill: "lark-doc", Reference: `x\y.md`},
		{AgentRunID: 1, Skill: "lark-doc", Reference: "x\x00.md"},
		{AgentRunID: 1, Skill: "lark-doc", Reference: "更新.md"},
		{AgentRunID: 1, Skill: "lark-doc", Reference: strings.Repeat("x", skillReaderMaxPathBytes+1)},
		{AgentRunID: 1, Skill: "lark-doc", Cursor: "malformed"},
	}
	for _, request := range tests {
		_, err := h.reader.Read(h.context(), request)
		require.Error(t, err, "%+v", request)
	}
	require.Empty(t, h.invocations())
}

func TestSkillReader_OnlyExposesDeclaredReferencesNamespace(t *testing.T) {
	t.Parallel()

	// This mirrors the real lark-doc 1.0.68 shape: the main skill links both
	// the supported reference and a helper script. Only references/ is readable.
	content := "Read [word stats](references/lark-doc-word-stat.md) and " +
		"run [the helper](scripts/doc_word_stat.py); ignore [assets](assets/template.md)."
	require.Equal(t, []string{"references/lark-doc-word-stat.md"}, declaredSkillReferences(content))
	require.True(t, validSkillReference("references/lark-doc-word-stat.md"))
	require.False(t, validSkillReference("scripts/doc_word_stat.py"))
	require.False(t, validSkillReference("assets/template.md"))

	h := newSkillReaderHarness(t, skillReaderOptions{})
	h.writeResource("lark-doc", "SKILL.md", content, true)
	_, err := h.reader.Read(h.context(), SkillReadRequest{AgentRunID: 1, Skill: "lark-doc", Reference: "scripts/doc_word_stat.py"})
	require.Error(t, err)
	require.Empty(t, h.invocations(), "non-reference namespaces must fail before CLI start")
}

func TestDeclaredSkillReferences_BoundsCountAndTotalBytes(t *testing.T) {
	t.Parallel()

	var content strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&content, "[ref](references/%03d-%s.md)\n", i, strings.Repeat("x", 200))
	}

	references := declaredSkillReferences(content.String())
	require.LessOrEqual(t, len(references), skillReaderMaxReferences)
	totalBytes := 0
	for _, reference := range references {
		totalBytes += len(reference)
	}
	require.LessOrEqual(t, totalBytes, skillReaderMaxReferenceBytes)
}

func TestSkillReader_UndeclaredReferenceAndResponseMismatchFailClosed(t *testing.T) {
	t.Parallel()

	h := newSkillReaderHarness(t, skillReaderOptions{})
	h.writeResource("lark-doc", "SKILL.md", "# Doc\n[Allowed](references/allowed.md)", true)
	h.writeResource("lark-doc", "references/allowed.md", "allowed", false)

	_, err := h.reader.Read(h.context(), SkillReadRequest{AgentRunID: 1, Skill: "lark-doc", Reference: "references/other.md"})
	require.Error(t, err)
	require.Len(t, h.invocations(), 1, "undeclared reference must not invoke the reference command")

	h.resetInvocations()
	h.writeRawResource("lark-doc", "SKILL.md", `{"skill":"lark-base","path":"SKILL.md","content":"# Doc","guidance":"g"}`)
	_, err = h.reader.Read(h.context(), SkillReadRequest{AgentRunID: 1, Skill: "lark-doc"})
	require.Error(t, err)

	h.writeRawResource("lark-doc", "SKILL.md", `{"skill":"lark-doc","path":"references/wrong.md","content":"# Doc","guidance":"g"}`)
	_, err = h.reader.Read(h.context(), SkillReadRequest{AgentRunID: 1, Skill: "lark-doc"})
	require.Error(t, err)

	h.writeResource("lark-doc", "SKILL.md", "[Allowed](references/allowed.md)", true)
	h.writeRawResource("lark-doc", "references/allowed.md", `{"skill":"lark-doc","path":"references/other.md","content":"allowed"}`)
	_, err = h.reader.Read(h.context(), SkillReadRequest{AgentRunID: 1, Skill: "lark-doc", Reference: "references/allowed.md"})
	require.Error(t, err)
}

func TestSkillReader_PaginationUTF8DriftAndReceiptBinding(t *testing.T) {
	t.Parallel()

	h := newSkillReaderHarness(t, skillReaderOptions{pageBytes: 17})
	content := "标题-你好吗-" + strings.Repeat("abcdef", 12)
	h.writeResource("lark-doc", "SKILL.md", content, true)

	request := SkillReadRequest{AgentRunID: 88, Skill: "lark-doc"}
	var assembled strings.Builder
	for pageNumber := 0; ; pageNumber++ {
		page, err := h.reader.Read(h.context(), request)
		require.NoError(t, err)
		require.True(t, utf8.ValidString(page.Content))
		require.LessOrEqual(t, len([]byte(page.Content)), 17)
		assembled.WriteString(page.Content)
		if page.Cursor == "" {
			require.NotEmpty(t, page.Receipt)
			require.NoError(t, h.reader.Verify(page.Receipt, 88, "lark-doc", LarkCLIVersion))
			require.Error(t, h.reader.Verify(page.Receipt, 89, "lark-doc", LarkCLIVersion))
			require.Error(t, h.reader.Verify(page.Receipt, 88, "lark-base", LarkCLIVersion))
			require.Error(t, h.reader.Verify(page.Receipt, 88, "lark-doc", "1.0.69"))
			tampered := tamperSkillToken(page.Receipt)
			require.Error(t, h.reader.Verify(tampered, 88, "lark-doc", LarkCLIVersion))
			break
		}
		require.Empty(t, page.Receipt, "intermediate main pages cannot mint a receipt")
		request.Cursor = page.Cursor
		require.Less(t, pageNumber, 100)
	}
	require.Equal(t, content, assembled.String())

	first, err := h.reader.Read(h.context(), SkillReadRequest{AgentRunID: 90, Skill: "lark-doc"})
	require.NoError(t, err)
	require.NotEmpty(t, first.Cursor)
	h.writeResource("lark-doc", "SKILL.md", content+" drift", true)
	_, err = h.reader.Read(h.context(), SkillReadRequest{AgentRunID: 90, Skill: "lark-doc", Cursor: first.Cursor})
	require.Error(t, err, "cursor digest must prevent cross-version page splicing")

	before := len(h.invocations())
	_, err = h.reader.Read(h.context(), SkillReadRequest{AgentRunID: 91, Skill: "lark-doc", Cursor: first.Cursor})
	require.Error(t, err)
	require.Len(t, h.invocations(), before, "cursor bound to another run must fail before CLI start")
	_, err = h.reader.Read(h.context(), SkillReadRequest{AgentRunID: 90, Skill: "lark-base", Cursor: first.Cursor})
	require.Error(t, err)
	require.Len(t, h.invocations(), before, "cursor bound to another skill must fail before CLI start")
	_, err = h.reader.Read(h.context(), SkillReadRequest{AgentRunID: 90, Skill: "lark-doc", Reference: "references/fetch.md", Cursor: first.Cursor})
	require.Error(t, err)
	require.Len(t, h.invocations(), before+1, "reference resolution may read only the fixed main skill before rejection")
	require.Equal(t, "HOME=unset|4|skills|read|lark-doc|--json", h.invocations()[before])
	before = len(h.invocations())
	tamperedCursor := tamperSkillToken(first.Cursor)
	_, err = h.reader.Read(h.context(), SkillReadRequest{AgentRunID: 90, Skill: "lark-doc", Cursor: tamperedCursor})
	require.Error(t, err)
	require.Len(t, h.invocations(), before, "tampered cursor must fail before CLI start")
}

func TestSkillReader_CursorRejectsExpiredOutOfBoundsAndNonUTF8Offsets(t *testing.T) {
	t.Parallel()

	h := newSkillReaderHarness(t, skillReaderOptions{pageBytes: 8})
	content := "你abc-defghijklmnop"
	h.writeResource("lark-doc", "SKILL.md", content, true)
	digestRaw := sha256.Sum256([]byte(content))
	digest := base64.RawURLEncoding.EncodeToString(digestRaw[:])
	makeCursor := func(offset int64) string {
		cursor, err := h.reader.encodeToken(skillTokenPayload{
			Kind:      skillCursorKind,
			Version:   skillReaderTokenVersion,
			RunID:     10,
			Skill:     "lark-doc",
			Offset:    offset,
			Digest:    digest,
			ExpiresAt: h.reader.now().Add(5 * time.Minute).Unix(),
		})
		require.NoError(t, err)
		return cursor
	}
	for _, offset := range []int64{1, 2, int64(len([]byte(content))), int64(len([]byte(content)) + 1)} {
		_, err := h.reader.Read(h.context(), SkillReadRequest{AgentRunID: 10, Skill: "lark-doc", Cursor: makeCursor(offset)})
		require.Error(t, err, "offset=%d", offset)
	}

	first, err := h.reader.Read(h.context(), SkillReadRequest{AgentRunID: 12, Skill: "lark-doc"})
	require.NoError(t, err)
	require.NotEmpty(t, first.Cursor)
	h.advance(16 * time.Minute)
	before := len(h.invocations())
	_, err = h.reader.Read(h.context(), SkillReadRequest{AgentRunID: 12, Skill: "lark-doc", Cursor: first.Cursor})
	require.Error(t, err)
	require.Len(t, h.invocations(), before, "expired cursor must fail before CLI start")
}

func TestSkillReader_ReceiptProvesMainDeliveryButReferencesNeverMintReceipt(t *testing.T) {
	t.Parallel()

	h := newSkillReaderHarness(t, skillReaderOptions{pageBytes: 16})
	h.writeResource("lark-doc", "SKILL.md", "[Long](references/long.md)", true)
	h.writeResource("lark-doc", "references/long.md", strings.Repeat("reference-", 10), false)

	request := SkillReadRequest{AgentRunID: 5, Skill: "lark-doc", Reference: "references/long.md"}
	pages := 0
	for {
		page, err := h.reader.Read(h.context(), request)
		require.NoError(t, err)
		require.Empty(t, page.Receipt)
		pages++
		if page.Cursor == "" {
			break
		}
		request.Cursor = page.Cursor
	}
	require.Greater(t, pages, 1)
}

func TestSkillReceipt_ExpiryDomainRequirementsAndDuplicates(t *testing.T) {
	t.Parallel()

	h := newSkillReaderHarness(t, skillReaderOptions{})
	for _, skill := range []string{"lark-shared", "lark-doc", "lark-base", "lark-wiki"} {
		h.writeResource(skill, "SKILL.md", "# "+skill, true)
	}
	receipts := make(map[string]string)
	for _, skill := range []string{"lark-shared", "lark-doc", "lark-base", "lark-wiki"} {
		page, err := h.reader.Read(h.context(), SkillReadRequest{AgentRunID: 77, Skill: skill})
		require.NoError(t, err)
		receipts[skill] = page.Receipt
	}

	require.NoError(t, h.reader.VerifyRequired([]string{receipts["lark-shared"], receipts["lark-doc"]}, 77, SkillDomainDocs))
	require.NoError(t, h.reader.VerifyRequired([]string{receipts["lark-shared"], receipts["lark-base"]}, 77, SkillDomainBase))
	require.NoError(t, h.reader.VerifyRequired([]string{receipts["lark-shared"], receipts["lark-wiki"]}, 77, SkillDomainWiki))
	require.NoError(t, h.reader.VerifyRequired([]string{receipts["lark-shared"], receipts["lark-wiki"], receipts["lark-doc"]}, 77, SkillDomainWikiContent))
	require.Error(t, h.reader.VerifyRequired(nil, 77, "unknown"))
	require.Error(t, h.reader.VerifyRequired([]string{receipts["lark-shared"]}, 77, SkillDomainDocs))
	require.Error(t, h.reader.VerifyRequired([]string{receipts["lark-shared"], receipts["lark-shared"]}, 77, SkillDomainDocs))
	require.Error(t, h.reader.VerifyRequired([]string{receipts["lark-shared"], "malformed"}, 77, SkillDomainDocs))
	require.Error(t, h.reader.VerifyRequired([]string{receipts["lark-shared"], receipts["lark-doc"], "malformed"}, 77, SkillDomainDocs))
	require.Error(t, h.reader.VerifyRequired([]string{receipts["lark-shared"], receipts["lark-base"]}, 77, SkillDomainDocs))

	for _, invalidWikiContent := range [][]string{
		{receipts["lark-shared"], receipts["lark-wiki"]},
		{receipts["lark-shared"], receipts["lark-doc"]},
		{receipts["lark-wiki"], receipts["lark-doc"]},
		{receipts["lark-shared"], receipts["lark-wiki"], receipts["lark-wiki"]},
		{receipts["lark-shared"], receipts["lark-wiki"], receipts["lark-doc"], receipts["lark-base"]},
	} {
		require.Error(t, h.reader.VerifyRequired(invalidWikiContent, 77, SkillDomainWikiContent))
	}
	otherRun, err := h.reader.Read(h.context(), SkillReadRequest{AgentRunID: 78, Skill: "lark-doc"})
	require.NoError(t, err)
	require.Error(t, h.reader.VerifyRequired([]string{receipts["lark-shared"], receipts["lark-wiki"], otherRun.Receipt}, 77, SkillDomainWikiContent))

	h.advance(16 * time.Minute)
	require.Error(t, h.reader.Verify(receipts["lark-doc"], 77, "lark-doc", LarkCLIVersion))
}

// Customer regression (Dev run 211): title-only discovery needs the official
// Drive skill and an exact same-run shared+drive receipt pair.
func TestSkillReader_DriveDiscoverySkillAndReceiptDomain(t *testing.T) {
	t.Parallel()

	h := newSkillReaderHarness(t, skillReaderOptions{})
	for _, skill := range []string{"lark-shared", "lark-drive"} {
		h.writeResource(skill, "SKILL.md", "# "+skill, true)
	}
	shared, err := h.reader.Read(h.context(), SkillReadRequest{AgentRunID: 211, Skill: "lark-shared"})
	require.NoError(t, err)
	drive, err := h.reader.Read(h.context(), SkillReadRequest{AgentRunID: 211, Skill: "lark-drive"})
	require.NoError(t, err)
	require.NoError(t, h.reader.VerifyRequired([]string{shared.Receipt, drive.Receipt}, 211, "drive"))
	require.Error(t, h.reader.VerifyRequired([]string{shared.Receipt}, 211, "drive"))
	require.Error(t, h.reader.VerifyRequired([]string{shared.Receipt, drive.Receipt, drive.Receipt}, 211, "drive"))
}

func TestSkillReceipt_KeyDerivationAndTokenShapeFailClosed(t *testing.T) {
	t.Parallel()

	h := newSkillReaderHarness(t, skillReaderOptions{})
	digest := sha256.Sum256([]byte("content"))
	payload := skillTokenPayload{
		Kind:       skillReceiptKind,
		Version:    skillReaderTokenVersion,
		RunID:      7,
		Skill:      "lark-doc",
		Digest:     base64.RawURLEncoding.EncodeToString(digest[:]),
		CLIVersion: LarkCLIVersion,
		ExpiresAt:  h.reader.now().Add(5 * time.Minute).Unix(),
	}
	payloadRaw, err := json.Marshal(payload)
	require.NoError(t, err)
	wrongSignature := signWithRawKeyForTest(h.rawKey, payloadRaw)
	wrongToken := base64.RawURLEncoding.EncodeToString(payloadRaw) + "." + base64.RawURLEncoding.EncodeToString(wrongSignature)
	require.Error(t, h.reader.Verify(wrongToken, 7, "lark-doc", LarkCLIVersion), "the raw third-party key must not sign receipts directly")

	for _, mutate := range []func(*skillTokenPayload){
		func(p *skillTokenPayload) { p.Kind = skillCursorKind },
		func(p *skillTokenPayload) { p.Version++ },
		func(p *skillTokenPayload) { p.Reference = "references/x.md" },
		func(p *skillTokenPayload) { p.Offset = 1 },
		func(p *skillTokenPayload) { p.Digest = "bad" },
	} {
		changed := payload
		mutate(&changed)
		token, signErr := h.reader.encodeToken(changed)
		if signErr != nil {
			continue
		}
		require.Error(t, h.reader.Verify(token, 7, "lark-doc", LarkCLIVersion))
	}
}

func TestSkillReader_TokensRequireCanonicalRawURLWithoutCRLF(t *testing.T) {
	t.Parallel()

	h := newSkillReaderHarness(t, skillReaderOptions{pageBytes: 8})
	h.writeResource("lark-doc", "SKILL.md", strings.Repeat("content-", 8), true)
	first, err := h.reader.Read(h.context(), SkillReadRequest{AgentRunID: 7, Skill: "lark-doc"})
	require.NoError(t, err)
	require.NotEmpty(t, first.Cursor)

	aliasCursor := aliasRawURLTokenForTest(t, first.Cursor)
	before := len(h.invocations())
	_, err = h.reader.Read(h.context(), SkillReadRequest{AgentRunID: 7, Skill: "lark-doc", Cursor: aliasCursor})
	require.Error(t, err)
	require.Len(t, h.invocations(), before, "non-canonical cursor must not start CLI")

	parts := strings.Split(first.Cursor, ".")
	for _, invalid := range []string{
		parts[0] + ".\n" + parts[1],
		parts[0] + ".\r" + parts[1],
		"." + parts[1],
		parts[0] + ".",
	} {
		_, err = h.reader.Read(h.context(), SkillReadRequest{AgentRunID: 7, Skill: "lark-doc", Cursor: invalid})
		require.Error(t, err)
		require.Len(t, h.invocations(), before)
	}

	h.writeResource("lark-doc", "SKILL.md", "short", true)
	page, err := h.reader.Read(h.context(), SkillReadRequest{AgentRunID: 8, Skill: "lark-doc"})
	require.NoError(t, err)
	require.NotEmpty(t, page.Receipt)
	require.Error(t, h.reader.Verify(aliasRawURLTokenForTest(t, page.Receipt), 8, "lark-doc", LarkCLIVersion))
	receiptParts := strings.Split(page.Receipt, ".")
	require.Error(t, h.reader.Verify(receiptParts[0]+".\n"+receiptParts[1], 8, "lark-doc", LarkCLIVersion))
	require.Error(t, h.reader.Verify(receiptParts[0]+".\r"+receiptParts[1], 8, "lark-doc", LarkCLIVersion))
}

func TestSkillReader_KeyAndTTLBoundaries(t *testing.T) {
	t.Parallel()

	validKey := base64.StdEncoding.EncodeToString(bytesOf(0x22, 32))
	for _, key := range []string{"", "not-base64", base64.StdEncoding.EncodeToString(bytesOf(1, 31))} {
		_, err := newSkillReaderWithOptions(key, skillReaderOptions{})
		require.Error(t, err)
	}
	for _, ttl := range []time.Duration{SkillReaderMinTTL - time.Nanosecond, SkillReaderMaxTTL + time.Nanosecond} {
		_, err := newSkillReaderWithOptions(validKey, skillReaderOptions{ttl: ttl})
		require.Error(t, err)
	}
	for _, ttl := range []time.Duration{SkillReaderMinTTL, SkillReaderMaxTTL} {
		reader, err := newSkillReaderWithOptions(validKey, skillReaderOptions{ttl: ttl})
		require.NoError(t, err)
		require.Equal(t, ttl, reader.ttl)
	}
	for _, size := range []int{utf8.UTFMax - 1, SkillReaderPageBytes + 1} {
		_, err := newSkillReaderWithOptions(validKey, skillReaderOptions{pageBytes: size})
		require.Error(t, err)
	}
}

func TestSkillReader_StrictOutputAndProcessFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
	}{
		{name: "unknown field", raw: `{"skill":"lark-doc","path":"SKILL.md","content":"x","guidance":"g","extra":true}`},
		{name: "trailing json", raw: `{"skill":"lark-doc","path":"SKILL.md","content":"x","guidance":"g"}{}`},
		{name: "missing content", raw: `{"skill":"lark-doc","path":"SKILL.md","guidance":"g"}`},
		{name: "missing main guidance", raw: `{"skill":"lark-doc","path":"SKILL.md","content":"x"}`},
		{name: "empty main guidance", raw: `{"skill":"lark-doc","path":"SKILL.md","content":"x","guidance":""}`},
		{name: "whitespace main guidance", raw: `{"skill":"lark-doc","path":"SKILL.md","content":"x","guidance":"  "}`},
		{name: "empty main content", raw: `{"skill":"lark-doc","path":"SKILL.md","content":"","guidance":"g"}`},
		{name: "whitespace main content", raw: `{"skill":"lark-doc","path":"SKILL.md","content":" \n\t","guidance":"g"}`},
		{name: "array", raw: `[]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := newSkillReaderHarness(t, skillReaderOptions{})
			h.writeRawResource("lark-doc", "SKILL.md", tt.raw)
			_, err := h.reader.Read(h.context(), SkillReadRequest{AgentRunID: 1, Skill: "lark-doc"})
			require.Error(t, err)
			require.NotContains(t, err.Error(), "content")
		})
	}

	t.Run("reader output ceiling", func(t *testing.T) {
		t.Parallel()
		h := newSkillReaderHarness(t, skillReaderOptions{maxOutputBytes: 256})
		h.writeResource("lark-doc", "SKILL.md", strings.Repeat("x", 512), true)
		_, err := h.reader.Read(h.context(), SkillReadRequest{AgentRunID: 1, Skill: "lark-doc"})
		require.Error(t, err)
	})

	t.Run("empty reference content", func(t *testing.T) {
		t.Parallel()
		h := newSkillReaderHarness(t, skillReaderOptions{})
		h.writeResource("lark-doc", "SKILL.md", "[Empty](references/empty.md)", true)
		for _, content := range []string{"", " \n\t"} {
			h.writeResource("lark-doc", "references/empty.md", content, false)
			_, err := h.reader.Read(h.context(), SkillReadRequest{AgentRunID: 1, Skill: "lark-doc", Reference: "references/empty.md"})
			require.Error(t, err)
		}
	})

	t.Run("exit nonzero", func(t *testing.T) {
		t.Parallel()
		h := newSkillReaderHarness(t, skillReaderOptions{})
		h.writeResource("lark-doc", "SKILL.md", "secret body", true)
		require.NoError(t, os.WriteFile(filepath.Join(h.root, "exit-nonzero"), []byte("1"), 0o600))
		_, err := h.reader.Read(h.context(), SkillReadRequest{AgentRunID: 1, Skill: "lark-doc"})
		require.Error(t, err)
		require.NotContains(t, err.Error(), "secret body")
	})
}

func TestSkillReader_ConcurrentReadAndVerify(t *testing.T) {
	t.Parallel()

	h := newSkillReaderHarness(t, skillReaderOptions{})
	h.writeResource("lark-doc", "SKILL.md", "# stable", true)
	const workers = 32
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			page, err := h.reader.Read(h.context(), SkillReadRequest{AgentRunID: 42, Skill: "lark-doc"})
			if err == nil {
				err = h.reader.Verify(page.Receipt, 42, "lark-doc", LarkCLIVersion)
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
}

func TestSkillReader_ProcessLimitIsSharedAndWaitingCancellationDoesNotStartCLI(t *testing.T) {
	h := newSkillReaderHarness(t, skillReaderOptions{})
	h.writeResource("lark-doc", "SKILL.md", "# stable", true)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	started := make(chan struct{}, SkillReaderMaxConcurrentProcesses*2)
	var wg sync.WaitGroup
	var cancelWaiting context.CancelFunc
	defer func() {
		if cancelWaiting != nil {
			cancelWaiting()
		}
		releaseAll()
		wg.Wait()
		if slots := len(skillProcessSlots); slots != 0 {
			t.Errorf("shared skill process slots leaked after cleanup: %d", slots)
		}
	}()
	if slots := len(skillProcessSlots); slots != 0 {
		t.Fatalf("shared skill process slots dirty at test start: %d", slots)
	}

	var current atomic.Int64
	var maximum atomic.Int64
	onStarted := func() {
		value := current.Add(1)
		for {
			old := maximum.Load()
			if value <= old || maximum.CompareAndSwap(old, value) {
				break
			}
		}
		started <- struct{}{}
		<-release
	}
	onFinished := func() { current.Add(-1) }
	options := skillReaderOptions{
		binary:          filepath.Join(h.root, "lark-cli"),
		now:             h.reader.now,
		processStarted:  onStarted,
		processFinished: onFinished,
	}
	key := base64.StdEncoding.EncodeToString(h.rawKey)
	firstReader, err := newSkillReaderWithOptions(key, options)
	require.NoError(t, err)
	secondReader, err := newSkillReaderWithOptions(key, options)
	require.NoError(t, err)
	readers := []*SkillReader{firstReader, secondReader}

	errs := make(chan error, SkillReaderMaxConcurrentProcesses*2)
	startReads := func(count int) {
		for index := 0; index < count; index++ {
			wg.Add(1)
			reader := readers[index%len(readers)]
			go func() {
				defer wg.Done()
				_, readErr := reader.Read(context.Background(), SkillReadRequest{AgentRunID: 9, Skill: "lark-doc"})
				errs <- readErr
			}()
		}
	}
	startReads(SkillReaderMaxConcurrentProcesses)
	for index := 0; index < SkillReaderMaxConcurrentProcesses; index++ {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out filling shared process slots")
		}
	}
	require.Empty(t, h.invocations(), "probe blocks before any CLI process starts")

	waitingCtx, cancel := context.WithCancel(context.Background())
	cancelWaiting = cancel
	waitingDone := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, waitErr := secondReader.Read(waitingCtx, SkillReadRequest{AgentRunID: 10, Skill: "lark-doc"})
		waitingDone <- waitErr
	}()
	select {
	case <-started:
		t.Fatal("a request exceeded the shared process limit")
	case <-time.After(100 * time.Millisecond):
	}
	cancelWaiting()
	cancelWaiting = nil
	select {
	case waitErr := <-waitingDone:
		require.Error(t, waitErr)
	case <-time.After(5 * time.Second):
		t.Fatal("canceled semaphore waiter did not return")
	}
	require.Empty(t, h.invocations(), "canceled waiter must not start an extra CLI")

	startReads(SkillReaderMaxConcurrentProcesses)
	releaseAll()
	wg.Wait()
	close(errs)
	for readErr := range errs {
		require.NoError(t, readErr)
	}
	require.LessOrEqual(t, maximum.Load(), int64(SkillReaderMaxConcurrentProcesses))
	require.Zero(t, current.Load())
	require.Zero(t, len(skillProcessSlots))
	require.Len(t, h.invocations(), SkillReaderMaxConcurrentProcesses*2)
}

type skillReaderHarness struct {
	t           *testing.T
	root        string
	reader      *SkillReader
	rawKey      []byte
	nowUnixNano atomic.Int64
}

func newSkillReaderHarness(t *testing.T, options skillReaderOptions) *skillReaderHarness {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "resources"), 0o700))
	logPath := filepath.Join(root, "invocations.log")
	scriptPath := filepath.Join(root, "lark-cli")
	script := fmt.Sprintf(`#!/bin/sh
home=unset
[ "${HOME+x}" = x ] && home=set
line="HOME=$home|$#"
for arg in "$@"; do line="$line|$arg"; done
printf '%%s\n' "$line" >> %q
[ -f %q ] && exit 7
skill="$3"
if [ "$4" = "--json" ]; then resource="SKILL.md"; else resource="$4"; fi
/bin/cat %q/"$skill"/"$resource".json
`, logPath, filepath.Join(root, "exit-nonzero"), filepath.Join(root, "resources"))
	require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0o700))

	rawKey := bytesOf(0x5a, 32)
	keyB64 := base64.StdEncoding.EncodeToString(rawKey)
	h := &skillReaderHarness{t: t, root: root, rawKey: rawKey}
	h.nowUnixNano.Store(time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC).UnixNano())
	options.binary = scriptPath
	options.now = func() time.Time { return time.Unix(0, h.nowUnixNano.Load()).UTC() }
	reader, err := newSkillReaderWithOptions(keyB64, options)
	require.NoError(t, err)
	h.reader = reader
	return h
}

func (h *skillReaderHarness) context() context.Context {
	return context.Background()
}

func (h *skillReaderHarness) writeResource(skill, resource, content string, guidance bool) {
	h.t.Helper()
	value := map[string]any{"skill": skill, "path": resource, "content": content}
	if guidance {
		value["guidance"] = "read the complete skill"
	}
	raw, err := json.Marshal(value)
	require.NoError(h.t, err)
	h.writeRawResource(skill, resource, string(raw))
}

func (h *skillReaderHarness) writeRawResource(skill, resource, raw string) {
	h.t.Helper()
	path := filepath.Join(h.root, "resources", skill, filepath.FromSlash(resource)+".json")
	require.NoError(h.t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(h.t, os.WriteFile(path, []byte(raw), 0o600))
}

func (h *skillReaderHarness) invocations() []string {
	h.t.Helper()
	raw, err := os.ReadFile(filepath.Join(h.root, "invocations.log"))
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(h.t, err)
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

func (h *skillReaderHarness) resetInvocations() {
	h.t.Helper()
	require.NoError(h.t, os.Remove(filepath.Join(h.root, "invocations.log")))
}

func (h *skillReaderHarness) advance(duration time.Duration) {
	h.nowUnixNano.Add(int64(duration))
}

func bytesOf(value byte, count int) []byte {
	result := make([]byte, count)
	for index := range result {
		result[index] = value
	}
	return result
}

func signWithRawKeyForTest(rawKey []byte, payload []byte) []byte {
	mac := hmac.New(sha256.New, rawKey)
	_, _ = mac.Write(payload)
	return mac.Sum(nil)
}

func tamperSkillToken(token string) string {
	replacement := byte('A')
	if token[len(token)-1] == replacement {
		replacement = 'B'
	}
	return token[:len(token)-1] + string(replacement)
}

func aliasRawURLTokenForTest(t *testing.T, token string) string {
	t.Helper()
	payloadPart, signaturePart, found := strings.Cut(token, ".")
	require.True(t, found)
	want, err := base64.RawURLEncoding.DecodeString(signaturePart)
	require.NoError(t, err)
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	for index := 0; index < len(alphabet); index++ {
		candidate := signaturePart[:len(signaturePart)-1] + string(alphabet[index])
		if candidate == signaturePart {
			continue
		}
		decoded, decodeErr := base64.RawURLEncoding.DecodeString(candidate)
		if decodeErr == nil && hmac.Equal(decoded, want) {
			return payloadPart + "." + candidate
		}
	}
	t.Fatal("signature had no non-canonical RawURL alias")
	return ""
}
