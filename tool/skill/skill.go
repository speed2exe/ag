package skill

import (
	"cmp"
	"encoding/json/v2"
	"maps"
	"slices"

	"github.com/speed2exe/ag/setting"
)

type Skills struct {
	SkillByFilePath        map[string]*Skill
	PreloadSkillByFilePath map[string]string
}

// New loads skills from the configured skill paths and stores them in Skills.
func New(settings *setting.Settings, log func(string)) *Skills {
	return &Skills{
		SkillByFilePath:        loadSkills(log, settings.SkillPaths),
		PreloadSkillByFilePath: preloadSkills(log, settings.PreloadSkillPaths),
	}
}

// Skill represents a loaded Agent Skills package (https://agentskills.io/specification).
type Skill struct {
	Frontmatter any    `json:"frontmatter"` // raw YAML frontmatter as a generic map
	FilePath    string `json:"file_path"`   // discovered path to the SKILL.md file
}

// BuildInstructions returns system prompt fragments for preloaded and on-demand skills.
// Entries are sorted to keep prompt order deterministic for prompt cache hits.
func (s *Skills) BuildInstructions() []string {
	var instructions []string
	if len(s.PreloadSkillByFilePath) > 0 {
		preloaded := slices.Collect(maps.Values(s.PreloadSkillByFilePath))
		slices.Sort(preloaded)
		instructions = append(instructions, preloaded...)
	}
	if len(s.SkillByFilePath) > 0 {
		list := slices.Collect(maps.Values(s.SkillByFilePath))
		slices.SortFunc(list, func(a, b *Skill) int {
			return cmp.Compare(a.FilePath, b.FilePath)
		})
		bs, _ := json.Marshal(list)
		instructions = append(instructions,
			"You have access to the following skills. To activate a skill, read the respective SKILL.md.\n"+string(bs))
	}
	return instructions
}
