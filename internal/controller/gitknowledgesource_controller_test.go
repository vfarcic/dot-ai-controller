package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	dotaiv1alpha1 "github.com/vfarcic/dot-ai-controller/api/v1alpha1"
)

func TestBuildDocumentURI(t *testing.T) {
	tests := []struct {
		name     string
		repoURL  string
		branch   string
		filePath string
		expected string
	}{
		{
			name:     "GitHub with .git suffix",
			repoURL:  "https://github.com/acme/platform.git",
			branch:   "main",
			filePath: "docs/guide.md",
			expected: "https://github.com/acme/platform/blob/main/docs/guide.md",
		},
		{
			name:     "GitHub without .git suffix",
			repoURL:  "https://github.com/acme/platform",
			branch:   "main",
			filePath: "README.md",
			expected: "https://github.com/acme/platform/blob/main/README.md",
		},
		{
			name:     "GitLab",
			repoURL:  "https://gitlab.com/acme/platform.git",
			branch:   "develop",
			filePath: "docs/api.md",
			expected: "https://gitlab.com/acme/platform/blob/develop/docs/api.md",
		},
		{
			name:     "Bitbucket",
			repoURL:  "https://bitbucket.org/acme/platform",
			branch:   "master",
			filePath: "docs/setup.md",
			expected: "https://bitbucket.org/acme/platform/blob/master/docs/setup.md",
		},
		{
			name:     "Default branch when empty",
			repoURL:  "https://github.com/acme/platform",
			branch:   "",
			filePath: "README.md",
			expected: "https://github.com/acme/platform/blob/main/README.md",
		},
		{
			name:     "Nested file path",
			repoURL:  "https://github.com/acme/platform",
			branch:   "main",
			filePath: "docs/guides/getting-started/quick-start.md",
			expected: "https://github.com/acme/platform/blob/main/docs/guides/getting-started/quick-start.md",
		},
		{
			name:     "Self-hosted GitLab",
			repoURL:  "https://git.internal.acme.com/platform/docs",
			branch:   "main",
			filePath: "index.md",
			expected: "https://git.internal.acme.com/platform/docs/blob/main/index.md",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := BuildDocumentURI(tt.repoURL, tt.branch, tt.filePath)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGitKnowledgeSourceReconciler_GetSecretValue(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = dotaiv1alpha1.AddToScheme(scheme)

	tests := []struct {
		name        string
		secret      *corev1.Secret
		secretRef   *dotaiv1alpha1.SecretReference
		expectError bool
		errorMsg    string
		expected    string
	}{
		{
			name: "valid secret",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret",
					Namespace: "default",
				},
				Data: map[string][]byte{
					"token": []byte("my-secret-token"),
				},
			},
			secretRef: &dotaiv1alpha1.SecretReference{
				Name: "test-secret",
				Key:  "token",
			},
			expectError: false,
			expected:    "my-secret-token",
		},
		{
			name:   "secret not found",
			secret: nil,
			secretRef: &dotaiv1alpha1.SecretReference{
				Name: "nonexistent",
				Key:  "token",
			},
			expectError: true,
			errorMsg:    "not found",
		},
		{
			name: "key not found in secret",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret",
					Namespace: "default",
				},
				Data: map[string][]byte{
					"other-key": []byte("value"),
				},
			},
			secretRef: &dotaiv1alpha1.SecretReference{
				Name: "test-secret",
				Key:  "token",
			},
			expectError: true,
			errorMsg:    "does not contain key",
		},
		{
			name: "empty value",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret",
					Namespace: "default",
				},
				Data: map[string][]byte{
					"token": []byte(""),
				},
			},
			secretRef: &dotaiv1alpha1.SecretReference{
				Name: "test-secret",
				Key:  "token",
			},
			expectError: true,
			errorMsg:    "is empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var objs []runtime.Object
			if tt.secret != nil {
				objs = append(objs, tt.secret)
			}

			client := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objs...).
				Build()

			r := &GitKnowledgeSourceReconciler{
				Client: client,
				Scheme: scheme,
			}

			ctx := context.Background()
			value, err := r.getSecretValue(ctx, "default", tt.secretRef)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected, value)
			}
		})
	}
}

func TestGitKnowledgeSourceReconciler_Reconcile_MissingMCPSecret(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = dotaiv1alpha1.AddToScheme(scheme)

	gks := &dotaiv1alpha1.GitKnowledgeSource{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-gks",
			Namespace:  "default",
			UID:        types.UID("test-uid-12345678"),
			Finalizers: []string{GitKnowledgeSourceFinalizer}, // Pre-add finalizer to test sync logic
		},
		Spec: dotaiv1alpha1.GitKnowledgeSourceSpec{
			Repository: dotaiv1alpha1.RepositoryConfig{
				URL:    "https://github.com/acme/platform.git",
				Branch: "main",
			},
			Paths: []string{"docs/**/*.md"},
			McpServer: dotaiv1alpha1.McpServerConfig{
				URL: "http://mcp-server:3456",
				AuthSecretRef: dotaiv1alpha1.SecretReference{
					Name: "mcp-auth",
					Key:  "token",
				},
			},
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(gks).
		WithStatusSubresource(gks).
		Build()

	recorder := record.NewFakeRecorder(10)

	r := &GitKnowledgeSourceReconciler{
		Client:         client,
		Scheme:         scheme,
		Recorder:       recorder,
		ScheduleParser: NewScheduleParser(),
	}

	ctx := context.Background()
	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-gks",
			Namespace: "default",
		},
	}

	result, err := r.Reconcile(ctx, req)

	// Should not return error but should requeue
	require.NoError(t, err)
	assert.Equal(t, time.Minute, result.RequeueAfter)

	// Check event was recorded
	select {
	case event := <-recorder.Events:
		assert.Contains(t, event, "MCPAuthError")
	default:
		t.Error("Expected MCPAuthError event")
	}
}

func TestGitKnowledgeSourceReconciler_Reconcile_Success(t *testing.T) {
	// This test requires a mock MCP server and would need to mock the git clone
	// For now, we test the integration path with a mock MCP server

	// Create mock MCP server
	mcpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := IngestResponse{
			Success:       true,
			ChunksCreated: 2,
			ChunkIDs:      []string{"chunk-1", "chunk-2"},
			Message:       "Successfully ingested",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer mcpServer.Close()

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = dotaiv1alpha1.AddToScheme(scheme)

	mcpSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mcp-auth",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"token": []byte("test-token"),
		},
	}

	gks := &dotaiv1alpha1.GitKnowledgeSource{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-gks",
			Namespace:  "default",
			UID:        types.UID("test-uid-12345678"),
			Generation: 1,
			Finalizers: []string{GitKnowledgeSourceFinalizer}, // Pre-add finalizer to test sync logic
		},
		Spec: dotaiv1alpha1.GitKnowledgeSourceSpec{
			Repository: dotaiv1alpha1.RepositoryConfig{
				URL:    "https://github.com/vfarcic/dot-ai-controller.git",
				Branch: "main",
			},
			Paths: []string{"*.md"}, // Just match a few files
			McpServer: dotaiv1alpha1.McpServerConfig{
				URL: mcpServer.URL,
				AuthSecretRef: dotaiv1alpha1.SecretReference{
					Name: "mcp-auth",
					Key:  "token",
				},
			},
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(gks, mcpSecret).
		WithStatusSubresource(gks).
		Build()

	recorder := record.NewFakeRecorder(10)

	r := &GitKnowledgeSourceReconciler{
		Client:         client,
		Scheme:         scheme,
		Recorder:       recorder,
		ScheduleParser: NewScheduleParser(),
	}

	ctx := context.Background()
	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-gks",
			Namespace: "default",
		},
	}

	// This will actually clone the repo, which takes time
	// In a real test environment, we'd mock the git client
	result, err := r.Reconcile(ctx, req)

	// The clone should succeed since it's a public repo
	require.NoError(t, err)
	// Should have RequeueAfter set for scheduling (default @every 24h)
	assert.True(t, result.RequeueAfter > 0, "Expected RequeueAfter to be set for scheduling")
	assert.InDelta(t, 24*time.Hour, result.RequeueAfter, float64(10*time.Second))

	// Verify status was updated
	var updated dotaiv1alpha1.GitKnowledgeSource
	err = client.Get(ctx, req.NamespacedName, &updated)
	require.NoError(t, err)

	assert.True(t, updated.Status.Active)
	assert.NotNil(t, updated.Status.LastSyncTime)
	assert.NotEmpty(t, updated.Status.LastSyncedCommit)
	assert.GreaterOrEqual(t, updated.Status.DocumentCount, 0)
	// Verify NextScheduledSync is set
	assert.NotNil(t, updated.Status.NextScheduledSync, "Expected NextScheduledSync to be set")
}

func TestGitKnowledgeSourceReconciler_Reconcile_NotFound(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = dotaiv1alpha1.AddToScheme(scheme)

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	r := &GitKnowledgeSourceReconciler{
		Client:         client,
		Scheme:         scheme,
		Recorder:       record.NewFakeRecorder(10),
		ScheduleParser: NewScheduleParser(),
	}

	ctx := context.Background()
	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "nonexistent",
			Namespace: "default",
		},
	}

	result, err := r.Reconcile(ctx, req)

	// Should return empty result with no error (resource was deleted)
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
}

func TestGitKnowledgeSourceReconciler_Reconcile_InvalidSchedule(t *testing.T) {
	// Create mock MCP server
	mcpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := IngestResponse{
			Success:       true,
			ChunksCreated: 1,
			ChunkIDs:      []string{"chunk-1"},
			Message:       "Successfully ingested",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer mcpServer.Close()

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = dotaiv1alpha1.AddToScheme(scheme)

	mcpSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mcp-auth",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"token": []byte("test-token"),
		},
	}

	gks := &dotaiv1alpha1.GitKnowledgeSource{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-gks-invalid-schedule",
			Namespace:  "default",
			UID:        types.UID("test-uid-invalid"),
			Generation: 1,
			Finalizers: []string{GitKnowledgeSourceFinalizer}, // Pre-add finalizer to test sync logic
		},
		Spec: dotaiv1alpha1.GitKnowledgeSourceSpec{
			Repository: dotaiv1alpha1.RepositoryConfig{
				URL:    "https://github.com/vfarcic/dot-ai-controller.git",
				Branch: "main",
			},
			Paths:    []string{"*.md"},
			Schedule: "invalid-garbage-schedule", // Invalid schedule
			McpServer: dotaiv1alpha1.McpServerConfig{
				URL: mcpServer.URL,
				AuthSecretRef: dotaiv1alpha1.SecretReference{
					Name: "mcp-auth",
					Key:  "token",
				},
			},
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(gks, mcpSecret).
		WithStatusSubresource(gks).
		Build()

	recorder := record.NewFakeRecorder(10)

	r := &GitKnowledgeSourceReconciler{
		Client:         client,
		Scheme:         scheme,
		Recorder:       recorder,
		ScheduleParser: NewScheduleParser(),
	}

	ctx := context.Background()
	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-gks-invalid-schedule",
			Namespace: "default",
		},
	}

	result, err := r.Reconcile(ctx, req)

	// Sync should complete but no requeue (invalid schedule)
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result, "Should not requeue with invalid schedule")

	// Verify status
	var updated dotaiv1alpha1.GitKnowledgeSource
	err = client.Get(ctx, req.NamespacedName, &updated)
	require.NoError(t, err)

	// Sync should still happen
	assert.True(t, updated.Status.Active)
	assert.NotNil(t, updated.Status.LastSyncTime)
	// But NextScheduledSync should be nil
	assert.Nil(t, updated.Status.NextScheduledSync, "NextScheduledSync should be nil for invalid schedule")

	// Check ScheduleError event was recorded
	foundScheduleError := false
	for i := 0; i < 5; i++ {
		select {
		case event := <-recorder.Events:
			if strings.Contains(event, "ScheduleError") {
				foundScheduleError = true
			}
		default:
		}
	}
	assert.True(t, foundScheduleError, "Expected ScheduleError event")
}

func TestGitKnowledgeSourceReconciler_Reconcile_ConcurrentSyncPrevention(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = dotaiv1alpha1.AddToScheme(scheme)

	gks := &dotaiv1alpha1.GitKnowledgeSource{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-gks-concurrent",
			Namespace:  "default",
			UID:        types.UID("test-uid-concurrent"),
			Generation: 1,
			Finalizers: []string{GitKnowledgeSourceFinalizer}, // Pre-add finalizer to test lock logic
		},
		Spec: dotaiv1alpha1.GitKnowledgeSourceSpec{
			Repository: dotaiv1alpha1.RepositoryConfig{
				URL:    "https://github.com/acme/platform.git",
				Branch: "main",
			},
			Paths: []string{"docs/**/*.md"},
			McpServer: dotaiv1alpha1.McpServerConfig{
				URL: "http://mcp-server:3456",
				AuthSecretRef: dotaiv1alpha1.SecretReference{
					Name: "mcp-auth",
					Key:  "token",
				},
			},
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(gks).
		WithStatusSubresource(gks).
		Build()

	r := &GitKnowledgeSourceReconciler{
		Client:         client,
		Scheme:         scheme,
		Recorder:       record.NewFakeRecorder(10),
		ScheduleParser: NewScheduleParser(),
	}

	ctx := context.Background()
	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-gks-concurrent",
			Namespace: "default",
		},
	}

	// Manually acquire the lock to simulate a sync in progress
	lockKey := req.NamespacedName.String()
	lock := r.getOrCreateLock(lockKey)
	lock.Lock()
	defer lock.Unlock()

	// Now try to reconcile - should skip due to lock
	result, err := r.Reconcile(ctx, req)

	require.NoError(t, err)
	assert.Equal(t, SyncInProgressRequeueAfter, result.RequeueAfter, "Should requeue after 30s when sync is in progress")
}
