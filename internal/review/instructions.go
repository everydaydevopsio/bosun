package review

import (
	"path"
	"strings"
)

// Instruction files are executed by an agent or a person rather than a
// compiler: skills, agent rules, runbooks, CI workflows, and prompts. They
// are dense, so a small diff of them can still need a deep read (#110).
const (
	// instructionMinLines keeps a one-line workflow bump or a typo fix in an
	// AGENTS.md from paying for a deep review.
	instructionMinLines = 50
	// instructionShare is the fraction of reviewable lines, in percent, that
	// instruction files must exceed for the change to count as one.
	instructionShare = 50
)

var instructionNames = map[string]bool{
	"AGENTS.md": true, "CLAUDE.md": true, "GEMINI.md": true, "SKILL.md": true,
	"copilot-instructions.md": true, ".cursorrules": true, ".windsurfrules": true,
}

// isInstructionFile reports whether a repository-relative path holds
// instructions an agent or operator follows step by step.
func isInstructionFile(p string) bool {
	if instructionNames[path.Base(p)] {
		return true
	}
	for _, dir := range []string{".cursor/rules/", ".claude/", ".agents/", ".github/workflows/", "prompts/"} {
		if strings.HasPrefix(p, dir) || strings.Contains(p, "/"+dir) {
			return true
		}
	}
	lower := strings.ToLower(p)
	return strings.Contains(path.Base(lower), "runbook") || strings.HasPrefix(lower, "runbooks/") || strings.Contains(lower, "/runbooks/")
}

// instructionHeavy reports whether a sized change is mostly instruction
// files, and if so a reason such as "instruction_lines=120 of 150".
func instructionHeavy(stats *DiffStats) string {
	total := stats.Reviewable.Additions + stats.Reviewable.Deletions
	instruction := 0
	for _, f := range stats.changed {
		if !f.generated && !f.binary && isInstructionFile(f.path) {
			instruction += f.additions + f.deletions
		}
	}
	if instruction < instructionMinLines || instruction*100 <= total*instructionShare {
		return ""
	}
	return "instruction_lines=" + itoa(instruction) + " of " + itoa(total)
}
