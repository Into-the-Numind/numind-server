package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"numind-server/internal/pkg/log"
)

// SkillEntry is a single record in the Registry.
// It bundles the parsed manifest with the host-side directory paths
// needed by invoke_skill to access SKILL.md and skill files.
type SkillEntry struct {
	// Manifest is the parsed manifest.json for this skill.
	Manifest SkillManifest

	// SkillMDPath is the absolute host-side path to the SKILL.md file
	// (e.g. /opt/numind/skills/xlsx-author/SKILL.md).
	// invoke_skill reads this file via docker exec cat to inject into the
	// code-gen prompt. The path may not exist at registration time (WARN logged).
	SkillMDPath string

	// RootDir is the absolute host-side path to the skill directory
	// (e.g. /opt/numind/skills/xlsx-author/).
	RootDir string
}

// Registry manages the set of registered skills discovered from the skills_root
// directory. It is safe for concurrent use; reads are lock-free (via RWMutex).
type Registry interface {
	// Get returns the SkillEntry for the given skillName.
	// Returns (nil, ErrSkillNotFound) when the skill is not registered.
	Get(skillName string) (*SkillEntry, error)

	// List returns the manifest for every registered skill.
	// The slice is a snapshot; it is safe to iterate without holding a lock.
	List() []SkillManifest

	// Reload re-scans the skills_root directory and refreshes the registry.
	// Skills currently being executed hold their own *SkillEntry reference;
	// those references remain valid after Reload (the map is replaced atomically).
	// Returns an error only when the skills_root directory itself cannot be read
	// (individual malformed manifests are silently skipped with a WARN log).
	Reload() error
}

// fileRegistry is the production Registry implementation backed by a local
// directory scan. It is constructed by NewRegistry and is safe for concurrent use.
type fileRegistry struct {
	skillsRoot string
	mu         sync.RWMutex
	entries    map[string]*SkillEntry // keyed by skill name
}

// Compile-time assertion.
var _ Registry = (*fileRegistry)(nil)

// NewRegistry scans skillsRoot and returns a populated Registry.
//
// Scanning rules:
//   - Each immediate subdirectory of skillsRoot is a potential skill.
//   - The subdirectory must contain a manifest.json that is valid JSON and passes
//     SkillManifest.Validate(). Invalid manifests are skipped with a WARN log.
//   - SKILL.md absence is logged as WARN but does not prevent registration;
//     invoke_skill degrades to manifest.description in that case.
//
// Returns an error when skillsRoot itself cannot be read (e.g. the directory does
// not exist). The caller should log the error and skip registering invoke_skill.
func NewRegistry(skillsRoot string) (Registry, error) {
	r := &fileRegistry{
		skillsRoot: skillsRoot,
		entries:    make(map[string]*SkillEntry),
	}
	if err := r.scan(); err != nil {
		return nil, err
	}
	return r, nil
}

// Get implements Registry.Get.
func (r *fileRegistry) Get(skillName string) (*SkillEntry, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.entries[skillName]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrSkillNotFound, skillName)
	}
	return entry, nil
}

// List implements Registry.List.
func (r *fileRegistry) List() []SkillManifest {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]SkillManifest, 0, len(r.entries))
	for _, e := range r.entries {
		out = append(out, e.Manifest)
	}
	return out
}

// Reload implements Registry.Reload.
func (r *fileRegistry) Reload() error {
	return r.scan()
}

// scan reads the skills_root directory and rebuilds the internal entries map.
// Called by NewRegistry and Reload. Protected by a write lock while swapping.
func (r *fileRegistry) scan() error {
	dirs, err := os.ReadDir(r.skillsRoot)
	if err != nil {
		return fmt.Errorf("skills.Registry: ReadDir %q: %w", r.skillsRoot, err)
	}

	newEntries := make(map[string]*SkillEntry, len(dirs))
	for _, dir := range dirs {
		if !dir.IsDir() {
			continue
		}
		name := dir.Name()
		manifestPath := filepath.Join(r.skillsRoot, name, "manifest.json")

		data, readErr := os.ReadFile(manifestPath)
		if readErr != nil {
			// No manifest.json → skip silently (not a skill directory).
			log.Warnw("skills.Registry: skip dir without manifest.json",
				"skill", name, "error", readErr)
			continue
		}

		manifest, parseErr := parseManifest(data)
		if parseErr != nil {
			// Invalid JSON or failed validation → skip with WARN, not fatal.
			log.Warnw("skills.Registry: invalid manifest.json, skill skipped",
				"skill", name, "error", parseErr)
			continue
		}

		skillMDPath := filepath.Join(r.skillsRoot, name, "SKILL.md")
		if _, statErr := os.Stat(skillMDPath); os.IsNotExist(statErr) {
			log.Warnw("skills.Registry: SKILL.md missing — skill registered but LLM prompt will degrade to manifest.description",
				"skill", name, "skill_md_path", skillMDPath)
		}

		newEntries[name] = &SkillEntry{
			Manifest:    *manifest,
			SkillMDPath: skillMDPath,
			RootDir:     filepath.Join(r.skillsRoot, name),
		}
		log.Infow("skills.Registry: registered skill", "skill", name, "version", manifest.Version)
	}

	r.mu.Lock()
	r.entries = newEntries
	r.mu.Unlock()

	log.Infow("skills.Registry: scan complete", "count", len(newEntries))
	return nil
}

// registeredSkillNames returns a sorted, comma-separated list of registered skill names.
// Used in error messages so the LLM can pick a valid skill from the list.
func (r *fileRegistry) registeredSkillNames() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.entries) == 0 {
		return "(none)"
	}
	names := make([]string, 0, len(r.entries))
	for k := range r.entries {
		names = append(names, k)
	}
	// Stable sort for deterministic output.
	sortStrings(names)
	result := ""
	for i, n := range names {
		if i > 0 {
			result += ", "
		}
		result += n
	}
	return result
}

// SkillNames returns all registered skill names (sorted).
// Exported so invoke_skill can list available skills in error messages.
func SkillNames(r Registry) string {
	if fr, ok := r.(*fileRegistry); ok {
		return fr.registeredSkillNames()
	}
	manifests := r.List()
	names := make([]string, 0, len(manifests))
	for _, m := range manifests {
		names = append(names, m.Name)
	}
	sortStrings(names)
	result := ""
	for i, n := range names {
		if i > 0 {
			result += ", "
		}
		result += n
	}
	if result == "" {
		return "(none)"
	}
	return result
}

// sortStrings sorts a string slice in ascending order without importing "sort"
// to keep the dependency minimal (insertion sort, fine for small N).
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		key := s[i]
		j := i - 1
		for j >= 0 && s[j] > key {
			s[j+1] = s[j]
			j--
		}
		s[j+1] = key
	}
}
