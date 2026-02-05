package controller

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/plumbing/transport/http"
)

const (
	// DefaultCloneDepth is used for initial syncs when no lastSyncedCommit exists
	DefaultCloneDepth = 1

	// IncrementalCloneDepth is used for subsequent syncs to increase the chance
	// of having lastSyncedCommit in history for efficient diffing.
	// For 24h sync intervals, most repos won't have more than 50 commits/day.
	IncrementalCloneDepth = 50
)

// GitClientConfig holds the configuration for creating a GitClient.
type GitClientConfig struct {
	// URL is the HTTPS URL of the Git repository
	URL string
	// Branch is the branch to clone (default: "main")
	Branch string
	// Depth is the shallow clone depth (0 means use default based on sync type)
	Depth int
	// AuthToken is the authentication token for private repositories
	AuthToken string
	// CloneDir is the directory to clone the repository into
	CloneDir string
	// LastSyncedCommit is the commit SHA from the previous sync (empty for first sync)
	LastSyncedCommit string
}

// GitClient handles Git operations for a repository.
// It uses a clone-fresh approach: clone, sync, then delete.
type GitClient struct {
	url              string
	branch           string
	depth            int
	authToken        string
	cloneDir         string
	lastSyncedCommit string
	repo             *git.Repository
}

// NewGitClient creates a new GitClient with the given configuration.
func NewGitClient(cfg GitClientConfig) *GitClient {
	branch := cfg.Branch
	if branch == "" {
		branch = "main"
	}

	// Determine clone depth based on whether this is an initial or incremental sync
	depth := cfg.Depth
	if depth == 0 {
		if cfg.LastSyncedCommit == "" {
			depth = DefaultCloneDepth
		} else {
			depth = IncrementalCloneDepth
		}
	}

	return &GitClient{
		url:              cfg.URL,
		branch:           branch,
		depth:            depth,
		authToken:        cfg.AuthToken,
		cloneDir:         cfg.CloneDir,
		lastSyncedCommit: cfg.LastSyncedCommit,
	}
}

// Clone clones the repository to the configured directory.
// This is the primary operation - always clone fresh, never reuse existing clones.
func (g *GitClient) Clone(ctx context.Context) error {
	// Ensure clean state - remove any existing directory
	if err := os.RemoveAll(g.cloneDir); err != nil {
		return fmt.Errorf("failed to clean clone directory: %w", err)
	}

	// Create parent directories if needed
	if err := os.MkdirAll(filepath.Dir(g.cloneDir), 0755); err != nil {
		return fmt.Errorf("failed to create clone directory parent: %w", err)
	}

	auth := g.getAuth()

	cloneOpts := &git.CloneOptions{
		URL:           g.url,
		Auth:          auth,
		ReferenceName: plumbing.NewBranchReferenceName(g.branch),
		SingleBranch:  true,
		Depth:         g.depth,
	}

	repo, err := git.PlainClone(g.cloneDir, cloneOpts)
	if err != nil {
		return fmt.Errorf("failed to clone repository: %w", err)
	}

	g.repo = repo
	return nil
}

// GetHeadCommit returns the SHA of the HEAD commit.
func (g *GitClient) GetHeadCommit(ctx context.Context) (string, error) {
	if g.repo == nil {
		return "", errors.New("repository not initialized, call Clone first")
	}

	head, err := g.repo.Head()
	if err != nil {
		return "", fmt.Errorf("failed to get HEAD: %w", err)
	}

	return head.Hash().String(), nil
}

// GetChangedFiles returns the list of files that changed between lastSyncedCommit and HEAD.
// If lastSyncedCommit is empty or not found in history, returns nil indicating
// that all matching files should be processed (first sync or fallback).
func (g *GitClient) GetChangedFiles(ctx context.Context) ([]string, bool, error) {
	if g.repo == nil {
		return nil, false, errors.New("repository not initialized, call Clone first")
	}

	// If no lastSyncedCommit, this is a first sync - process all files
	if g.lastSyncedCommit == "" {
		return nil, false, nil
	}

	// Get HEAD commit
	head, err := g.repo.Head()
	if err != nil {
		return nil, false, fmt.Errorf("failed to get HEAD: %w", err)
	}

	headCommit, err := g.repo.CommitObject(head.Hash())
	if err != nil {
		return nil, false, fmt.Errorf("failed to get HEAD commit: %w", err)
	}

	// Try to get lastSyncedCommit - it might not be in shallow history
	lastHash := plumbing.NewHash(g.lastSyncedCommit)
	lastCommit, err := g.repo.CommitObject(lastHash)
	if err != nil {
		// Commit not found in history (shallow clone doesn't include it)
		// Fall back to processing all files
		return nil, false, nil
	}

	// If HEAD equals lastSyncedCommit, no changes
	if head.Hash() == lastHash {
		return []string{}, true, nil
	}

	// Get the patch between commits
	patch, err := lastCommit.PatchContext(ctx, headCommit)
	if err != nil {
		// If patch fails (e.g., commits not connected), fall back to all files
		return nil, false, nil
	}

	// Extract file names from stats
	stats := patch.Stats()
	files := make([]string, 0, len(stats))
	for _, stat := range stats {
		files = append(files, stat.Name)
	}

	return files, true, nil
}

// GetAllFiles returns all files in the repository at HEAD.
// Used for first sync or when change detection falls back.
func (g *GitClient) GetAllFiles(ctx context.Context) ([]string, error) {
	if g.repo == nil {
		return nil, errors.New("repository not initialized, call Clone first")
	}

	head, err := g.repo.Head()
	if err != nil {
		return nil, fmt.Errorf("failed to get HEAD: %w", err)
	}

	commit, err := g.repo.CommitObject(head.Hash())
	if err != nil {
		return nil, fmt.Errorf("failed to get HEAD commit: %w", err)
	}

	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get tree: %w", err)
	}

	var files []string
	err = tree.Files().ForEach(func(f *object.File) error {
		files = append(files, f.Name)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to iterate files: %w", err)
	}

	return files, nil
}

// GetFileContent returns the content of a file at HEAD.
func (g *GitClient) GetFileContent(ctx context.Context, path string) ([]byte, error) {
	if g.repo == nil {
		return nil, errors.New("repository not initialized, call Clone first")
	}

	head, err := g.repo.Head()
	if err != nil {
		return nil, fmt.Errorf("failed to get HEAD: %w", err)
	}

	commit, err := g.repo.CommitObject(head.Hash())
	if err != nil {
		return nil, fmt.Errorf("failed to get commit: %w", err)
	}

	file, err := commit.File(path)
	if err != nil {
		return nil, fmt.Errorf("failed to get file %s: %w", path, err)
	}

	content, err := file.Contents()
	if err != nil {
		return nil, fmt.Errorf("failed to read file %s: %w", path, err)
	}

	return []byte(content), nil
}

// GetFileSize returns the size of a file at HEAD in bytes.
func (g *GitClient) GetFileSize(ctx context.Context, path string) (int64, error) {
	if g.repo == nil {
		return 0, errors.New("repository not initialized, call Clone first")
	}

	head, err := g.repo.Head()
	if err != nil {
		return 0, fmt.Errorf("failed to get HEAD: %w", err)
	}

	commit, err := g.repo.CommitObject(head.Hash())
	if err != nil {
		return 0, fmt.Errorf("failed to get commit: %w", err)
	}

	file, err := commit.File(path)
	if err != nil {
		return 0, fmt.Errorf("failed to get file %s: %w", path, err)
	}

	return file.Size, nil
}

// GetCloneDir returns the directory where the repository is cloned.
func (g *GitClient) GetCloneDir() string {
	return g.cloneDir
}

// Cleanup removes the clone directory and all its contents.
// This should always be called after sync operations complete.
func (g *GitClient) Cleanup() error {
	g.repo = nil
	if g.cloneDir == "" {
		return nil
	}
	return os.RemoveAll(g.cloneDir)
}

// getAuth returns the authentication method based on the configured token.
func (g *GitClient) getAuth() *http.BasicAuth {
	if g.authToken == "" {
		return nil
	}

	// For GitHub, GitLab, Bitbucket, and most Git providers,
	// use "x-access-token" as username with the token as password.
	// This works for:
	// - GitHub PAT and fine-grained tokens
	// - GitLab personal/project access tokens
	// - Bitbucket app passwords
	// - Gitea/Forgejo access tokens
	return &http.BasicAuth{
		Username: "x-access-token",
		Password: g.authToken,
	}
}

// IsRepoNotFoundError checks if the error indicates the repository was not found
// or access was denied (common for private repos without auth).
func IsRepoNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	errStr := strings.ToLower(err.Error())
	// Common patterns across git providers
	return strings.Contains(errStr, "repository not found") ||
		strings.Contains(errStr, "not found") ||
		strings.Contains(errStr, "authentication required") ||
		strings.Contains(errStr, "could not read username")
}

// IsAuthenticationError checks if the error indicates an authentication failure.
func IsAuthenticationError(err error) bool {
	if err == nil {
		return false
	}
	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "authentication") ||
		strings.Contains(errStr, "401") ||
		strings.Contains(errStr, "403") ||
		strings.Contains(errStr, "invalid credentials")
}

// BuildCloneDir constructs the clone directory path for a GitKnowledgeSource.
// Format: /tmp/knowledge-sources/<namespace>-<name>-<uid[:8]>
func BuildCloneDir(namespace, name, uid string) string {
	uidShort := uid
	if len(uid) > 8 {
		uidShort = uid[:8]
	}
	return filepath.Join("/tmp", "knowledge-sources", fmt.Sprintf("%s-%s-%s", namespace, name, uidShort))
}
