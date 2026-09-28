package skill

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// LoadFromPaths discovers and loads Agent Skills from a list of paths.
// Each path may be a SKILL.md file or a directory searched recursively for
// SKILL.md files. Missing or invalid paths emit warnings. Skills are keyed by
// discovered file path, so duplicate frontmatter names may coexist.
func LoadFromPaths(log func(s string), paths []string) map[string]*Skill {
	skillByFilePath := make(map[string]*Skill)
	countByPath := make(map[string]int, len(paths))
	for _, p := range paths {
		countByPath[p] = 0
	}

	walkSkillFiles(log, paths, func(root, path string) {
		s, err := loadSkillFile(path)
		if err != nil {
			log(fmt.Sprintf("warning: skill %q: %v\n", path, err))
			return
		}
		skillByFilePath[path] = s
		countByPath[root]++
	})

	for _, p := range paths {
		log(fmt.Sprintf("loaded %d skills from %s\n", countByPath[p], p))
	}

	return skillByFilePath
}

func PreloadFromPaths(log func(s string), paths []string) map[string]string {
	skillByFilePath := make(map[string]string)
	countByPath := make(map[string]int, len(paths))
	for _, p := range paths {
		countByPath[p] = 0
	}

	walkSkillFiles(log, paths, func(root, path string) {
		// load entire file content as string for preload skills
		f, err := os.Open(path)
		if err != nil {
			log(fmt.Sprintf("warning: opening skill file %q: %v\n", path, err))
			return
		}
		defer f.Close()
		contents, err := io.ReadAll(f)
		if err != nil {
			log(fmt.Sprintf("warning: reading skill file %q: %v\n", path, err))
			return
		}

		skillByFilePath[path] = string(contents)
		countByPath[root]++
	})

	for _, p := range paths {
		log(fmt.Sprintf("preloaded %d skills from %s\n", countByPath[p], p))
	}

	return skillByFilePath
}

func walkSkillFiles(log func(s string), paths []string, load func(root, path string)) {
	visitedPaths := make(map[string]struct{})
	for _, root := range paths {
		if err := filepath.WalkDir(root, func(leaf string, _ fs.DirEntry, err error) error {
			if err != nil {
				log(fmt.Sprintf("warning: walking skill path %q: %v\n", leaf, err))
				return nil
			}

			// Skip if we've already visited this path (handles duplicates and cycles).
			if _, visited := visitedPaths[leaf]; visited {
				return nil
			}
			visitedPaths[leaf] = struct{}{}

			if !strings.HasSuffix(leaf, "/SKILL.md") {
				return nil
			}
			load(root, leaf)
			return nil
		}); err != nil {
			log(fmt.Sprintf("warning: reading skill path %q: %v\n", root, err))
		}
	}
}

// loadSkillFile parses SKILL.md content according to the Agent Skills specification.
// The loader requires delimited, parseable YAML frontmatter but does not
// validate or interpret individual fields.
func loadSkillFile(filePath string) (*Skill, error) {
	frontmatterStr, err := readFrontmatter(filePath)
	if err != nil {
		return nil, fmt.Errorf("reading SKILL.md: %w", err)
	}

	var fm any
	if err := yaml.Unmarshal([]byte(frontmatterStr), &fm); err != nil {
		return nil, fmt.Errorf("SKILL.md frontmatter: %w", err)
	}

	return &Skill{
		Frontmatter: fm,
		FilePath:    filePath,
	}, nil
}

func readFrontmatter(filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("reading file: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)

	// First line must be "---" (skip BOM if present).
	if !scanner.Scan() {
		return "", errors.New("SKILL.md must begin with YAML frontmatter (---)")
	}
	firstLine := strings.TrimPrefix(scanner.Text(), "\xef\xbb\xbf")
	if strings.TrimSpace(firstLine) != "---" {
		return "", errors.New("SKILL.md must begin with YAML frontmatter (---)")
	}

	var sb strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "---" {
			return sb.String(), nil
		}
		sb.WriteString(line)
		sb.WriteByte('\n')
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("reading file: %w", err)
	}
	return "", errors.New("SKILL.md has unclosed frontmatter (missing closing ---)")
}
