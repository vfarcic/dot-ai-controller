package controller

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	dotaiv1alpha1 "github.com/vfarcic/dot-ai-controller/api/v1alpha1"
)

const (
	// ConditionTypeReady indicates whether the GitKnowledgeSource is ready and syncing
	ConditionTypeReady = "Ready"
	// ConditionTypeSynced indicates whether the last sync was successful
	ConditionTypeSynced = "Synced"
	// ConditionTypeScheduled indicates whether scheduling is configured correctly
	ConditionTypeScheduled = "Scheduled"

	// SyncTimeout is the maximum duration for a sync operation
	SyncTimeout = 30 * time.Minute
	// SyncInProgressRequeueAfter is how long to wait before retrying when sync is in progress
	SyncInProgressRequeueAfter = 30 * time.Second

	// GitKnowledgeSourceFinalizer is the finalizer used for cleanup on deletion
	GitKnowledgeSourceFinalizer = "dot-ai.devopstoolkit.live/finalizer"
)

// syncLocks tracks active syncs per GitKnowledgeSource to prevent concurrent syncs
var syncLocks sync.Map // map[string]*sync.Mutex

// GitKnowledgeSourceReconciler reconciles a GitKnowledgeSource object.
// It clones Git repositories, syncs matching documents to MCP, and handles cleanup.
type GitKnowledgeSourceReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	Recorder       record.EventRecorder
	ScheduleParser *ScheduleParser
}

// +kubebuilder:rbac:groups=dot-ai.devopstoolkit.live,resources=gitknowledgesources,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=dot-ai.devopstoolkit.live,resources=gitknowledgesources/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=dot-ai.devopstoolkit.live,resources=gitknowledgesources/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile handles GitKnowledgeSource reconciliation.
// For each GitKnowledgeSource, it:
// 1. Acquires lock to prevent concurrent syncs on same CR
// 2. Clones the specified Git repository (fresh clone each time)
// 3. Filters files by path patterns (include/exclude)
// 4. Detects changed files since last sync using git diff (full sync if spec changed)
// 5. Syncs changed documents to MCP via manageKnowledge API
// 6. Updates status with sync results
// 7. Cleans up clone directory after sync
// 8. Schedules next sync using RequeueAfter
func (r *GitKnowledgeSourceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logf.FromContext(ctx).WithValues("gitknowledgesource", req.NamespacedName)

	// Fetch the GitKnowledgeSource instance
	var gks dotaiv1alpha1.GitKnowledgeSource
	if err := r.Get(ctx, req.NamespacedName, &gks); err != nil {
		// Not found - likely deleted, nothing to do
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Handle deletion
	if !gks.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&gks, GitKnowledgeSourceFinalizer) {
			// Perform cleanup based on deletion policy
			if err := r.handleDeletion(ctx, &gks); err != nil {
				logger.Error(err, "Failed to handle deletion")
				return ctrl.Result{}, err
			}

			// Remove finalizer
			controllerutil.RemoveFinalizer(&gks, GitKnowledgeSourceFinalizer)
			if err := r.Update(ctx, &gks); err != nil {
				logger.Error(err, "Failed to remove finalizer")
				return ctrl.Result{}, err
			}

			// Clean up sync lock to prevent memory leak
			syncLocks.Delete(req.NamespacedName.String())

			logger.Info("Finalizer removed, deletion complete")
		}
		return ctrl.Result{}, nil
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(&gks, GitKnowledgeSourceFinalizer) {
		controllerutil.AddFinalizer(&gks, GitKnowledgeSourceFinalizer)
		if err := r.Update(ctx, &gks); err != nil {
			logger.Error(err, "Failed to add finalizer")
			return ctrl.Result{}, err
		}
		logger.Info("Finalizer added")
		// Requeue to continue with sync
		return ctrl.Result{Requeue: true}, nil
	}

	// Try to acquire lock for this CR to prevent concurrent syncs
	lockKey := req.NamespacedName.String()
	lock := r.getOrCreateLock(lockKey)
	if !lock.TryLock() {
		logger.Info("Sync already in progress, skipping")
		return ctrl.Result{RequeueAfter: SyncInProgressRequeueAfter}, nil
	}
	defer lock.Unlock()

	logger.Info("Reconciling GitKnowledgeSource",
		"repository", gks.Spec.Repository.URL,
		"branch", gks.Spec.Repository.Branch,
	)

	// Check if spec changed (for full sync decision)
	specChanged := gks.Generation != gks.Status.ObservedGeneration

	// Update observed generation before sync
	gks.Status.ObservedGeneration = gks.Generation

	// Set phase to Syncing and persist immediately so users see progress
	gks.Status.Phase = "Syncing"
	if statusErr := r.Status().Update(ctx, &gks); statusErr != nil {
		logger.Error(statusErr, "Failed to update syncing status")
		return ctrl.Result{}, statusErr
	}

	// Apply sync timeout
	syncCtx, cancel := context.WithTimeout(ctx, SyncTimeout)
	defer cancel()

	// Perform the sync
	result, err := r.doSync(syncCtx, &gks, specChanged)

	// Check for timeout
	if syncCtx.Err() == context.DeadlineExceeded {
		logger.Error(syncCtx.Err(), "Sync timed out")
		r.setErrorCondition(&gks, "SyncTimeout", "Sync operation timed out after 30 minutes")
		r.Recorder.Event(&gks, corev1.EventTypeWarning, "SyncTimeout", "Sync operation timed out")
	}

	// Update status regardless of error
	if statusErr := r.Status().Update(ctx, &gks); statusErr != nil {
		logger.Error(statusErr, "Failed to update status")
		if err == nil {
			err = statusErr
		}
	}

	return result, err
}

// getOrCreateLock returns the mutex for a given key, creating one if needed.
func (r *GitKnowledgeSourceReconciler) getOrCreateLock(key string) *sync.Mutex {
	lock, _ := syncLocks.LoadOrStore(key, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

// handleDeletion handles cleanup when a GitKnowledgeSource is being deleted.
// Based on the deletionPolicy, it either deletes documents from MCP or retains them.
func (r *GitKnowledgeSourceReconciler) handleDeletion(ctx context.Context, gks *dotaiv1alpha1.GitKnowledgeSource) error {
	logger := logf.FromContext(ctx)

	// Check deletion policy - default to Delete if not specified
	policy := gks.Spec.DeletionPolicy
	if policy == "" {
		policy = dotaiv1alpha1.DeletionPolicyDelete
	}

	if policy == dotaiv1alpha1.DeletionPolicyRetain {
		logger.Info("DeletionPolicy is Retain, documents will remain in MCP knowledge base",
			"sourceIdentifier", fmt.Sprintf("%s/%s", gks.Namespace, gks.Name),
		)
		r.Recorder.Event(gks, corev1.EventTypeNormal, "DocumentsRetained",
			"Documents retained in MCP knowledge base per deletionPolicy")
		return nil
	}

	// Policy is Delete - remove documents from MCP
	logger.Info("DeletionPolicy is Delete, removing documents from MCP",
		"sourceIdentifier", fmt.Sprintf("%s/%s", gks.Namespace, gks.Name),
	)

	// Get MCP auth token
	mcpAuthToken, err := r.getSecretValue(ctx, gks.Namespace, &gks.Spec.McpServer.AuthSecretRef)
	if err != nil {
		// If secret is not found, we can't delete from MCP but should still allow CR deletion
		logger.Error(err, "Failed to get MCP auth token, cannot delete documents from MCP")
		r.Recorder.Eventf(gks, corev1.EventTypeWarning, "CleanupWarning",
			"Could not delete documents from MCP: %v", err)
		return nil // Don't block deletion
	}

	// Build the delete URL
	sourceIdentifier := fmt.Sprintf("%s/%s", gks.Namespace, gks.Name)
	baseURL := strings.TrimSuffix(gks.Spec.McpServer.URL, "/")
	deleteURL := fmt.Sprintf("%s/api/v1/knowledge/source/%s", baseURL, url.PathEscape(sourceIdentifier))

	// Create MCP client and delete documents
	mcpClient := NewMCPKnowledgeClient(MCPKnowledgeClientConfig{
		Endpoint:  deleteURL, // Not used by DeleteBySource but needed for client creation
		AuthToken: mcpAuthToken,
	})

	resp, err := mcpClient.DeleteBySource(ctx, deleteURL)
	if err != nil {
		logger.Error(err, "Failed to delete documents from MCP")
		r.Recorder.Eventf(gks, corev1.EventTypeWarning, "CleanupWarning",
			"Could not delete documents from MCP: %v", err)
		// Don't block CR deletion on MCP errors
		return nil
	}

	chunksDeleted := 0
	if resp.Data != nil {
		chunksDeleted = resp.Data.ChunksDeleted
	}
	logger.Info("Documents deleted from MCP", "chunksDeleted", chunksDeleted)
	r.Recorder.Eventf(gks, corev1.EventTypeNormal, "DocumentsDeleted",
		"Deleted %d chunks from MCP knowledge base", chunksDeleted)

	return nil
}

// doSync performs the actual sync operation.
// specChanged indicates if the CR spec was modified (triggers full sync).
func (r *GitKnowledgeSourceReconciler) doSync(ctx context.Context, gks *dotaiv1alpha1.GitKnowledgeSource, specChanged bool) (ctrl.Result, error) {
	logger := logf.FromContext(ctx)

	// Mark as active
	gks.Status.Active = true

	// Validate schedule early - if invalid, we still sync but don't schedule
	schedule := gks.Spec.Schedule
	if schedule == "" {
		schedule = DefaultSchedule
	}
	scheduleValid := true
	if r.ScheduleParser != nil {
		if err := r.ScheduleParser.ValidateSchedule(schedule); err != nil {
			logger.Error(err, "Invalid schedule configured")
			r.setScheduleErrorCondition(gks, err.Error())
			r.Recorder.Eventf(gks, corev1.EventTypeWarning, "ScheduleError", "Invalid schedule: %v", err)
			scheduleValid = false
		}
	}

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

	// Filter by patterns
	matcher := NewPatternMatcher(gks.Spec.Paths, gks.Spec.Exclude)

	// M4: Change detection - only sync files that changed since last sync
	// M5: Force full sync if spec changed (e.g., paths filter modified)
	if gks.Status.LastSyncedCommit != "" && !specChanged {
		changedFiles, foundInHistory, err := gitClient.GetChangedFiles(ctx)
		if err != nil {
			logger.Error(err, "Failed to get changed files, falling back to full sync")
		} else if foundInHistory {
			// Only process changed files that match patterns
			filesToProcess = matcher.FilterFiles(changedFiles)
			logger.Info("Incremental sync", "changedFiles", len(changedFiles), "matching", len(filesToProcess))
			goto processFiles
		}
		// Fall through to full sync if commit not in history
	} else if specChanged {
		logger.Info("Spec changed, performing full sync")
	}

	// Full sync: first sync or fallback
	{
		allFiles, err := gitClient.GetAllFiles(ctx)
		if err != nil {
			errMsg := fmt.Sprintf("Failed to list files: %v", err)
			r.setErrorCondition(gks, "GitError", errMsg)
			return ctrl.Result{RequeueAfter: time.Minute}, nil
		}
		filesToProcess = matcher.FilterFiles(allFiles)
		logger.Info("Full sync", "total", len(allFiles), "matching", len(filesToProcess))
	}

processFiles:

	// Create MCP client
	mcpEndpoint := strings.TrimSuffix(gks.Spec.McpServer.URL, "/") + "/api/v1/tools/manageKnowledge"
	mcpClient := NewMCPKnowledgeClient(MCPKnowledgeClientConfig{
		Endpoint:  mcpEndpoint,
		AuthToken: mcpAuthToken,
	})

	// M7: Build metadata with sourceIdentifier for MCP bulk operations
	metadata := make(map[string]string)
	for k, v := range gks.Spec.Metadata {
		metadata[k] = v
	}
	metadata["sourceIdentifier"] = fmt.Sprintf("%s/%s", gks.Namespace, gks.Name)

	// Sync documents to MCP
	var syncErrors int
	var documentCount int
	var lastError string
	var skippedFiles []dotaiv1alpha1.SkippedFile

	for _, filePath := range filesToProcess {
		// Read file content
		content, err := gitClient.GetFileContent(ctx, filePath)
		if err != nil {
			logger.Error(err, "Failed to read file", "path", filePath)
			syncErrors++
			lastError = fmt.Sprintf("Failed to read %s: %v", filePath, err)
			continue
		}

		// M6: Skip Tracking - check file size against limit
		if gks.Spec.MaxFileSizeBytes != nil && int64(len(content)) > *gks.Spec.MaxFileSizeBytes {
			logger.Info("Skipping file due to size limit",
				"path", filePath,
				"size", len(content),
				"limit", *gks.Spec.MaxFileSizeBytes,
			)
			skippedFiles = append(skippedFiles, dotaiv1alpha1.SkippedFile{
				Path:   filePath,
				Reason: fmt.Sprintf("exceeded max file size (%d bytes > %d bytes)", len(content), *gks.Spec.MaxFileSizeBytes),
			})
			continue
		}

		// Build document URI
		uri := BuildDocumentURI(gks.Spec.Repository.URL, gks.Spec.Repository.Branch, filePath)

		// Ingest document
		resp, err := mcpClient.IngestDocument(ctx, uri, string(content), metadata)
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
	gks.Status.DocumentCount += documentCount // Increment with files processed in this sync
	gks.Status.SyncErrors = syncErrors
	gks.Status.LastError = lastError
	// Only update skipped tracking if files were processed (avoid resetting on incremental syncs with no changes)
	if len(filesToProcess) > 0 {
		gks.Status.SkippedDocuments = len(skippedFiles)
		gks.Status.SkippedFiles = skippedFiles
	}

	// Set condition and phase based on results
	if syncErrors == 0 {
		gks.Status.Phase = "Synced"
		r.setSyncedCondition(gks, true, "SyncComplete",
			fmt.Sprintf("Successfully synced %d documents", documentCount))
		r.Recorder.Eventf(gks, corev1.EventTypeNormal, "SyncComplete",
			"Synced %d documents from %s", documentCount, gks.Spec.Repository.URL)
	} else {
		gks.Status.Phase = "Error"
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

	// Calculate next scheduled sync
	if scheduleValid && r.ScheduleParser != nil {
		duration, nextTime, err := r.ScheduleParser.NextSyncDuration(schedule, now.Time)
		if err != nil {
			// This shouldn't happen since we validated earlier, but handle gracefully
			logger.Error(err, "Failed to calculate next sync time")
			gks.Status.NextScheduledSync = nil
			return ctrl.Result{}, nil
		}

		gks.Status.NextScheduledSync = &metav1.Time{Time: nextTime}
		r.setScheduledCondition(gks, true, "Scheduled",
			fmt.Sprintf("Next sync scheduled for %s", nextTime.Format(time.RFC3339)))

		logger.Info("Scheduled next sync",
			"schedule", schedule,
			"nextSync", nextTime.Format(time.RFC3339),
			"duration", duration.String(),
		)

		return ctrl.Result{RequeueAfter: duration}, nil
	}

	// Invalid schedule - don't requeue, user must fix CR
	gks.Status.NextScheduledSync = nil
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
	gks.Status.Phase = "Error"
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

// setScheduledCondition sets the Scheduled condition.
func (r *GitKnowledgeSourceReconciler) setScheduledCondition(gks *dotaiv1alpha1.GitKnowledgeSource, success bool, reason, message string) {
	status := metav1.ConditionFalse
	if success {
		status = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&gks.Status.Conditions, metav1.Condition{
		Type:               ConditionTypeScheduled,
		Status:             status,
		ObservedGeneration: gks.Generation,
		Reason:             reason,
		Message:            message,
	})
}

// setScheduleErrorCondition sets the Scheduled condition to False with an error.
func (r *GitKnowledgeSourceReconciler) setScheduleErrorCondition(gks *dotaiv1alpha1.GitKnowledgeSource, message string) {
	meta.SetStatusCondition(&gks.Status.Conditions, metav1.Condition{
		Type:               ConditionTypeScheduled,
		Status:             metav1.ConditionFalse,
		ObservedGeneration: gks.Generation,
		Reason:             "ScheduleError",
		Message:            message,
	})
	gks.Status.NextScheduledSync = nil
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
