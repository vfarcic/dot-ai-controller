package controller

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	dotaiv1alpha1 "github.com/vfarcic/dot-ai-controller/api/v1alpha1"
)

const (
	// ConditionTypeReady indicates whether the GitKnowledgeSource is ready and syncing
	ConditionTypeReady = "Ready"
	// ConditionTypeSynced indicates whether the last sync was successful
	ConditionTypeSynced = "Synced"
)

// GitKnowledgeSourceReconciler reconciles a GitKnowledgeSource object.
// It clones Git repositories, syncs matching documents to MCP, and handles cleanup.
type GitKnowledgeSourceReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=dot-ai.devopstoolkit.live,resources=gitknowledgesources,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=dot-ai.devopstoolkit.live,resources=gitknowledgesources/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=dot-ai.devopstoolkit.live,resources=gitknowledgesources/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile handles GitKnowledgeSource reconciliation.
// For each GitKnowledgeSource, it:
// 1. Clones the specified Git repository (fresh clone each time)
// 2. Filters files by path patterns (include/exclude)
// 3. Detects changed files since last sync using git diff
// 4. Syncs changed documents to MCP via manageKnowledge API
// 5. Updates status with sync results
// 6. Cleans up clone directory after sync
// 7. Schedules next sync using RequeueAfter (M5)
func (r *GitKnowledgeSourceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logf.FromContext(ctx).WithValues("gitknowledgesource", req.NamespacedName)

	// Fetch the GitKnowledgeSource instance
	var gks dotaiv1alpha1.GitKnowledgeSource
	if err := r.Get(ctx, req.NamespacedName, &gks); err != nil {
		// Not found - likely deleted, nothing to do
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	logger.Info("Reconciling GitKnowledgeSource",
		"repository", gks.Spec.Repository.URL,
		"branch", gks.Spec.Repository.Branch,
	)

	// Update observed generation
	gks.Status.ObservedGeneration = gks.Generation

	// Perform the sync
	result, err := r.doSync(ctx, &gks)

	// Update status regardless of error
	if statusErr := r.Status().Update(ctx, &gks); statusErr != nil {
		logger.Error(statusErr, "Failed to update status")
		if err == nil {
			err = statusErr
		}
	}

	return result, err
}

// doSync performs the actual sync operation.
func (r *GitKnowledgeSourceReconciler) doSync(ctx context.Context, gks *dotaiv1alpha1.GitKnowledgeSource) (ctrl.Result, error) {
	logger := logf.FromContext(ctx)

	// Mark as active
	gks.Status.Active = true

	// Get Git auth token if configured
	var gitAuthToken string
	if gks.Spec.Repository.SecretRef != nil {
		token, err := r.getSecretValue(ctx, gks.Namespace, gks.Spec.Repository.SecretRef)
		if err != nil {
			r.setErrorCondition(gks, "GitAuthError", err.Error())
			r.Recorder.Event(gks, corev1.EventTypeWarning, "GitAuthError", err.Error())
			return ctrl.Result{RequeueAfter: time.Minute}, nil // Retry later
		}
		gitAuthToken = token
	}

	// Get MCP auth token
	mcpAuthToken, err := r.getSecretValue(ctx, gks.Namespace, &gks.Spec.McpServer.AuthSecretRef)
	if err != nil {
		r.setErrorCondition(gks, "MCPAuthError", err.Error())
		r.Recorder.Event(gks, corev1.EventTypeWarning, "MCPAuthError", err.Error())
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}

	// Build clone directory path
	cloneDir := BuildCloneDir(gks.Namespace, gks.Name, string(gks.UID))

	// Create Git client
	gitClient := NewGitClient(GitClientConfig{
		URL:              gks.Spec.Repository.URL,
		Branch:           gks.Spec.Repository.Branch,
		AuthToken:        gitAuthToken,
		CloneDir:         cloneDir,
		LastSyncedCommit: gks.Status.LastSyncedCommit,
	})

	// Always cleanup clone directory when done
	defer func() {
		if cleanupErr := gitClient.Cleanup(); cleanupErr != nil {
			logger.Error(cleanupErr, "Failed to cleanup clone directory")
		}
	}()

	// Clone repository
	logger.Info("Cloning repository", "url", gks.Spec.Repository.URL, "branch", gks.Spec.Repository.Branch)
	if err := gitClient.Clone(ctx); err != nil {
		errMsg := fmt.Sprintf("Failed to clone repository: %v", err)
		r.setErrorCondition(gks, "CloneError", errMsg)
		r.Recorder.Event(gks, corev1.EventTypeWarning, "CloneError", errMsg)

		if IsAuthenticationError(err) {
			gks.Status.LastError = "Authentication failed - check credentials"
		} else if IsRepoNotFoundError(err) {
			gks.Status.LastError = "Repository not found or access denied"
		} else {
			gks.Status.LastError = err.Error()
		}

		return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
	}

	// Get HEAD commit SHA
	headCommit, err := gitClient.GetHeadCommit(ctx)
	if err != nil {
		errMsg := fmt.Sprintf("Failed to get HEAD commit: %v", err)
		r.setErrorCondition(gks, "GitError", errMsg)
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}
	logger.Info("Got HEAD commit", "commit", headCommit)

	// Determine which files to process
	var filesToProcess []string

	// For M3, we always do a full sync (no change detection yet - that's M4)
	// Get all files and filter by patterns
	allFiles, err := gitClient.GetAllFiles(ctx)
	if err != nil {
		errMsg := fmt.Sprintf("Failed to list files: %v", err)
		r.setErrorCondition(gks, "GitError", errMsg)
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}

	// Filter by patterns
	matcher := NewPatternMatcher(gks.Spec.Paths, gks.Spec.Exclude)
	filesToProcess = matcher.FilterFiles(allFiles)
	logger.Info("Filtered files", "total", len(allFiles), "matching", len(filesToProcess))

	// Create MCP client
	mcpEndpoint := strings.TrimSuffix(gks.Spec.McpServer.URL, "/") + "/api/v1/tools/manageKnowledge"
	mcpClient := NewMCPKnowledgeClient(MCPKnowledgeClientConfig{
		Endpoint:  mcpEndpoint,
		AuthToken: mcpAuthToken,
	})

	// Sync documents to MCP
	var syncErrors int
	var documentCount int
	var lastError string

	for _, filePath := range filesToProcess {
		// Read file content
		content, err := gitClient.GetFileContent(ctx, filePath)
		if err != nil {
			logger.Error(err, "Failed to read file", "path", filePath)
			syncErrors++
			lastError = fmt.Sprintf("Failed to read %s: %v", filePath, err)
			continue
		}

		// Build document URI
		uri := BuildDocumentURI(gks.Spec.Repository.URL, gks.Spec.Repository.Branch, filePath)

		// Ingest document
		resp, err := mcpClient.IngestDocument(ctx, uri, string(content), gks.Spec.Metadata)
		if err != nil {
			logger.Error(err, "Failed to ingest document", "path", filePath, "uri", uri)
			syncErrors++
			lastError = fmt.Sprintf("Failed to ingest %s: %v", filePath, err)
			continue
		}

		logger.V(1).Info("Ingested document",
			"path", filePath,
			"uri", uri,
			"chunks", resp.ChunksCreated,
		)
		documentCount++
	}

	// Update status
	now := metav1.Now()
	gks.Status.LastSyncTime = &now
	gks.Status.LastSyncedCommit = headCommit
	gks.Status.DocumentCount = documentCount
	gks.Status.SyncErrors = syncErrors
	gks.Status.LastError = lastError

	// Set condition based on results
	if syncErrors == 0 {
		r.setSyncedCondition(gks, true, "SyncComplete",
			fmt.Sprintf("Successfully synced %d documents", documentCount))
		r.Recorder.Eventf(gks, corev1.EventTypeNormal, "SyncComplete",
			"Synced %d documents from %s", documentCount, gks.Spec.Repository.URL)
	} else {
		r.setSyncedCondition(gks, false, "SyncPartial",
			fmt.Sprintf("Synced %d documents with %d errors", documentCount, syncErrors))
		r.Recorder.Eventf(gks, corev1.EventTypeWarning, "SyncPartial",
			"Synced %d documents with %d errors", documentCount, syncErrors)
	}

	// Set Ready condition
	meta.SetStatusCondition(&gks.Status.Conditions, metav1.Condition{
		Type:               ConditionTypeReady,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: gks.Generation,
		Reason:             "Active",
		Message:            "GitKnowledgeSource is active and syncing",
	})

	logger.Info("Sync complete",
		"documents", documentCount,
		"errors", syncErrors,
		"commit", headCommit,
	)

	// No requeue for now (scheduling is M5)
	return ctrl.Result{}, nil
}

// getSecretValue retrieves a value from a Kubernetes Secret.
func (r *GitKnowledgeSourceReconciler) getSecretValue(ctx context.Context, namespace string, ref *dotaiv1alpha1.SecretReference) (string, error) {
	secret := &corev1.Secret{}
	secretKey := client.ObjectKey{
		Namespace: namespace,
		Name:      ref.Name,
	}

	if err := r.Get(ctx, secretKey, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return "", fmt.Errorf("Secret '%s' not found in namespace '%s'", ref.Name, namespace)
		}
		return "", fmt.Errorf("failed to fetch Secret: %w", err)
	}

	value, exists := secret.Data[ref.Key]
	if !exists {
		return "", fmt.Errorf("Secret '%s' does not contain key '%s'", ref.Name, ref.Key)
	}

	if len(value) == 0 {
		return "", fmt.Errorf("Secret '%s' key '%s' is empty", ref.Name, ref.Key)
	}

	return string(value), nil
}

// setErrorCondition sets the Synced condition to False with an error.
func (r *GitKnowledgeSourceReconciler) setErrorCondition(gks *dotaiv1alpha1.GitKnowledgeSource, reason, message string) {
	meta.SetStatusCondition(&gks.Status.Conditions, metav1.Condition{
		Type:               ConditionTypeSynced,
		Status:             metav1.ConditionFalse,
		ObservedGeneration: gks.Generation,
		Reason:             reason,
		Message:            message,
	})
	gks.Status.LastError = message
}

// setSyncedCondition sets the Synced condition.
func (r *GitKnowledgeSourceReconciler) setSyncedCondition(gks *dotaiv1alpha1.GitKnowledgeSource, success bool, reason, message string) {
	status := metav1.ConditionFalse
	if success {
		status = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&gks.Status.Conditions, metav1.Condition{
		Type:               ConditionTypeSynced,
		Status:             status,
		ObservedGeneration: gks.Generation,
		Reason:             reason,
		Message:            message,
	})
}

// BuildDocumentURI constructs the blob URL for a file in a Git repository.
// Example: https://github.com/acme/platform/blob/main/docs/guide.md
func BuildDocumentURI(repoURL, branch, filePath string) string {
	// Parse the repo URL to extract components
	// Support formats:
	//   https://github.com/owner/repo.git
	//   https://github.com/owner/repo
	//   https://gitlab.com/owner/repo.git

	repoURL = strings.TrimSuffix(repoURL, ".git")

	// Default branch if not specified
	if branch == "" {
		branch = "main"
	}

	// Parse URL to ensure it's valid and get host
	parsed, err := url.Parse(repoURL)
	if err != nil {
		// Fallback: just append /blob/branch/path
		return repoURL + "/blob/" + branch + "/" + filePath
	}

	// Build the blob URL based on host
	// GitHub, GitLab, Bitbucket all use /blob/ pattern for file views
	return fmt.Sprintf("%s://%s%s/blob/%s/%s",
		parsed.Scheme,
		parsed.Host,
		parsed.Path,
		branch,
		filePath,
	)
}

// SetupWithManager sets up the controller with the Manager.
func (r *GitKnowledgeSourceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&dotaiv1alpha1.GitKnowledgeSource{}).
		Named("gitknowledgesource").
		Complete(r)
}
