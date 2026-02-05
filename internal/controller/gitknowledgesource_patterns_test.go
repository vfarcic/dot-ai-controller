package controller

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("PatternMatcher", func() {
	Describe("Matches", func() {
		Context("with single include pattern", func() {
			It("should match files matching the pattern", func() {
				matcher := NewPatternMatcher([]string{"docs/**/*.md"}, nil)

				Expect(matcher.Matches("docs/guide.md")).To(BeTrue())
				Expect(matcher.Matches("docs/setup/install.md")).To(BeTrue())
				Expect(matcher.Matches("docs/nested/deep/file.md")).To(BeTrue())
			})

			It("should not match files not matching the pattern", func() {
				matcher := NewPatternMatcher([]string{"docs/**/*.md"}, nil)

				Expect(matcher.Matches("README.md")).To(BeFalse())
				Expect(matcher.Matches("src/main.go")).To(BeFalse())
				Expect(matcher.Matches("docs/guide.txt")).To(BeFalse())
			})

			It("should match root-level files with simple patterns", func() {
				matcher := NewPatternMatcher([]string{"README.md"}, nil)

				Expect(matcher.Matches("README.md")).To(BeTrue())
				Expect(matcher.Matches("docs/README.md")).To(BeFalse())
			})

			It("should match with wildcard patterns", func() {
				matcher := NewPatternMatcher([]string{"*.md"}, nil)

				Expect(matcher.Matches("README.md")).To(BeTrue())
				Expect(matcher.Matches("CONTRIBUTING.md")).To(BeTrue())
				Expect(matcher.Matches("docs/guide.md")).To(BeFalse()) // Only root level
			})
		})

		Context("with multiple include patterns", func() {
			It("should match files matching any include pattern", func() {
				matcher := NewPatternMatcher([]string{"docs/**/*.md", "README.md", "*.txt"}, nil)

				Expect(matcher.Matches("docs/guide.md")).To(BeTrue())
				Expect(matcher.Matches("README.md")).To(BeTrue())
				Expect(matcher.Matches("notes.txt")).To(BeTrue())
			})
		})

		Context("with exclude patterns", func() {
			It("should exclude files matching exclude patterns", func() {
				matcher := NewPatternMatcher(
					[]string{"docs/**/*.md"},
					[]string{"docs/internal/**"},
				)

				Expect(matcher.Matches("docs/guide.md")).To(BeTrue())
				Expect(matcher.Matches("docs/public/api.md")).To(BeTrue())
				Expect(matcher.Matches("docs/internal/secret.md")).To(BeFalse())
				Expect(matcher.Matches("docs/internal/nested/file.md")).To(BeFalse())
			})

			It("should handle multiple exclude patterns", func() {
				matcher := NewPatternMatcher(
					[]string{"**/*.md"},
					[]string{"docs/internal/**", "docs/draft/**", "**/PRIVATE.md"},
				)

				Expect(matcher.Matches("docs/guide.md")).To(BeTrue())
				Expect(matcher.Matches("docs/internal/secret.md")).To(BeFalse())
				Expect(matcher.Matches("docs/draft/wip.md")).To(BeFalse())
				Expect(matcher.Matches("docs/PRIVATE.md")).To(BeFalse())
			})

			It("should give exclude patterns precedence over includes", func() {
				matcher := NewPatternMatcher(
					[]string{"docs/**/*.md", "docs/internal/important.md"},
					[]string{"docs/internal/**"},
				)

				// Even though docs/internal/important.md matches an include pattern,
				// it should be excluded because it matches the exclude pattern
				Expect(matcher.Matches("docs/internal/important.md")).To(BeFalse())
			})
		})

		Context("with path normalization", func() {
			It("should handle leading slashes", func() {
				matcher := NewPatternMatcher([]string{"docs/**/*.md"}, nil)

				Expect(matcher.Matches("/docs/guide.md")).To(BeTrue())
				Expect(matcher.Matches("docs/guide.md")).To(BeTrue())
			})

			It("should handle backslashes on Windows-style paths", func() {
				matcher := NewPatternMatcher([]string{"docs/**/*.md"}, nil)

				// Backslashes are converted to forward slashes
				Expect(matcher.Matches("docs\\guide.md")).To(BeTrue())
				Expect(matcher.Matches("docs\\nested\\file.md")).To(BeTrue())
			})
		})

		Context("edge cases", func() {
			It("should handle empty include patterns", func() {
				matcher := NewPatternMatcher([]string{}, nil)

				Expect(matcher.Matches("anything.md")).To(BeFalse())
			})

			It("should handle patterns with character classes", func() {
				// In doublestar, [abc] is a character class matching a, b, or c
				matcher := NewPatternMatcher([]string{"docs/[gG]uide.md"}, nil)

				Expect(matcher.Matches("docs/guide.md")).To(BeTrue())
				Expect(matcher.Matches("docs/Guide.md")).To(BeTrue())
				Expect(matcher.Matches("docs/xuide.md")).To(BeFalse())
			})

			It("should match dotfiles when explicitly included", func() {
				matcher := NewPatternMatcher([]string{"**/.gitignore"}, nil)

				Expect(matcher.Matches(".gitignore")).To(BeTrue())
				Expect(matcher.Matches("subdir/.gitignore")).To(BeTrue())
			})
		})
	})

	Describe("FilterFiles", func() {
		It("should filter a list of files", func() {
			matcher := NewPatternMatcher(
				[]string{"docs/**/*.md", "README.md"},
				[]string{"docs/internal/**"},
			)

			files := []string{
				"README.md",
				"main.go",
				"docs/guide.md",
				"docs/internal/secret.md",
				"docs/public/api.md",
			}

			filtered := matcher.FilterFiles(files)

			Expect(filtered).To(ConsistOf(
				"README.md",
				"docs/guide.md",
				"docs/public/api.md",
			))
		})

		It("should return empty slice for no matches", func() {
			matcher := NewPatternMatcher([]string{"*.md"}, nil)

			files := []string{"main.go", "test.py", "config.yaml"}

			filtered := matcher.FilterFiles(files)

			Expect(filtered).To(BeEmpty())
		})

		It("should handle nil input", func() {
			matcher := NewPatternMatcher([]string{"*.md"}, nil)

			filtered := matcher.FilterFiles(nil)

			Expect(filtered).To(BeNil())
		})
	})

	Describe("MatchingFiles", func() {
		var tempDir string

		BeforeEach(func() {
			var err error
			tempDir, err = os.MkdirTemp("", "pattern-test-*")
			Expect(err).NotTo(HaveOccurred())

			// Create test directory structure
			dirs := []string{
				"docs",
				"docs/guides",
				"docs/internal",
				"src",
			}
			for _, dir := range dirs {
				err = os.MkdirAll(filepath.Join(tempDir, dir), 0755)
				Expect(err).NotTo(HaveOccurred())
			}

			// Create test files
			files := []string{
				"README.md",
				"main.go",
				"docs/index.md",
				"docs/guides/setup.md",
				"docs/internal/secret.md",
				"src/code.go",
			}
			for _, file := range files {
				err = os.WriteFile(filepath.Join(tempDir, file), []byte("test"), 0644)
				Expect(err).NotTo(HaveOccurred())
			}
		})

		AfterEach(func() {
			os.RemoveAll(tempDir)
		})

		It("should find files matching include patterns", func() {
			matcher := NewPatternMatcher([]string{"**/*.md"}, nil)

			matches, err := matcher.MatchingFiles(tempDir)
			Expect(err).NotTo(HaveOccurred())

			Expect(matches).To(ConsistOf(
				"README.md",
				"docs/index.md",
				"docs/guides/setup.md",
				"docs/internal/secret.md",
			))
		})

		It("should exclude files matching exclude patterns", func() {
			matcher := NewPatternMatcher(
				[]string{"**/*.md"},
				[]string{"docs/internal/**"},
			)

			matches, err := matcher.MatchingFiles(tempDir)
			Expect(err).NotTo(HaveOccurred())

			Expect(matches).To(ConsistOf(
				"README.md",
				"docs/index.md",
				"docs/guides/setup.md",
			))
			Expect(matches).NotTo(ContainElement("docs/internal/secret.md"))
		})

		It("should handle multiple include patterns without duplicates", func() {
			matcher := NewPatternMatcher(
				[]string{"README.md", "*.md", "**/*.md"},
				nil,
			)

			matches, err := matcher.MatchingFiles(tempDir)
			Expect(err).NotTo(HaveOccurred())

			// README.md should appear only once even though it matches multiple patterns
			count := 0
			for _, m := range matches {
				if m == "README.md" {
					count++
				}
			}
			Expect(count).To(Equal(1))
		})

		It("should return empty slice for non-existent directory", func() {
			matcher := NewPatternMatcher([]string{"**/*.md"}, nil)

			matches, err := matcher.MatchingFiles("/non/existent/path")
			Expect(err).NotTo(HaveOccurred())
			Expect(matches).To(BeEmpty())
		})
	})

	Describe("uniqueStrings", func() {
		It("should remove duplicates preserving order", func() {
			input := []string{"a", "b", "a", "c", "b", "d"}
			result := uniqueStrings(input)

			Expect(result).To(Equal([]string{"a", "b", "c", "d"}))
		})

		It("should handle empty input", func() {
			result := uniqueStrings([]string{})
			Expect(result).To(BeEmpty())
		})

		It("should handle nil input", func() {
			result := uniqueStrings(nil)
			Expect(result).To(BeNil())
		})
	})
})
