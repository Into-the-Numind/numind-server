// Package agentmd implements the 2-level AGENT.md cascade loader (V1.5).
//
// AGENT.md is the developer-written rules layer (modelled after ClaudeCode's
// CLAUDE.md cascade). It is loaded once at agent_run startup and injected
// into the system prompt's Memories section (segment 3) as a static block
// that precedes the dynamically retrieved user facts.
//
// V1.5 scope (D6 decision, locked by product owner):
//
//   - Level 1 (deployment): /etc/numind/AGENT.md — ops/company-wide rules
//   - Level 2 (user-global): <user_data_dir>/users/<user_id>/AGENT.md — per-user
//
// V2 extension hook (NOT implemented in V1.5):
//
//   - Level 3 workspace: <workspaceDir>/.numind/AGENT.md
//   - Level 4 project-root: <workspaceDir>/AGENT.md
//   - Level 5 rules dir: <workspaceDir>/.numind/rules/*.md (alphabetical)
//   - Level 6 local override: <workspaceDir>/AGENT.local.md
//
// When V2 lands, LoadAgentMd can grow a functional option WithWorkspace(dir)
// and buildCandidates can append candidates 3-6 when workspaceDir is non-empty.
// The current package surface (LoadResult, Source) is forward-compatible.
package agentmd

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/viper"

	"numind-server/internal/pkg/log"
)

// Source describes a single AGENT.md file successfully loaded into the cascade.
// Returned in LoadResult.Sources for debugging / observability.
type Source struct {
	Path  string // absolute path actually read
	Label string // injection label, e.g. "[Deployment-level]"
	Size  int    // character count of file content (post-truncation if applicable)
}

// LoadResult is the aggregate output of LoadAgentMd.
// Content is the joined-and-labelled text ready for direct injection into
// the system prompt's Memories section. It is "" when no file is found,
// the loader is disabled, or all candidates fail to load.
type LoadResult struct {
	Content    string   // concatenated text with section labels + "\n\n---\n\n" separators
	Sources    []Source // successfully loaded files, in cascade order (low → high priority)
	TotalChars int      // total characters across all sections (after per-file truncation)
	Truncated  bool     // true if any file exceeded MaxPerFileChars or total exceeded MaxTotalChars
}

// Config holds loader behaviour read from viper.
//
// Configuration key layout (config_*.yaml):
//
//	agent:
//	  memory:
//	    agent_md:
//	      enabled: true
//	      user_data_dir: "/data/numind/user_files"
//	      etc_dir: "/etc/numind"
//	      max_per_file_chars: 12288
//	      max_total_chars: 51200
type Config struct {
	Enabled         bool
	UserDataDir     string
	EtcDir          string
	MaxPerFileChars int
	MaxTotalChars   int
}

// Defaults used when a viper key is unset. Kept here (not as viper.SetDefault
// calls in the constructor) so unit tests can call GetConfig with a fresh
// viper instance and still observe deterministic behaviour.
const (
	defaultEnabled         = true
	defaultEtcDir          = "/etc/numind"
	defaultMaxPerFileChars = 12288 // 12 KB — OpenHarness-compatible
	defaultMaxTotalChars   = 51200 // 50 KB — fits I3 system prompt segment budget
)

// candidate is the internal representation of a candidate path before any
// disk I/O. buildCandidates emits a slice of these in cascade-order
// (lowest priority first → highest priority last).
type candidate struct {
	Path  string
	Label string
}

// GetConfig reads the AgentMdConfig from viper. Falls back to safe defaults
// when individual keys are unset so the loader works out-of-the-box in dev
// without explicit config.
//
// Note: user_data_dir has no default — when unset, the user-global path
// will resolve to "users/<id>/AGENT.md" (relative cwd) which os.Stat will
// rarely match. This is intentional: production deployments must set
// user_data_dir explicitly via config_*.yaml.
func GetConfig() Config {
	// Use IsSet to distinguish "unset" from "explicit false"
	enabled := defaultEnabled
	if viper.IsSet("agent.memory.agent_md.enabled") {
		enabled = viper.GetBool("agent.memory.agent_md.enabled")
	}

	etcDir := viper.GetString("agent.memory.agent_md.etc_dir")
	if etcDir == "" {
		etcDir = defaultEtcDir
	}

	maxPerFile := viper.GetInt("agent.memory.agent_md.max_per_file_chars")
	if maxPerFile <= 0 {
		maxPerFile = defaultMaxPerFileChars
	}

	maxTotal := viper.GetInt("agent.memory.agent_md.max_total_chars")
	if maxTotal <= 0 {
		maxTotal = defaultMaxTotalChars
	}

	return Config{
		Enabled:         enabled,
		UserDataDir:     viper.GetString("agent.memory.agent_md.user_data_dir"),
		EtcDir:          etcDir,
		MaxPerFileChars: maxPerFile,
		MaxTotalChars:   maxTotal,
	}
}

// LoadAgentMd loads the AGENT.md cascade for the given user and returns
// the concatenated content ready for system-prompt injection.
//
// Called once at agent_run startup (from AgentRunner.Run). Result should
// be cached on the run-scoped runtime so subsequent turns reuse it without
// re-reading the filesystem.
//
// Errors: never returns a hard error. Individual file read failures are
// logged at WARN level and the loader continues with the remaining
// candidates. A non-existent file is the most common case and is silent.
//
// userID == 0 (anonymous / system context) skips the user-global path
// and reads only the deployment-level file.
//
// V1.5 signature does NOT take a workspaceDir — workspace-scoped paths
// (levels 3-6 in the original ClaudeCode hierarchy) are deferred to V2.
// When V2 lands, this signature can grow an opts variadic without
// breaking existing callers.
func LoadAgentMd(ctx context.Context, userID uint) (*LoadResult, error) {
	cfg := GetConfig()
	if !cfg.Enabled {
		return &LoadResult{}, nil
	}

	candidates := buildCandidates(cfg, userID)

	var sections []string
	var sources []Source
	total := 0
	truncated := false

	for _, c := range candidates {
		content, ok := readIfExists(c.Path)
		if !ok {
			continue
		}

		// Normalize CRLF / CR → LF for consistent rendering across platforms.
		content = normalizeLineEndings(content)

		// Per-file size cap (in characters, not bytes — Go strings are UTF-8 byte
		// slices; we treat len() as character count for ASCII-dominant cases.
		// For mixed CJK content this slightly under-counts characters but
		// over-counts bytes, which is the conservative bias we want for prompts).
		if len(content) > cfg.MaxPerFileChars {
			content = content[:cfg.MaxPerFileChars] +
				"\n\n[truncated: original > " +
				strconv.Itoa(cfg.MaxPerFileChars) + " chars]"
			truncated = true
			log.Warnw("agent_md file truncated",
				"path", c.Path,
				"max_per_file_chars", cfg.MaxPerFileChars)
		}

		section := fmt.Sprintf("%s (%s)\n%s", c.Label, c.Path, content)

		// Total size cap — drop the current section (and any remaining) when
		// it would push us past MaxTotalChars. V1.5 with only 2 levels × 12KB
		// per-file cap = 24KB max, so MaxTotalChars=50KB is comfortable, but
		// the check is kept defensive for V2 expansion.
		if total+len(section) > cfg.MaxTotalChars {
			truncated = true
			log.Warnw("agent_md total cap reached, dropping remaining sections",
				"path", c.Path,
				"current_total_chars", total,
				"max_total_chars", cfg.MaxTotalChars)
			break
		}

		sections = append(sections, section)
		sources = append(sources, Source{
			Path:  c.Path,
			Label: c.Label,
			Size:  len(content),
		})
		total += len(section)
	}

	result := &LoadResult{
		Content:    strings.Join(sections, "\n\n---\n\n"),
		Sources:    sources,
		TotalChars: total,
		Truncated:  truncated,
	}

	log.Infow("agent_md loaded",
		"user_id", userID,
		"sources", len(sources),
		"total_chars", total,
		"truncated", truncated)

	return result, nil
}

// buildCandidates returns the 2 candidate paths in cascade order
// (deployment first → user-global last; later entries override earlier
// in the LLM's reading order).
//
// V2 extension hook: when workspaceDir is added as a parameter, append
// the following candidates after the user-global entry:
//
//   - <workspaceDir>/.numind/AGENT.md   (project-level)
//   - <workspaceDir>/AGENT.md           (project root)
//   - <workspaceDir>/.numind/rules/*.md (rules dir, alphabetical)
//   - <workspaceDir>/AGENT.local.md     (local override)
//
// Keep this function pure (no I/O) so tests can assert path generation
// independently of disk state.
func buildCandidates(cfg Config, userID uint) []candidate {
	out := []candidate{
		{
			Path:  filepath.Join(cfg.EtcDir, "AGENT.md"),
			Label: "[Deployment-level]",
		},
	}

	// userID==0 means anonymous / system context — skip per-user path.
	// D7 decision: B2B2C parent and child accounts are completely isolated;
	// each user_id has its own directory and never inherits from another.
	if userID > 0 && cfg.UserDataDir != "" {
		out = append(out, candidate{
			Path: filepath.Join(
				cfg.UserDataDir,
				"users",
				strconv.FormatUint(uint64(userID), 10),
				"AGENT.md",
			),
			Label: "[User-global]",
		})
	}

	return out
}

// readIfExists returns the file contents and true if the file exists and is
// readable, or "" and false otherwise. Logs WARN for permission errors;
// silently skips on os.IsNotExist (which is the dominant case — most users
// never write an AGENT.md).
func readIfExists(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err == nil {
		return string(data), true
	}
	// File-not-exist is the expected case for most paths; do not log it.
	if errors.Is(err, fs.ErrNotExist) {
		return "", false
	}
	// Permission denied, I/O error, etc. — log and skip without failing the run.
	log.Warnw("agent_md read failed; skipping candidate",
		"path", path,
		"error", err)
	return "", false
}

// normalizeLineEndings converts CRLF and lone CR sequences to LF.
// Keeps prompt rendering deterministic when users edit AGENT.md on Windows.
func normalizeLineEndings(s string) string {
	// Order matters: replace CRLF first, then any remaining CR (lone CR is rare
	// but classic Mac line endings exist in the wild).
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return s
}
