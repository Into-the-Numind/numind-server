// Package skills provides the Skill Registry for the invoke_skill tool framework.
// A Skill is a declarative, sandboxed code-generation unit: the LLM generates Python
// code guided by a SKILL.md and the skill's manifest.json, then the code runs inside
// an isolated Docker container to produce structured output files (xlsx/docx/pptx/pdf).
package skills

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ErrSkillNotFound is returned by Registry.Get when the requested skill name is not
// registered (either missing from skills_root or its manifest.json failed to parse).
var ErrSkillNotFound = errors.New("skill not found")

// SkillManifest is the Go representation of a skill's manifest.json file.
// Every skill directory under skills_root must contain a manifest.json that
// conforms to this schema.
//
// Example manifest.json:
//
//	{
//	  "name": "xlsx-author",
//	  "version": "1.0.0",
//	  "description": "Generate Excel spreadsheets with openpyxl",
//	  "categories": ["spreadsheet", "data"],
//	  "required_libs": ["openpyxl>=3.1", "pandas>=2.0"],
//	  "output_mime_types": ["application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"],
//	  "max_runtime_seconds": 180,
//	  "max_output_size_mb": 50,
//	  "input_dir": "/workdir/input/",
//	  "output_dir": "/workdir/output/"
//	}
type SkillManifest struct {
	// Name is the unique identifier for this skill (e.g. "xlsx-author").
	// Must match the directory name under skills_root.
	Name string `json:"name"`

	// Version is a semver-style string (e.g. "1.0.0").
	Version string `json:"version"`

	// Description is a human-readable summary of what this skill generates.
	// Used as fallback LLM context when SKILL.md cannot be read.
	Description string `json:"description"`

	// Categories groups skills by output type (e.g. ["spreadsheet", "data"]).
	Categories []string `json:"categories"`

	// RequiredLibs lists the Python library specifiers pre-installed in the
	// skill image (e.g. "openpyxl>=3.1"). The code-gen prompt includes these
	// so the LLM knows which imports are available.
	RequiredLibs []string `json:"required_libs"`

	// OutputMimeTypes lists the expected MIME types of output files.
	// Used by ScanOutput for MIME verification.
	OutputMimeTypes []string `json:"output_mime_types"`

	// MaxRuntimeSeconds is the sandbox timeout for this skill's Python execution.
	// Must be ≤ 180. Default is 180 when zero.
	MaxRuntimeSeconds int `json:"max_runtime_seconds"`

	// MaxOutputSizeMB is the size cap (in MiB) for a single output file.
	// Must be ≤ 100. Default is 50 when zero.
	MaxOutputSizeMB int `json:"max_output_size_mb"`

	// InputDir is the container path where input files are placed.
	// Defaults to "/workdir/input/" when empty.
	InputDir string `json:"input_dir,omitempty"`

	// OutputDir is the container path where the skill must write output files.
	// Defaults to "/workdir/output/" when empty.
	OutputDir string `json:"output_dir,omitempty"`
}

// Validate checks that the manifest has all required fields and respects the
// hard constraints defined by the platform. Returns a non-nil error describing
// the first violation found.
//
// Required fields: Name, Version, Description.
// Hard constraints: MaxRuntimeSeconds ≤ 180, MaxOutputSizeMB ≤ 100.
func (m *SkillManifest) Validate() error {
	if m.Name == "" {
		return errors.New("manifest: name is required")
	}
	if m.Version == "" {
		return errors.New("manifest: version is required")
	}
	if m.Description == "" {
		return errors.New("manifest: description is required")
	}
	if m.MaxRuntimeSeconds > 180 {
		return fmt.Errorf("manifest: max_runtime_seconds %d exceeds hard limit of 180", m.MaxRuntimeSeconds)
	}
	if m.MaxOutputSizeMB > 100 {
		return fmt.Errorf("manifest: max_output_size_mb %d exceeds hard limit of 100", m.MaxOutputSizeMB)
	}
	return nil
}

// EffectiveMaxRuntime returns the effective runtime limit (seconds) applying
// defaults when the manifest value is zero or negative.
func (m *SkillManifest) EffectiveMaxRuntime() int {
	if m.MaxRuntimeSeconds <= 0 {
		return 180
	}
	return m.MaxRuntimeSeconds
}

// EffectiveMaxOutputMB returns the effective output size limit applying
// defaults when the manifest value is zero or negative.
func (m *SkillManifest) EffectiveMaxOutputMB() int {
	if m.MaxOutputSizeMB <= 0 {
		return 50
	}
	return m.MaxOutputSizeMB
}

// EffectiveInputDir returns the container input directory path, defaulting to
// "/workdir/input/" when the manifest's InputDir is empty.
func (m *SkillManifest) EffectiveInputDir() string {
	if m.InputDir == "" {
		return "/workdir/input/"
	}
	return m.InputDir
}

// EffectiveOutputDir returns the container output directory path, defaulting to
// "/workdir/output/" when the manifest's OutputDir is empty.
func (m *SkillManifest) EffectiveOutputDir() string {
	if m.OutputDir == "" {
		return "/workdir/output/"
	}
	return m.OutputDir
}

// parseManifest unmarshals raw JSON bytes into a SkillManifest and validates it.
// Returns an error if the JSON is malformed or validation fails.
func parseManifest(data []byte) (*SkillManifest, error) {
	var m SkillManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("manifest: JSON parse error: %w", err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}
