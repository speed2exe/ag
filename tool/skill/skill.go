package skill

import (
	"strings"

	"encoding/json/v2"
	"github.com/speed2exe/ag/setting"
)

type Skills struct {
	SkillByFilePath        map[string]*Skill
	PreloadSkillByFilePath map[string]string
}

// Init loads skills from the configured skill paths and stores them in Skills.
func New(settings *setting.Settings, log func(s string)) *Skills {
	return &Skills{
		SkillByFilePath:        LoadFromPaths(log, settings.SkillPaths),
		PreloadSkillByFilePath: PreloadFromPaths(log, settings.PreloadSkillPaths),
	}
}

// Skill represents a loaded Agent Skills package.
// The format follows https://agentskills.io/specification:
//   - A skill is a directory containing a SKILL.md file.
//   - SKILL.md has YAML frontmatter (name, description, …) followed by Markdown instructions.
//   - Metadata is injected into the agent's instructions at startup.
//   - Full instructions are loaded on demand when the agent activates the skill.
type Skill struct {
	Frontmatter any    `json:"frontmatter"` // raw YAML frontmatter as a generic map
	FilePath    string `json:"file_path"`   // discovered path to the SKILL.md file
}

// BuildSkillInstructions returns a system-level prompt fragment listing all available skills.
// Each skill entry includes its name, description, and the path to its SKILL.md file
// so the model can resolve relative references within the skill body.
// This is injected into ResponsesRequest.Instructions so the model knows what skills exist.
func BuildSkillInstructions(ss map[string]*Skill) string {
	if len(ss) == 0 {
		return ""
	}
	skillsList := make([]*Skill, 0, len(ss))
	for _, skill := range ss {
		skillsList = append(skillsList, skill)
	}

	var b strings.Builder
	b.WriteString("You have access to the following skills. ")
	b.WriteString("To activate a skill, read the respective SKILL.md.\n")
	bs, _ := json.Marshal(skillsList)
	b.Write(bs)
	return b.String()
}

func BuildPreloadSkillInstructions(ss map[string]string) []string {
	if len(ss) == 0 {
		return nil
	}
	var res []string
	for _, content := range ss {
		res = append(res, content)
	}
	return res
}
