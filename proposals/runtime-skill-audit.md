# Runtime Skill Audit — Frontend Design System (NDF S1 Round 2)

**Date**: 2026-04-10
**Auditor**: Sonnet subagent (independent, no S0 context)
**Purpose**: Determine if 6 runtime "impeccable" skills genuinely enforce DESIGN.md as source of truth, before user invests 2-3 hours building DESIGN.md content.
**Decision gate**: PASS → proceed to NDF S1 Round 3; PARTIAL → revise proposal with fallbacks; FAIL → Pause and Ask, possible feature pivot.

## Methodology

Read each skill's SKILL.md in full. Ran `grep -r "DESIGN\.md"` across all 6 skill directories. Checked file type (regular file vs symlink). Answered 6 standardized questions per skill with exact line numbers. Skeptical, not generous.

**Key structural discovery**: The 5 non-gstack skills (`normalize`, `audit`, `frontend-design`, `polish`, `harden`) are standalone regular files (authored 2026-04-02, sizes 4–10 KB). They belong to a separate "impeccable" skill family that uses `.impeccable.md` as its context file — NOT `DESIGN.md`. The `design-review` skill is a symlink to `~/.claude/skills/gstack/design-review/SKILL.md` (a much larger gstack skill, ~1500+ lines), which DOES read `DESIGN.md`. This structural split is the central finding of this audit.

---

## Per-Skill Audit Results

### 1. /normalize ⭐

**File**: `~/.claude/skills/normalize/SKILL.md` — regular file, 4,195 bytes, 70 lines.

**Q1 — Reads DESIGN.md?** NO. Grep for "DESIGN.md" returns zero matches. The string does not appear anywhere in the file.

**Q2 — Treats DESIGN.md as source of truth?** NO. The file never references `DESIGN.md`. Its context-gathering mechanism is: "Invoke `/frontend-design` — it contains design principles, anti-patterns, and the **Context Gathering Protocol**" (line 12). The Context Gathering Protocol in `/frontend-design` reads `.impeccable.md` (a project-specific context file), not `DESIGN.md`.

**Q3 — Has hardcoded design opinions?** YES, extensively. The Execute section (lines 44–58) has its own hardcoded rubric: 8 independent dimensions (Typography, Color & Theme, Spacing & Layout, Components, Motion & Interaction, Responsive Behavior, Accessibility, Progressive Disclosure) with explicit NEVER rules ("Never hard-code values that should use design tokens", "Never create new one-off components when design system equivalents exist"). These operate with zero reference to any project-specific file.

**Q4 — Modifies code or only advises?** Modifies code. The skill's framing is "Analyze and redesign the feature" and "Execute" with explicit code changes. Clean Up section (lines 63–69) includes lint, type-check, and removing orphaned code. No commit step mentioned.

**Q5 — Has ordering dependencies?** YES. Mandatory dependency: must invoke `/frontend-design` first (line 12), which must invoke `/teach-impeccable` if `.impeccable.md` doesn't exist. No artifact output that other skills consume.

**Q6 — Conflict resolution?** The skill has no mechanism to detect or resolve conflicts with a project `DESIGN.md` because it never reads one. If `DESIGN.md` says "use 4px spacing scale" and the code uses 8px, /normalize will apply its own judgment ("Use spacing tokens") without referencing `DESIGN.md` at all.

**Verdict: FAIL** — Zero DESIGN.md references. Uses `.impeccable.md` + hardcoded rubric as its design authority. A 2-3 hour investment in `DESIGN.md` content would be completely invisible to this skill.

---

### 2. /design-review ⭐

**File**: `~/.claude/skills/design-review/SKILL.md` — symlink → `~/.claude/skills/gstack/design-review/SKILL.md`, a full gstack skill.

**Q1 — Reads DESIGN.md?** YES. Line 509–511 of the gstack SKILL.md: `"Check for DESIGN.md: Look for DESIGN.md, design-system.md, or similar in the repo root. If found, read it — all design decisions must be calibrated against it."` This is an explicit read step in the setup phase, before any auditing begins.

**Q2 — Treats DESIGN.md as source of truth?** PARTIAL. The language at line 511 is strong: "all design decisions must be calibrated against it. Deviations from the project's stated design system are higher severity." However, Phase 2 (lines 864–891) explicitly states its goal is to extract the **rendered** design system ("not what a DESIGN.md says, but what's rendered"), treating the live site as the ground truth and offering to overwrite DESIGN.md from observations. This creates a potential authority conflict: DESIGN.md is set up as source of truth in Phase 1, but Phase 2 treats the rendered site as equally or more authoritative. The skill also has extensive hardcoded design opinions (Phase 3+ rules, the AI slop checklist, the 80-item `design-checklist.md` referenced in `review/design-checklist.md`). DESIGN.md violations are flagged as one category among many, not as the primary rubric.

**Q3 — Has hardcoded design opinions?** YES, massively. The `design-checklist.md` file (referenced by the gstack review system) contains 80+ hardcoded design checks across 5 categories. The skill's Important Rules (lines 1142–1166) include hard rejection criteria and a page-type classifier that operates independently of any project file.

**Q4 — Modifies code or only advises?** Modifies code AND commits. The skill description says "iteratively fixes issues in source code, committing each fix atomically and re-verifying with before/after screenshots." This is a fix-and-commit loop, not advisory.

**Q5 — Has ordering dependencies?** Weak dependency: it runs gstack's preamble bash script to check git branch, proactive settings, etc. (lines 29–90). Produces a report that could be consumed by other skills but no explicit handoff protocol.

**Q6 — Conflict resolution?** When code deviates from DESIGN.md: it raises severity of the finding (line 511 says "Deviations from the project's stated design system are higher severity"). It does NOT silently override DESIGN.md. It also does not fail loudly — it flags and fixes. The authority hierarchy is: DESIGN.md calibrates severity, but the skill still applies its own 80-item checklist regardless.

**Verdict: PARTIAL** — The only skill that genuinely reads DESIGN.md and explicitly uses it to calibrate findings. However, DESIGN.md is one input among many rather than the exclusive rubric. The skill's own hardcoded opinions (AI slop detection, landing page rules, responsive standards) are equally or more prominent than DESIGN.md constraints. The Phase 2 extraction that bypasses DESIGN.md in favor of rendered reality further weakens the source-of-truth claim.

---

### 3. /audit

**File**: `~/.claude/skills/audit/SKILL.md` — regular file, 7,620 bytes, 147 lines.

**Q1 — Reads DESIGN.md?** NO. Grep returns zero matches.

**Q2 — Treats DESIGN.md as source of truth?** NO. The skill's rubric is entirely self-contained: 5 dimensions scored 0-4 (Accessibility, Performance, Theming, Responsive Design, Anti-Patterns). The Theming dimension at line 47 checks for "Hard-coded colors: Colors not using design tokens" — but "design tokens" here means CSS custom properties/variables, not any specific project DESIGN.md content.

**Q3 — Has hardcoded design opinions?** YES. The Anti-Patterns dimension (line 66–70) hardcodes specific forbidden patterns: "AI color palette, gradient text, glassmorphism, hero metrics, card grids, generic fonts." These are internal defaults, not derived from any project file. The 0-4 scoring rubrics for each dimension are fixed in the skill.

**Q4 — Modifies code or only advises?** Advisory only. Line 14: "Don't fix issues — document them for other commands to address." Generates a scored report with P0-P3 ratings.

**Q5 — Has ordering dependencies?** YES. Mandatory: "Invoke /frontend-design" (line 10) before proceeding. Produces a report that other skills (listed at line 111–130) are expected to consume and act on. `/audit` is explicitly positioned as a diagnostic first step.

**Q6 — Conflict resolution?** Not applicable — the skill never reads DESIGN.md, so there is no mechanism to detect conflicts with it. Reports issues based solely on its internal rubric.

**Verdict: FAIL** — Zero DESIGN.md references. Reports findings against an entirely internal rubric.

---

### 4. /frontend-design

**File**: `~/.claude/skills/frontend-design/SKILL.md` — regular file, 9,553 bytes, 146 lines.

**Q1 — Reads DESIGN.md?** NO. Grep returns zero matches. The Context Gathering Protocol (lines 19–26) checks for `.impeccable.md` in the project root, not `DESIGN.md`.

**Q2 — Treats DESIGN.md as source of truth?** NO. The skill's design authority hierarchy is: (1) Design Context in current instructions, (2) `.impeccable.md` project file, (3) run `/teach-impeccable` interactively. `DESIGN.md` is entirely absent from this chain.

**Q3 — Has hardcoded design opinions?** YES, this skill is the primary repository of hardcoded design opinions for the entire "impeccable" family. Lines 46–128 contain explicit DO/DON'T lists covering Typography, Color & Theme, Layout & Space, Visual Details, Motion, Interaction, Responsive, and UX Writing. 3 examples: (a) "DON'T use Inter, Roboto, Arial, Open Sans, system defaults" (line 54); (b) "DON'T use the AI color palette: cyan-on-dark, purple-to-blue gradients" (line 67); (c) "DON'T wrap everything in cards" (line 79). These are hardcoded regardless of project context.

**Q4 — Modifies code or only advises?** Modifies code. The skill generates "real working code" (line 39) — it's the implementation skill for creating new components/pages.

**Q5 — Has ordering dependencies?** YES. It is the mandatory dependency that `/normalize` and `/audit` invoke first. It acts as a shared context loader for the entire family. It also links to reference files (`reference/typography.md`, `reference/color-and-contrast.md`, etc.) in its skill directory.

**Q6 — Conflict resolution?** Not applicable to DESIGN.md since it never reads one. When creating code, it applies its own DO/DON'T rubric plus whatever is in `.impeccable.md`.

**Verdict: FAIL** — Zero DESIGN.md references. Uses `.impeccable.md` + internal rubric. Is the foundational skill the others depend on, so this failure propagates to `/normalize` and `/audit`.

---

### 5. /polish

**File**: `~/.claude/skills/polish/SKILL.md` — regular file, 8,107 bytes, 203 lines.

**Q1 — Reads DESIGN.md?** NO. Grep returns zero matches.

**Q2 — Treats DESIGN.md as source of truth?** NO. The skill's polish rubric is entirely internal: 10 dimensions with explicit checklists (Visual Alignment, Typography, Color & Contrast, Interaction States, Micro-interactions, Content & Copy, Icons, Forms, Edge Cases, Responsiveness, Performance, Code Quality). Color & Contrast section (lines 66–73) mentions "Consistent token usage: No hard-coded colors, all use design tokens" but "design tokens" means CSS variables, not DESIGN.md content.

**Q3 — Has hardcoded design opinions?** YES. Examples: (a) Line 71: "Tinted neutrals: No pure gray or pure black — add subtle color tint (0.01 chroma)"; (b) Line 93: "Never bounce or elastic [easing] — they feel dated"; (c) Line 57: "Line length: 45-75 characters for body text." These specific values are hardcoded independent of any project file.

**Q4 — Modifies code or only advises?** Modifies code. The skill performs "a meticulous final pass" and includes a 20-item checklist to verify after changes. Final Verification (lines 192–200) includes "Compare to design: Match intended design" — but "design" here means the user's intended design, not DESIGN.md.

**Q5 — Has ordering dependencies?** YES. "Invoke /frontend-design" (line 10) mandatory before proceeding. The skill is explicitly "the last step" (line 34: "Polish is the last step, not the first"). Positioned to run after all other skills.

**Q6 — Conflict resolution?** Not applicable — never reads DESIGN.md.

**Verdict: FAIL** — Zero DESIGN.md references. Internal rubric only.

---

### 6. /harden

**File**: `~/.claude/skills/harden/SKILL.md` — regular file, 9,059 bytes, 354 lines.

**Q1 — Reads DESIGN.md?** NO. Grep returns zero matches.

**Q2 — Treats DESIGN.md as source of truth?** NO. The skill is entirely focused on resilience: edge cases, i18n, error handling, accessibility, performance. Design system compliance is not in scope. The skill does not invoke `/frontend-design` either — it has no context gathering protocol at all.

**Q3 — Has hardcoded design opinions?** YES, specific to resilience: (a) Line 58 CSS snippet: "overflow: hidden; text-overflow: ellipsis; white-space: nowrap" as standard truncation pattern; (b) Lines 96–99: "Add 30-40% space budget for translations"; (c) Line 88: "minimum readable sizes (14px on mobile)." These are universal best practices, not project-specific.

**Q4 — Modifies code or only advises?** Modifies code. The skill "systematically improves resilience" with explicit code snippets to implement. No commit step mentioned.

**Q5 — Has ordering dependencies?** Minimal. No mandatory pre-skill invocation (unlike normalize/audit/polish which all require /frontend-design). No explicit artifact consumption or production.

**Q6 — Conflict resolution?** Not applicable — scope is resilience/robustness, not design aesthetics. No DESIGN.md to conflict with.

**Verdict: FAIL** — Zero DESIGN.md references. Different scope (resilience vs. design system enforcement), but still a clear FAIL on the audit question.

---

## Aggregate Verdict

**Score**: 0/6 PASS, 1/6 PARTIAL, 5/6 FAIL

**Starred skills (normalize, design-review)**: normalize FAIL, design-review PARTIAL

**Final verdict: FAIL**

### Reasoning

The 6 skills split into two fundamentally different families:

**Family A: "impeccable" skills** (`normalize`, `audit`, `frontend-design`, `polish`, `harden`) — These are a self-contained design enforcement system built around `.impeccable.md`, not `DESIGN.md`. They were authored independently (regular files, April 2, 2026) and share a common dependency chain through `/frontend-design`. **Zero of them read DESIGN.md.** A DESIGN.md file you spend 2-3 hours writing is completely invisible to all 5 of these skills.

**Family B: gstack skills** (`design-review`) — The one skill that reads DESIGN.md (`design-review`) is actually a gstack tool (symlinked) designed for visual QA of a running site via headless browser screenshots. It reads DESIGN.md as a severity calibrator, but: (a) it also extracts the rendered site as a competing source of truth, (b) it applies an 80-item hardcoded checklist regardless of DESIGN.md, and (c) it requires a running app with gstack browser setup — it's a QA tool, not a coding assistant.

The bet was: "6 runtime skills will enforce DESIGN.md when generating/refining frontend code." The reality: the 5 skills that generate/refine frontend code enforce `.impeccable.md`, not `DESIGN.md`. The 1 skill that reads `DESIGN.md` doesn't generate frontend code — it audits a running site.

### Recommendation to NDF S1

**Pause and Ask the user before writing proposal.** The feature as conceived does not work. Investing 2-3 hours writing DESIGN.md content will not be enforced by the runtime skills. The user should choose an alternative architecture before proceeding.

---

## Alternative Architectures

### Option A: Pivot to .impeccable.md (minimal effort, maximum enforcement)

Use the existing `.impeccable.md` mechanism that the "impeccable" skill family already reads. Instead of writing a `DESIGN.md`, write a `.impeccable.md` file with Numind's brand context (target audience: operations/SOP managers; tone: professional-efficient-Chinese SaaS; color palette; typography choices). This file is read by `normalize`, `audit`, `frontend-design`, and `polish` via the `/frontend-design` Context Gathering Protocol. **Cost: ~30 min to write. Enforcement: genuine, immediate, zero new infrastructure.** Tradeoff: `.impeccable.md` is a brand/persona context file, not a detailed design token spec — it's read by AI, not by build tools.

### Option B: DESIGN.md as documentation only, route through /teach-impeccable

Write `DESIGN.md` as intended (2-3 hours), but also run `/teach-impeccable` to bake its content into `.impeccable.md`. The skills read `.impeccable.md`, which summarizes `DESIGN.md`. This preserves `DESIGN.md` as a human-readable design system document, while `.impeccable.md` becomes the machine-readable summary that skills enforce. **Cost: 2-3 hours writing DESIGN.md + 20 min running teach-impeccable. Enforcement: indirect but genuine.** Risk: information loss in the summarization step; changes to DESIGN.md require re-running teach-impeccable to stay in sync.

### Option C: Modify normalize/audit/polish to read DESIGN.md directly

Edit the 5 "impeccable" skill SKILL.md files to add an explicit DESIGN.md read step in their "MANDATORY PREPARATION" section, alongside the existing `.impeccable.md` check. This is the closest to the original feature vision but requires modifying existing skills. **Cost: ~1 hour to modify 5 skills + write DESIGN.md. Enforcement: genuine and direct.** Risk: modifying shared skills has downstream effects; needs careful design to avoid conflicts between DESIGN.md and `.impeccable.md` authorities.
