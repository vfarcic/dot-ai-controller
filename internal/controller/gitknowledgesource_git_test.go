package controller

import (
	"context"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("GitClient", func() {
	var (
		ctx      context.Context
		tempDir  string
		cloneDir string
	)

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		tempDir, err = os.MkdirTemp("", "git-client-test-*")
		Expect(err).NotTo(HaveOccurred())
		cloneDir = filepath.Join(tempDir, "repo")
	})

	AfterEach(func() {
		os.RemoveAll(tempDir)
	})

	Describe("NewGitClient", func() {
		It("should use default branch when not specified", func() {
			client := NewGitClient(GitClientConfig{
				URL:      "https://github.com/example/repo.git",
				CloneDir: cloneDir,
			})

			Expect(client.branch).To(Equal("main"))
		})

		It("should use specified branch", func() {
			client := NewGitClient(GitClientConfig{
				URL:      "https://github.com/example/repo.git",
				Branch:   "develop",
				CloneDir: cloneDir,
			})

			Expect(client.branch).To(Equal("develop"))
		})

		It("should use DefaultCloneDepth for first sync", func() {
			client := NewGitClient(GitClientConfig{
				URL:              "https://github.com/example/repo.git",
				CloneDir:         cloneDir,
				LastSyncedCommit: "", // First sync
			})

			Expect(client.depth).To(Equal(DefaultCloneDepth))
		})

		It("should use IncrementalCloneDepth for subsequent syncs", func() {
			client := NewGitClient(GitClientConfig{
				URL:              "https://github.com/example/repo.git",
				CloneDir:         cloneDir,
				LastSyncedCommit: "abc123",
			})

			Expect(client.depth).To(Equal(IncrementalCloneDepth))
		})

		It("should use explicit depth when provided", func() {
			client := NewGitClient(GitClientConfig{
				URL:      "https://github.com/example/repo.git",
				CloneDir: cloneDir,
				Depth:    10,
			})

			Expect(client.depth).To(Equal(10))
		})
	})

	Describe("Clone", func() {
		It("should clone a public repository", func() {
			// Use this repo as test target (public, always available)
			client := NewGitClient(GitClientConfig{
				URL:      "https://github.com/vfarcic/dot-ai-controller.git",
				Branch:   "main",
				CloneDir: cloneDir,
				Depth:    1,
			})

			err := client.Clone(ctx)
			Expect(err).NotTo(HaveOccurred())

			// Verify clone directory exists
			_, err = os.Stat(cloneDir)
			Expect(err).NotTo(HaveOccurred())

			// Verify .git directory exists
			_, err = os.Stat(filepath.Join(cloneDir, ".git"))
			Expect(err).NotTo(HaveOccurred())

			// Cleanup
			err = client.Cleanup()
			Expect(err).NotTo(HaveOccurred())
		})

		It("should clean existing directory before clone", func() {
			// Create a file in the clone directory
			err := os.MkdirAll(cloneDir, 0755)
			Expect(err).NotTo(HaveOccurred())
			err = os.WriteFile(filepath.Join(cloneDir, "existing.txt"), []byte("test"), 0644)
			Expect(err).NotTo(HaveOccurred())

			client := NewGitClient(GitClientConfig{
				URL:      "https://github.com/vfarcic/dot-ai-controller.git",
				Branch:   "main",
				CloneDir: cloneDir,
				Depth:    1,
			})

			err = client.Clone(ctx)
			Expect(err).NotTo(HaveOccurred())

			// Old file should be gone
			_, err = os.Stat(filepath.Join(cloneDir, "existing.txt"))
			Expect(os.IsNotExist(err)).To(BeTrue())

			client.Cleanup()
		})

		It("should fail for non-existent repository", func() {
			client := NewGitClient(GitClientConfig{
				URL:      "https://github.com/vfarcic/definitely-does-not-exist-12345.git",
				CloneDir: cloneDir,
			})

			err := client.Clone(ctx)
			Expect(err).To(HaveOccurred())
			Expect(IsRepoNotFoundError(err)).To(BeTrue())
		})

		It("should fail for invalid URL", func() {
			client := NewGitClient(GitClientConfig{
				URL:      "not-a-valid-url",
				CloneDir: cloneDir,
			})

			err := client.Clone(ctx)
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("GetHeadCommit", func() {
		var client *GitClient

		BeforeEach(func() {
			client = NewGitClient(GitClientConfig{
				URL:      "https://github.com/vfarcic/dot-ai-controller.git",
				Branch:   "main",
				CloneDir: cloneDir,
				Depth:    1,
			})
			err := client.Clone(ctx)
			Expect(err).NotTo(HaveOccurred())
		})

		AfterEach(func() {
			client.Cleanup()
		})

		It("should return a valid commit SHA", func() {
			sha, err := client.GetHeadCommit(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(sha).To(HaveLen(40)) // Git SHA is 40 hex characters
			Expect(sha).To(MatchRegexp("^[0-9a-f]{40}$"))
		})

		It("should fail if repository not cloned", func() {
			uninitClient := NewGitClient(GitClientConfig{
				URL:      "https://github.com/example/repo.git",
				CloneDir: filepath.Join(tempDir, "other"),
			})

			_, err := uninitClient.GetHeadCommit(ctx)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("not initialized"))
		})
	})

	Describe("GetAllFiles", func() {
		var client *GitClient

		BeforeEach(func() {
			client = NewGitClient(GitClientConfig{
				URL:      "https://github.com/vfarcic/dot-ai-controller.git",
				Branch:   "main",
				CloneDir: cloneDir,
				Depth:    1,
			})
			err := client.Clone(ctx)
			Expect(err).NotTo(HaveOccurred())
		})

		AfterEach(func() {
			client.Cleanup()
		})

		It("should return list of files", func() {
			files, err := client.GetAllFiles(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(files).NotTo(BeEmpty())

			// Should contain common files
			Expect(files).To(ContainElement("README.md"))
			Expect(files).To(ContainElement("go.mod"))
		})
	})

	Describe("GetFileContent", func() {
		var client *GitClient

		BeforeEach(func() {
			client = NewGitClient(GitClientConfig{
				URL:      "https://github.com/vfarcic/dot-ai-controller.git",
				Branch:   "main",
				CloneDir: cloneDir,
				Depth:    1,
			})
			err := client.Clone(ctx)
			Expect(err).NotTo(HaveOccurred())
		})

		AfterEach(func() {
			client.Cleanup()
		})

		It("should return file content", func() {
			content, err := client.GetFileContent(ctx, "README.md")
			Expect(err).NotTo(HaveOccurred())
			Expect(content).NotTo(BeEmpty())
		})

		It("should fail for non-existent file", func() {
			_, err := client.GetFileContent(ctx, "does-not-exist.txt")
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("GetFileSize", func() {
		var client *GitClient

		BeforeEach(func() {
			client = NewGitClient(GitClientConfig{
				URL:      "https://github.com/vfarcic/dot-ai-controller.git",
				Branch:   "main",
				CloneDir: cloneDir,
				Depth:    1,
			})
			err := client.Clone(ctx)
			Expect(err).NotTo(HaveOccurred())
		})

		AfterEach(func() {
			client.Cleanup()
		})

		It("should return file size", func() {
			size, err := client.GetFileSize(ctx, "README.md")
			Expect(err).NotTo(HaveOccurred())
			Expect(size).To(BeNumerically(">", 0))
		})

		It("should fail for non-existent file", func() {
			_, err := client.GetFileSize(ctx, "does-not-exist.txt")
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("GetChangedFiles", func() {
		It("should return nil for first sync (no lastSyncedCommit)", func() {
			client := NewGitClient(GitClientConfig{
				URL:              "https://github.com/vfarcic/dot-ai-controller.git",
				Branch:           "main",
				CloneDir:         cloneDir,
				Depth:            1,
				LastSyncedCommit: "", // First sync
			})

			err := client.Clone(ctx)
			Expect(err).NotTo(HaveOccurred())
			defer client.Cleanup()

			files, found, err := client.GetChangedFiles(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse()) // Indicates first sync
			Expect(files).To(BeNil())
		})

		It("should return nil when lastSyncedCommit not in history", func() {
			client := NewGitClient(GitClientConfig{
				URL:              "https://github.com/vfarcic/dot-ai-controller.git",
				Branch:           "main",
				CloneDir:         cloneDir,
				Depth:            1,
				LastSyncedCommit: "0000000000000000000000000000000000000000", // Non-existent
			})

			err := client.Clone(ctx)
			Expect(err).NotTo(HaveOccurred())
			defer client.Cleanup()

			files, found, err := client.GetChangedFiles(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse()) // Fallback to full sync
			Expect(files).To(BeNil())
		})

		It("should return empty list when HEAD equals lastSyncedCommit", func() {
			client := NewGitClient(GitClientConfig{
				URL:      "https://github.com/vfarcic/dot-ai-controller.git",
				Branch:   "main",
				CloneDir: cloneDir,
				Depth:    1,
			})

			err := client.Clone(ctx)
			Expect(err).NotTo(HaveOccurred())
			defer client.Cleanup()

			// Get current HEAD
			headSHA, err := client.GetHeadCommit(ctx)
			Expect(err).NotTo(HaveOccurred())

			// Create new client with current HEAD as lastSyncedCommit
			client2 := NewGitClient(GitClientConfig{
				URL:              "https://github.com/vfarcic/dot-ai-controller.git",
				Branch:           "main",
				CloneDir:         filepath.Join(tempDir, "repo2"),
				Depth:            1,
				LastSyncedCommit: headSHA,
			})

			err = client2.Clone(ctx)
			Expect(err).NotTo(HaveOccurred())
			defer client2.Cleanup()

			files, found, err := client2.GetChangedFiles(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(files).To(BeEmpty()) // No changes
		})
	})

	Describe("Cleanup", func() {
		It("should remove clone directory", func() {
			client := NewGitClient(GitClientConfig{
				URL:      "https://github.com/vfarcic/dot-ai-controller.git",
				Branch:   "main",
				CloneDir: cloneDir,
				Depth:    1,
			})

			err := client.Clone(ctx)
			Expect(err).NotTo(HaveOccurred())

			// Directory exists
			_, err = os.Stat(cloneDir)
			Expect(err).NotTo(HaveOccurred())

			// Cleanup
			err = client.Cleanup()
			Expect(err).NotTo(HaveOccurred())

			// Directory is gone
			_, err = os.Stat(cloneDir)
			Expect(os.IsNotExist(err)).To(BeTrue())
		})

		It("should handle empty clone directory gracefully", func() {
			client := NewGitClient(GitClientConfig{
				URL:      "https://github.com/example/repo.git",
				CloneDir: "",
			})

			err := client.Cleanup()
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Describe("BuildCloneDir", func() {
		It("should construct correct path", func() {
			path := BuildCloneDir("default", "my-source", "abc12345-def6-7890-ghij-klmnopqrstuv")
			Expect(path).To(Equal("/tmp/knowledge-sources/default-my-source-abc12345"))
		})

		It("should handle short UID", func() {
			path := BuildCloneDir("ns", "name", "short")
			Expect(path).To(Equal("/tmp/knowledge-sources/ns-name-short"))
		})

		It("should truncate long UID to 8 characters", func() {
			path := BuildCloneDir("ns", "name", "12345678901234567890")
			Expect(path).To(ContainSubstring("12345678"))
			Expect(path).NotTo(ContainSubstring("123456789"))
		})
	})

	Describe("Error detection helpers", func() {
		Describe("IsRepoNotFoundError", func() {
			It("should detect repository not found errors", func() {
				Expect(IsRepoNotFoundError(nil)).To(BeFalse())
				Expect(IsRepoNotFoundError(errFromString("repository not found"))).To(BeTrue())
				Expect(IsRepoNotFoundError(errFromString("Repository Not Found"))).To(BeTrue())
				Expect(IsRepoNotFoundError(errFromString("authentication required"))).To(BeTrue())
				Expect(IsRepoNotFoundError(errFromString("some other error"))).To(BeFalse())
			})
		})

		Describe("IsAuthenticationError", func() {
			It("should detect authentication errors", func() {
				Expect(IsAuthenticationError(nil)).To(BeFalse())
				Expect(IsAuthenticationError(errFromString("authentication failed"))).To(BeTrue())
				Expect(IsAuthenticationError(errFromString("401 Unauthorized"))).To(BeTrue())
				Expect(IsAuthenticationError(errFromString("403 Forbidden"))).To(BeTrue())
				Expect(IsAuthenticationError(errFromString("invalid credentials"))).To(BeTrue())
				Expect(IsAuthenticationError(errFromString("some other error"))).To(BeFalse())
			})
		})
	})
})

// errFromString creates an error from a string for testing
type stringError string

func (e stringError) Error() string { return string(e) }

func errFromString(s string) error {
	return stringError(s)
}
