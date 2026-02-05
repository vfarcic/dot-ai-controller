package controller

import (
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// PatternMatcher handles file path matching using glob patterns.
// It supports include patterns (files to sync) and exclude patterns
// (files to skip even if they match include patterns).
type PatternMatcher struct {
	includes []string
	excludes []string
}

// NewPatternMatcher creates a new PatternMatcher with the given include and exclude patterns.
// Patterns use doublestar syntax:
//   - "*" matches any sequence of non-separator characters
//   - "**" matches any sequence of characters including separators
//   - "?" matches any single non-separator character
//
// Examples:
//   - "docs/**/*.md" matches all .md files under docs/
//   - "README.md" matches only README.md at the root
//   - "*.go" matches all .go files at the root level only
func NewPatternMatcher(includes, excludes []string) *PatternMatcher {
	return &PatternMatcher{
		includes: includes,
		excludes: excludes,
	}
}

// Matches returns true if the path matches any include pattern
// and does not match any exclude pattern.
// Paths should use forward slashes as separators (Unix-style).
func (p *PatternMatcher) Matches(path string) bool {
	// Normalize path separators to forward slash
	path = filepath.ToSlash(path)
	// Also replace backslashes directly (for strings not from filepath functions)
	path = strings.ReplaceAll(path, "\\", "/")

	// Remove leading slash if present for consistent matching
	path = strings.TrimPrefix(path, "/")

	// First check if it matches any include pattern
	matchesInclude := false
	for _, pattern := range p.includes {
		pattern = filepath.ToSlash(pattern)
		matched, err := doublestar.Match(pattern, path)
		if err == nil && matched {
			matchesInclude = true
			break
		}
	}

	if !matchesInclude {
		return false
	}

	// Then check if it matches any exclude pattern
	for _, pattern := range p.excludes {
		pattern = filepath.ToSlash(pattern)
		matched, err := doublestar.Match(pattern, path)
		if err == nil && matched {
			return false
		}
	}

	return true
}

// FilterFiles returns a new slice containing only the paths that match
// the include patterns and do not match any exclude patterns.
func (p *PatternMatcher) FilterFiles(files []string) []string {
	var result []string
	for _, file := range files {
		if p.Matches(file) {
			result = append(result, file)
		}
	}
	return result
}

// MatchingFiles walks a directory tree and returns all file paths that match
// the include patterns and do not match any exclude patterns.
// The basePath is stripped from the returned paths.
func (p *PatternMatcher) MatchingFiles(basePath string) ([]string, error) {
	basePath = filepath.ToSlash(basePath)
	if !strings.HasSuffix(basePath, "/") {
		basePath += "/"
	}

	var matches []string

	// Use doublestar.Glob for each include pattern
	for _, pattern := range p.includes {
		pattern = filepath.ToSlash(pattern)
		fullPattern := basePath + pattern

		files, err := doublestar.FilepathGlob(fullPattern)
		if err != nil {
			return nil, err
		}

		for _, file := range files {
			// Convert to relative path
			relPath := strings.TrimPrefix(filepath.ToSlash(file), basePath)

			// Check excludes
			excluded := false
			for _, excludePattern := range p.excludes {
				excludePattern = filepath.ToSlash(excludePattern)
				matched, err := doublestar.Match(excludePattern, relPath)
				if err == nil && matched {
					excluded = true
					break
				}
			}

			if !excluded {
				matches = append(matches, relPath)
			}
		}
	}

	// Remove duplicates (a file might match multiple include patterns)
	return uniqueStrings(matches), nil
}

// uniqueStrings returns a new slice with duplicates removed, preserving order.
func uniqueStrings(input []string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, s := range input {
		if !seen[s] {
			seen[s] = true
			result = append(result, s)
		}
	}
	return result
}
