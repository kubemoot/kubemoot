package bus

import (
	"os"
	"path/filepath"
	"strings"
)

// Instructions assembles what a turn tells the harness to follow: the agent's system prompt, and the
// bodies of the skills the coordinator selected for the thread.
type Instructions struct {
	system    string
	skillsDir string
}

// LoadInstructions reads the system prompt file once. A missing file is an agent with no
// instructions, not an error: the policy ConfigMap is optional.
func LoadInstructions(systemFile, skillsDir string) Instructions {
	var system string
	if systemFile != "" {
		if b, err := os.ReadFile(systemFile); err == nil {
			system = string(b)
		}
	}
	return Instructions{system: system, skillsDir: skillsDir}
}

// For returns the instructions for a turn that selected the named skills. A skill name that is
// malformed (a path separator) or whose file is missing is skipped; it never fails the turn.
func (i Instructions) For(skills []string) string {
	body := i.skills(skills)
	switch {
	case i.system == "":
		return body
	case body == "":
		return i.system
	}
	return i.system + "\n\n" + body
}

func (i Instructions) skills(names []string) string {
	var sb strings.Builder
	for _, name := range names {
		if name == "" || strings.ContainsAny(name, "/\\\x00") || i.skillsDir == "" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(i.skillsDir, name+".txt"))
		if err != nil {
			continue
		}
		if sb.Len() > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString("--- Skill: " + name + " ---\n" + string(b))
	}
	if sb.Len() == 0 {
		return ""
	}
	return "Skills context for this discussion:\n\n" + sb.String() + "\n"
}
