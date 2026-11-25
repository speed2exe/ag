package skill

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

func loadSkills(log func(string), roots []string) map[string]*Skill {
	return loadFromPaths(log, roots, "loaded", loadSkillFile)
}

func preloadSkills(log func(string), roots []string) map[string]string {
	return loadFromPaths(log, roots, "preloaded", func(path string) (string, error) {
		contents, err := os.ReadFile(path)
		return string(contents), err
	})
}

func loadFromPaths[T any](log func(string), roots []string, verb string, parse func(string) (T, error)) map[string]T {
	result := make(map[string]T)
	visited := make(map[string]bool)
	for _, root := range roots {
		count := 0
		if err := filepath.WalkDir(root, func(leaf string, d fs.DirEntry, err error) error {
			if err != nil {
				log(fmt.Sprintf("warning: walking skill path %q: %v\n", leaf, err))
				return nil
			}
			if visited[leaf] {
				return nil
			}
			visited[leaf] = true
			if d.IsDir() || d.Name() != "SKILL.md" {
				return nil
			}
			v, err := parse(leaf)
			if err != nil {
				log(fmt.Sprintf("warning: skill %q: %v\n", leaf, err))
				return nil
			}
			result[leaf] = v
			count++
			return nil
		}); err != nil {
			log(fmt.Sprintf("warning: reading skill path %q: %v\n", root, err))
		}
		log(fmt.Sprintf("%s %d skills from %s\n", verb, count, root))
	}
	return result
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
	if !scanner.Scan() || strings.TrimSpace(strings.TrimPrefix(scanner.Text(), "\xef\xbb\xbf")) != "---" {
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
