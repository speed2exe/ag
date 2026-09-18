// Package agentsmd implements discovery and loading of AGENTS.md files.
//
// AGENTS.md is a plain-markdown convention (https://agents.md) for providing
// project-level context and instructions to AI coding agents — environment
// tips, test commands, coding conventions, etc.
//
// Discovery walks from the working directory up to the git root (or the
// filesystem root if no git root is found), collecting every AGENTS.md found
// along the way. Files are returned outermost-first so that closer (more
// specific) files appear later and take implicit precedence when all content
// is concatenated into the system prompt.
package agentsmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const filename = "AGENTS.md"

// Load discovers and concatenates all AGENTS.md files reachable from dir,
// walking up to the git root. Returns an empty string when none are found.
func Load(log func(msg string), dir string) string {
	dirs := collectDirs(dir)
	var sections []string
	for _, d := range dirs {
		content, err := os.ReadFile(filepath.Join(d, filename))
		if err != nil {
			continue
		}
		log(fmt.Sprintf("loaded %s from %s\n", filename, d))
		sections = append(sections, strings.TrimSpace(string(content)))
	}
	if len(sections) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("The following project instructions come from ")
	b.WriteString(filename)
	b.WriteString(" files found in this project:\n\n")
	for _, s := range sections {
		b.WriteString(s)
		b.WriteString("\n\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// collectDirs returns directories from the git root down to dir (inclusive),
// so outermost files are first. If no git root is found, only dir is returned.
func collectDirs(dir string) []string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return []string{dir}
	}

	// Walk up from dir, collecting each directory until we find .git or hit root.
	var chain []string
	current := abs
	for {
		chain = append(chain, current)
		if isGitRoot(current) {
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			// filesystem root — no git root found, use only the starting dir
			return []string{abs}
		}
		current = parent
	}

	// Reverse so outermost (git root) is first.
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain
}

func isGitRoot(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}
