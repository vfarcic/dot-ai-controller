package controller

import (
	"context"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Test fixture branches in vfarcic/dot-ai-controller:
// - test-base (efa276d): test/git-fixtures/{file1.md, file2.md, file3.md}
// - test-after-add (3de174c): file1.md modified, file4.md added
// - test-after-delete (77fb26d): file2.md deleted
const (
	testRepoURL        = "https://github.com/vfarcic/dot-ai-controller.git"
	testBaseCommit     = "efa276dd4c2bbc1abd3d5bb2008163447a3f272e"
	testAfterAddCommit = "3de174c3e148f3a7d859308f3c4b80ab2c2cd36f"
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
			client := NewGitClient(GitClientConfig{
				URL:      testRepoURL,
				Branch:   "test-base",
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
				URL:      testRepoURL,
				Branch:   "test-base",
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
		It("should return the expected commit SHA for a fixture branch", func() {
			client := NewGitClient(GitClientConfig{
				URL:      testRepoURL,
				Branch:   "test-base",
				CloneDir: cloneDir,
				Depth:    1,
			})
			err := client.Clone(ctx)
			Expect(err).NotTo(HaveOccurred())
			defer client.Cleanup()

			sha, err := client.GetHeadCommit(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(sha).To(Equal(testBaseCommit))
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
		It("should return known fixture files on test-base", func() {
			client := NewGitClient(GitClientConfig{
				URL:      testRepoURL,
				Branch:   "test-base",
				CloneDir: cloneDir,
				Depth:    1,
			})
			err := client.Clone(ctx)
			Expect(err).NotTo(HaveOccurred())
			defer client.Cleanup()

			files, err := client.GetAllFiles(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(files).To(ContainElement("test/git-fixtures/file1.md"))
			Expect(files).To(ContainElement("test/git-fixtures/file2.md"))
			Expect(files).To(ContainElement("test/git-fixtures/file3.md"))
			Expect(files).NotTo(ContainElement("test/git-fixtures/file4.md"))
		})

		It("should include file4.md on test-after-add", func() {
			client := NewGitClient(GitClientConfig{
				URL:      testRepoURL,
				Branch:   "test-after-add",
				CloneDir: cloneDir,
				Depth:    1,
			})
			err := client.Clone(ctx)
			Expect(err).NotTo(HaveOccurred())
			defer client.Cleanup()

			files, err := client.GetAllFiles(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(files).To(ContainElement("test/git-fixtures/file1.md"))
			Expect(files).To(ContainElement("test/git-fixtures/file2.md"))
			Expect(files).To(ContainElement("test/git-fixtures/file3.md"))
			Expect(files).To(ContainElement("test/git-fixtures/file4.md"))
		})

		It("should not include file2.md on test-after-delete", func() {
			client := NewGitClient(GitClientConfig{
				URL:      testRepoURL,
				Branch:   "test-after-delete",
				CloneDir: cloneDir,
				Depth:    1,
			})
			err := client.Clone(ctx)
			Expect(err).NotTo(HaveOccurred())
			defer client.Cleanup()

			files, err := client.GetAllFiles(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(files).To(ContainElement("test/git-fixtures/file1.md"))
			Expect(files).NotTo(ContainElement("test/git-fixtures/file2.md"))
			Expect(files).To(ContainElement("test/git-fixtures/file3.md"))
			Expect(files).To(ContainElement("test/git-fixtures/file4.md"))
		})
	})

	Describe("GetFileContent", func() {
		It("should return original content on test-base", func() {
			client := NewGitClient(GitClientConfig{
				URL:      testRepoURL,
				Branch:   "test-base",
				CloneDir: cloneDir,
				Depth:    1,
			})
			err := client.Clone(ctx)
			Expect(err).NotTo(HaveOccurred())
			defer client.Cleanup()

			content, err := client.GetFileContent(ctx, "test/git-fixtures/file1.md")
			Expect(err).NotTo(HaveOccurred())
			Expect(string(content)).To(ContainSubstring("This is the first test fixture file."))
			Expect(string(content)).NotTo(ContainSubstring("now modified"))
		})

		It("should return modified content on test-after-add", func() {
			client := NewGitClient(GitClientConfig{
				URL:      testRepoURL,
				Branch:   "test-after-add",
				CloneDir: cloneDir,
				Depth:    1,
			})
			err := client.Clone(ctx)
			Expect(err).NotTo(HaveOccurred())
			defer client.Cleanup()

			content, err := client.GetFileContent(ctx, "test/git-fixtures/file1.md")
			Expect(err).NotTo(HaveOccurred())
			Expect(string(content)).To(ContainSubstring("now modified"))
		})

		It("should fail for deleted file on test-after-delete", func() {
			client := NewGitClient(GitClientConfig{
				URL:      testRepoURL,
				Branch:   "test-after-delete",
				CloneDir: cloneDir,
				Depth:    1,
			})
			err := client.Clone(ctx)
			Expect(err).NotTo(HaveOccurred())
			defer client.Cleanup()

			_, err = client.GetFileContent(ctx, "test/git-fixtures/file2.md")
			Expect(err).To(HaveOccurred())
		})

		It("should fail for non-existent file", func() {
			client := NewGitClient(GitClientConfig{
				URL:      testRepoURL,
				Branch:   "test-base",
				CloneDir: cloneDir,
				Depth:    1,
			})
			err := client.Clone(ctx)
			Expect(err).NotTo(HaveOccurred())
			defer client.Cleanup()

			_, err = client.GetFileContent(ctx, "does-not-exist.txt")
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("GetFileSize", func() {
		It("should return correct size for a known fixture file", func() {
			client := NewGitClient(GitClientConfig{
				URL:      testRepoURL,
				Branch:   "test-base",
				CloneDir: cloneDir,
				Depth:    1,
			})
			err := client.Clone(ctx)
			Expect(err).NotTo(HaveOccurred())
			defer client.Cleanup()

			size, err := client.GetFileSize(ctx, "test/git-fixtures/file1.md")
			Expect(err).NotTo(HaveOccurred())
			Expect(size).To(BeNumerically(">", 0))

			// Cross-check with content length
			content, err := client.GetFileContent(ctx, "test/git-fixtures/file1.md")
			Expect(err).NotTo(HaveOccurred())
			Expect(size).To(Equal(int64(len(content))))
		})

		It("should fail for non-existent file", func() {
			client := NewGitClient(GitClientConfig{
				URL:      testRepoURL,
				Branch:   "test-base",
				CloneDir: cloneDir,
				Depth:    1,
			})
			err := client.Clone(ctx)
			Expect(err).NotTo(HaveOccurred())
			defer client.Cleanup()

			_, err = client.GetFileSize(ctx, "does-not-exist.txt")
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("GetChangedFiles", func() {
		It("should return nil for first sync (no lastSyncedCommit)", func() {
			client := NewGitClient(GitClientConfig{
				URL:              testRepoURL,
				Branch:           "test-base",
				CloneDir:         cloneDir,
				Depth:            10,
				LastSyncedCommit: "",
			})

			err := client.Clone(ctx)
			Expect(err).NotTo(HaveOccurred())
			defer client.Cleanup()

			changes, found, err := client.GetChangedFiles(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse())
			Expect(changes).To(BeNil())
		})

		It("should return nil when lastSyncedCommit not in history", func() {
			client := NewGitClient(GitClientConfig{
				URL:              testRepoURL,
				Branch:           "test-base",
				CloneDir:         cloneDir,
				Depth:            1,
				LastSyncedCommit: "0000000000000000000000000000000000000000",
			})

			err := client.Clone(ctx)
			Expect(err).NotTo(HaveOccurred())
			defer client.Cleanup()

			changes, found, err := client.GetChangedFiles(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse())
			Expect(changes).To(BeNil())
		})

		It("should return empty changes when HEAD equals lastSyncedCommit", func() {
			client := NewGitClient(GitClientConfig{
				URL:              testRepoURL,
				Branch:           "test-base",
				CloneDir:         cloneDir,
				Depth:            10,
				LastSyncedCommit: testBaseCommit,
			})

			err := client.Clone(ctx)
			Expect(err).NotTo(HaveOccurred())
			defer client.Cleanup()

			changes, found, err := client.GetChangedFiles(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(changes.Modified).To(BeEmpty())
			Expect(changes.Deleted).To(BeEmpty())
		})

		It("should detect added and modified files", func() {
			client := NewGitClient(GitClientConfig{
				URL:              testRepoURL,
				Branch:           "test-after-add",
				CloneDir:         cloneDir,
				Depth:            10,
				LastSyncedCommit: testBaseCommit,
			})

			err := client.Clone(ctx)
			Expect(err).NotTo(HaveOccurred())
			defer client.Cleanup()

			changes, found, err := client.GetChangedFiles(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(changes.Modified).To(ConsistOf(
				"test/git-fixtures/file1.md",
				"test/git-fixtures/file4.md",
			))
			Expect(changes.Deleted).To(BeEmpty())
		})

		It("should detect deleted files", func() {
			client := NewGitClient(GitClientConfig{
				URL:              testRepoURL,
				Branch:           "test-after-delete",
				CloneDir:         cloneDir,
				Depth:            10,
				LastSyncedCommit: testAfterAddCommit,
			})

			err := client.Clone(ctx)
			Expect(err).NotTo(HaveOccurred())
			defer client.Cleanup()

			changes, found, err := client.GetChangedFiles(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(changes.Modified).To(BeEmpty())
			Expect(changes.Deleted).To(ConsistOf(
				"test/git-fixtures/file2.md",
			))
		})

		It("should detect mixed additions, modifications, and deletions", func() {
			client := NewGitClient(GitClientConfig{
				URL:              testRepoURL,
				Branch:           "test-after-delete",
				CloneDir:         cloneDir,
				Depth:            10,
				LastSyncedCommit: testBaseCommit,
			})

			err := client.Clone(ctx)
			Expect(err).NotTo(HaveOccurred())
			defer client.Cleanup()

			changes, found, err := client.GetChangedFiles(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(changes.Modified).To(ConsistOf(
				"test/git-fixtures/file1.md",
				"test/git-fixtures/file4.md",
			))
			Expect(changes.Deleted).To(ConsistOf(
				"test/git-fixtures/file2.md",
			))
		})
	})

	Describe("Cleanup", func() {
		It("should remove clone directory", func() {
			client := NewGitClient(GitClientConfig{
				URL:      testRepoURL,
				Branch:   "test-base",
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
