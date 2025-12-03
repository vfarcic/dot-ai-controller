// remediationpolicy_notifications.go contains shared notification helpers
// for the RemediationPolicy controller. This file provides common functionality
// used by both Slack and Google Chat notification implementations.
package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	dotaiv1alpha1 "github.com/vfarcic/dot-ai-controller/api/v1alpha1"
)

// validateSlackConfiguration validates Slack notification settings
func (r *RemediationPolicyReconciler) validateSlackConfiguration(policy *dotaiv1alpha1.RemediationPolicy) error {
	slack := policy.Spec.Notifications.Slack

	// If Slack is disabled, no validation needed
	if !slack.Enabled {
		return nil
	}

	// If enabled, either webhook URL or Secret reference is required
	if slack.WebhookUrl == "" && slack.WebhookUrlSecretRef == nil {
		return fmt.Errorf("Slack webhook URL or webhookUrlSecretRef is required when notifications are enabled")
	}

	// Validate Secret reference if provided
	if slack.WebhookUrlSecretRef != nil {
		if slack.WebhookUrlSecretRef.Name == "" {
			return fmt.Errorf("Slack webhookUrlSecretRef.name cannot be empty")
		}
		if slack.WebhookUrlSecretRef.Key == "" {
			return fmt.Errorf("Slack webhookUrlSecretRef.key cannot be empty")
		}
	}

	// Basic URL format validation for plain text (deprecated)
	if slack.WebhookUrl != "" {
		if !strings.HasPrefix(slack.WebhookUrl, "https://hooks.slack.com/") {
			return fmt.Errorf("invalid Slack webhook URL format - must start with https://hooks.slack.com/")
		}
	}

	return nil
}

// validateGoogleChatConfiguration validates Google Chat notification settings
func (r *RemediationPolicyReconciler) validateGoogleChatConfiguration(policy *dotaiv1alpha1.RemediationPolicy) error {
	googleChat := policy.Spec.Notifications.GoogleChat

	// If Google Chat is disabled, no validation needed
	if !googleChat.Enabled {
		return nil
	}

	// If enabled, either webhook URL or Secret reference is required
	if googleChat.WebhookUrl == "" && googleChat.WebhookUrlSecretRef == nil {
		return fmt.Errorf("Google Chat webhook URL or webhookUrlSecretRef is required when notifications are enabled")
	}

	// Validate Secret reference if provided
	if googleChat.WebhookUrlSecretRef != nil {
		if googleChat.WebhookUrlSecretRef.Name == "" {
			return fmt.Errorf("Google Chat webhookUrlSecretRef.name cannot be empty")
		}
		if googleChat.WebhookUrlSecretRef.Key == "" {
			return fmt.Errorf("Google Chat webhookUrlSecretRef.key cannot be empty")
		}
	}

	// Basic URL format validation for plain text (deprecated)
	if googleChat.WebhookUrl != "" {
		if !strings.HasPrefix(googleChat.WebhookUrl, "https://chat.googleapis.com/") {
			return fmt.Errorf("invalid Google Chat webhook URL format - must start with https://chat.googleapis.com/")
		}
	}

	return nil
}

// resolveWebhookUrl resolves a webhook URL from either a Secret reference or plain text
// Preference order:
// 1. If both provided: log warning and prefer Secret reference
// 2. If Secret reference provided: resolve from Secret
// 3. If plain URL provided: log deprecation warning and return it
// 4. If neither provided: return error (configuration error)
func (r *RemediationPolicyReconciler) resolveWebhookUrl(
	ctx context.Context,
	namespace string,
	plainUrl string,
	secretRef *dotaiv1alpha1.SecretReference,
	serviceType string,
) (string, error) {
	logger := logf.FromContext(ctx)

	// Case 1: Both provided - warn and prefer Secret reference
	if plainUrl != "" && secretRef != nil {
		logger.Info("⚠️ Both webhook URL and Secret reference provided - using Secret reference",
			"service", serviceType,
			"namespace", namespace,
			"secretName", secretRef.Name)
	}

	// Case 2: Secret reference provided
	if secretRef != nil {
		// Fetch the Secret
		secret := &corev1.Secret{}
		secretKey := client.ObjectKey{
			Namespace: namespace,
			Name:      secretRef.Name,
		}

		if err := r.Get(ctx, secretKey, secret); err != nil {
			if apierrors.IsNotFound(err) {
				return "", fmt.Errorf("%s webhook Secret '%s' not found in namespace '%s'",
					serviceType, secretRef.Name, namespace)
			}
			return "", fmt.Errorf("failed to fetch %s webhook Secret: %w", serviceType, err)
		}

		// Extract the key from Secret data
		webhookUrlBytes, exists := secret.Data[secretRef.Key]
		if !exists {
			return "", fmt.Errorf("%s webhook Secret '%s' does not contain key '%s'",
				serviceType, secretRef.Name, secretRef.Key)
		}

		if len(webhookUrlBytes) == 0 {
			return "", fmt.Errorf("%s webhook Secret '%s' key '%s' is empty",
				serviceType, secretRef.Name, secretRef.Key)
		}

		webhookUrl := string(webhookUrlBytes)

		logger.V(1).Info("✅ Resolved webhook URL from Secret",
			"service", serviceType,
			"secretName", secretRef.Name,
			"secretKey", secretRef.Key)

		return webhookUrl, nil
	}

	// Case 3: Plain URL provided - deprecation warning
	if plainUrl != "" {
		logger.Info("⚠️ Using deprecated plain text webhook URL",
			"service", serviceType,
			"namespace", namespace,
			"recommendation", "Migrate to webhookUrlSecretRef for better security")
		return plainUrl, nil
	}

	// Case 4: Neither provided - configuration error
	return "", fmt.Errorf("%s notifications enabled but no webhook URL configured (provide either webhookUrl or webhookUrlSecretRef)", serviceType)
}

// updateNotificationHealthCondition updates the NotificationsHealthy condition based on notification errors
func (r *RemediationPolicyReconciler) updateNotificationHealthCondition(ctx context.Context, policy *dotaiv1alpha1.RemediationPolicy, notificationError error) error {
	// Fetch fresh copy to avoid conflicts
	fresh := &dotaiv1alpha1.RemediationPolicy{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(policy), fresh); err != nil {
		return fmt.Errorf("failed to fetch fresh policy: %w", err)
	}

	now := metav1.NewTime(time.Now())
	var healthCondition metav1.Condition

	if notificationError != nil {
		// Notification failed - mark as unhealthy
		healthCondition = metav1.Condition{
			Type:               "NotificationsHealthy",
			Status:             metav1.ConditionFalse,
			LastTransitionTime: now,
			Reason:             "NotificationConfigurationError",
			Message:            notificationError.Error(),
		}
	} else {
		// Notifications working - mark as healthy
		healthCondition = metav1.Condition{
			Type:               "NotificationsHealthy",
			Status:             metav1.ConditionTrue,
			LastTransitionTime: now,
			Reason:             "NotificationsWorking",
			Message:            "Notifications are configured correctly",
		}
	}

	// Find and update existing condition or append new one
	updated := false
	for i, condition := range fresh.Status.Conditions {
		if condition.Type == "NotificationsHealthy" {
			// Only update if status changed (to preserve LastTransitionTime)
			if condition.Status != healthCondition.Status {
				fresh.Status.Conditions[i] = healthCondition
				updated = true
			} else {
				// Update message but keep transition time
				fresh.Status.Conditions[i].Message = healthCondition.Message
				updated = true
			}
			break
		}
	}
	if !updated {
		fresh.Status.Conditions = append(fresh.Status.Conditions, healthCondition)
	}

	// Update status subresource
	if err := r.Status().Update(ctx, fresh); err != nil {
		return fmt.Errorf("failed to update notification health condition: %w", err)
	}

	return nil
}
