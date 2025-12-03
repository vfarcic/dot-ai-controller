// remediationpolicy_slack.go contains Slack notification types and functions
// for the RemediationPolicy controller. This file handles sending formatted
// notifications to Slack using Block Kit.
package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	dotaiv1alpha1 "github.com/vfarcic/dot-ai-controller/api/v1alpha1"
)

// SlackMessage represents the structure of a Slack webhook message
type SlackMessage struct {
	Channel     string            `json:"channel,omitempty"`
	Username    string            `json:"username,omitempty"`
	IconEmoji   string            `json:"icon_emoji,omitempty"`
	Attachments []SlackAttachment `json:"attachments,omitempty"` // Attachments with Block Kit blocks
}

// SlackBlock represents a Slack Block Kit block
type SlackBlock struct {
	Type      string              `json:"type"`
	Text      *SlackBlockText     `json:"text,omitempty"`
	Elements  []SlackBlockElement `json:"elements,omitempty"`
	Fields    []SlackBlockText    `json:"fields,omitempty"`
	Accessory interface{}         `json:"accessory,omitempty"`
}

// SlackBlockText represents text in a Block Kit block
type SlackBlockText struct {
	Type  string `json:"type"` // "plain_text" or "mrkdwn"
	Text  string `json:"text"`
	Emoji *bool  `json:"emoji,omitempty"`
}

// SlackBlockElement represents an element in a Block Kit block
type SlackBlockElement struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// SlackAttachment represents a Slack message attachment for rich formatting (legacy)
type SlackAttachment struct {
	Color     string       `json:"color,omitempty"`
	Title     string       `json:"title,omitempty"`
	Text      string       `json:"text,omitempty"`
	Fields    []SlackField `json:"fields,omitempty"`
	Footer    string       `json:"footer,omitempty"`
	Timestamp int64        `json:"ts,omitempty"`
	Blocks    []SlackBlock `json:"blocks,omitempty"`
}

// SlackField represents a field in a Slack attachment (legacy)
type SlackField struct {
	Title string `json:"title"`
	Value string `json:"value"`
	Short bool   `json:"short"`
}

// sendSlackNotification sends a notification to Slack if configured
func (r *RemediationPolicyReconciler) sendSlackNotification(ctx context.Context, policy *dotaiv1alpha1.RemediationPolicy, event *corev1.Event, notificationType string, mcpRequest *dotaiv1alpha1.McpRequest, mcpResponse *McpResponse) error {
	logger := logf.FromContext(ctx)

	// Check if Slack notifications are enabled
	if !policy.Spec.Notifications.Slack.Enabled {
		logger.V(1).Info("Slack notifications disabled, skipping")
		return nil
	}

	// Check notification type against policy configuration
	if notificationType == "start" && !policy.Spec.Notifications.Slack.NotifyOnStart {
		logger.V(1).Info("Start notifications disabled, skipping")
		return nil
	}
	if notificationType == "complete" && !policy.Spec.Notifications.Slack.NotifyOnComplete {
		logger.V(1).Info("Complete notifications disabled, skipping")
		return nil
	}

	// Resolve webhook URL from Secret or plain text
	webhookUrl, err := r.resolveWebhookUrl(
		ctx,
		policy.Namespace,
		policy.Spec.Notifications.Slack.WebhookUrl,
		policy.Spec.Notifications.Slack.WebhookUrlSecretRef,
		"Slack",
	)
	if err != nil {
		logger.Error(err, "failed to resolve Slack webhook URL")
		// Update notification health condition
		if updateErr := r.updateNotificationHealthCondition(ctx, policy, err); updateErr != nil {
			logger.Error(updateErr, "failed to update notification health condition")
		}
		return fmt.Errorf("failed to resolve Slack webhook URL: %w", err)
	}
	if webhookUrl == "" {
		logger.V(1).Info("Slack webhook URL not configured, skipping notification")
		return nil
	}

	// Create Slack message
	message := r.createSlackMessage(policy, event, notificationType, mcpRequest, mcpResponse)

	// Send the message
	if err := r.sendSlackWebhook(ctx, webhookUrl, message); err != nil {
		logger.Error(err, "failed to send Slack notification",
			"notificationType", notificationType)
		// Update notification health condition with HTTP error
		if updateErr := r.updateNotificationHealthCondition(ctx, policy, err); updateErr != nil {
			logger.Error(updateErr, "failed to update notification health condition")
		}
		return err
	}

	// Update notification health condition - success
	if err := r.updateNotificationHealthCondition(ctx, policy, nil); err != nil {
		logger.Error(err, "failed to update notification health condition")
		// Don't fail notification on status update error
	}

	logger.Info("📱 Slack notification sent successfully",
		"notificationType", notificationType,
		"channel", policy.Spec.Notifications.Slack.Channel)
	return nil
}

// createSlackMessage creates a formatted Slack message using Block Kit
func (r *RemediationPolicyReconciler) createSlackMessage(policy *dotaiv1alpha1.RemediationPolicy, event *corev1.Event, notificationType string, mcpRequest *dotaiv1alpha1.McpRequest, mcpResponse *McpResponse) SlackMessage {
	var blocks []SlackBlock
	var title, emoji, color string

	switch notificationType {
	case "start":
		emoji = "🔄"
		title = "Remediation Started"
		color = "#f2994a" // Orange vertical bar
		blocks = r.createStartBlocks(emoji, title, policy, event, mcpRequest)

	case "complete":
		if mcpResponse != nil && mcpResponse.Success {
			executed := r.getMcpExecutedStatus(mcpResponse)
			if executed {
				emoji = "✅"
				title = "Remediation Completed Successfully"
				color = "#2eb67d" // Green vertical bar (automatic execution)
			} else {
				emoji = "📋"
				title = "Analysis Completed - Manual Action Required"
				color = "#0073e6" // Blue vertical bar (manual mode)
			}
		} else {
			emoji = "❌"
			title = "Remediation Failed"
			color = "#e01e5a" // Red vertical bar
		}
		blocks = r.createCompleteBlocks(emoji, title, policy, event, mcpRequest, mcpResponse)
	}

	message := SlackMessage{
		Username:  "dot-ai-controller",
		IconEmoji: ":robot_face:",
		// Put blocks inside attachment with color for the vertical bar
		Attachments: []SlackAttachment{
			{
				Color:  color,
				Blocks: blocks,
			},
		},
	}

	// Set channel if configured
	if policy.Spec.Notifications.Slack.Channel != "" {
		message.Channel = policy.Spec.Notifications.Slack.Channel
	}

	return message
}

// createStartBlocks creates Block Kit blocks for start notifications
func (r *RemediationPolicyReconciler) createStartBlocks(emoji, title string, policy *dotaiv1alpha1.RemediationPolicy, event *corev1.Event, mcpRequest *dotaiv1alpha1.McpRequest) []SlackBlock {
	blocks := []SlackBlock{
		// Header
		{
			Type: "header",
			Text: &SlackBlockText{
				Type: "plain_text",
				Text: fmt.Sprintf("%s %s", emoji, title),
			},
		},
		// Context info
		{
			Type: "section",
			Fields: []SlackBlockText{
				{Type: "mrkdwn", Text: fmt.Sprintf("*Event Type:*\n%s/%s", event.Type, event.Reason)},
				{Type: "mrkdwn", Text: fmt.Sprintf("*Resource:*\n%s/%s", event.InvolvedObject.Kind, event.InvolvedObject.Name)},
				{Type: "mrkdwn", Text: fmt.Sprintf("*Namespace:*\n%s", event.InvolvedObject.Namespace)},
				{Type: "mrkdwn", Text: fmt.Sprintf("*Mode:*\n%s", mcpRequest.Mode)},
			},
		},
		// Issue description
		{
			Type: "section",
			Text: &SlackBlockText{
				Type: "mrkdwn",
				Text: fmt.Sprintf("*Issue:*\n%s", mcpRequest.Issue),
			},
		},
		// Divider
		{
			Type: "divider",
		},
		// Footer
		{
			Type: "context",
			Elements: []SlackBlockElement{
				{
					Type: "mrkdwn",
					Text: fmt.Sprintf("Policy: `%s` | dot-ai Kubernetes Event Controller", policy.Name),
				},
			},
		},
	}
	return blocks
}

// createCompleteBlocks creates Block Kit blocks for completion notifications
func (r *RemediationPolicyReconciler) createCompleteBlocks(emoji, title string, policy *dotaiv1alpha1.RemediationPolicy, event *corev1.Event, mcpRequest *dotaiv1alpha1.McpRequest, mcpResponse *McpResponse) []SlackBlock {
	blocks := []SlackBlock{
		// Header
		{
			Type: "header",
			Text: &SlackBlockText{
				Type: "plain_text",
				Text: fmt.Sprintf("%s %s", emoji, title),
			},
		},
	}

	// Add result message
	if mcpResponse != nil {
		var resultText string
		if mcpResponse.Success {
			resultText = mcpResponse.GetResultMessage()
		} else {
			resultText = mcpResponse.GetErrorMessage()
		}
		blocks = append(blocks, SlackBlock{
			Type: "section",
			Text: &SlackBlockText{
				Type: "mrkdwn",
				Text: fmt.Sprintf("*Result:*\n%s", resultText),
			},
		})
	}

	// Context fields
	fields := []SlackBlockText{
		{Type: "mrkdwn", Text: fmt.Sprintf("*Event Type:*\n%s/%s", event.Type, event.Reason)},
		{Type: "mrkdwn", Text: fmt.Sprintf("*Resource:*\n%s/%s", event.InvolvedObject.Kind, event.InvolvedObject.Name)},
		{Type: "mrkdwn", Text: fmt.Sprintf("*Namespace:*\n%s", event.InvolvedObject.Namespace)},
		{Type: "mrkdwn", Text: fmt.Sprintf("*Mode:*\n%s", mcpRequest.Mode)},
	}
	blocks = append(blocks, SlackBlock{
		Type:   "section",
		Fields: fields,
	})

	// Add MCP details if available
	if mcpResponse != nil {
		mcpBlocks := r.createMcpDetailBlocks(mcpResponse)
		blocks = append(blocks, mcpBlocks...)
	}

	// Original issue
	blocks = append(blocks, SlackBlock{
		Type: "section",
		Text: &SlackBlockText{
			Type: "mrkdwn",
			Text: fmt.Sprintf("*Original Issue:*\n%s", mcpRequest.Issue),
		},
	})

	// Divider
	blocks = append(blocks, SlackBlock{
		Type: "divider",
	})

	// Footer
	blocks = append(blocks, SlackBlock{
		Type: "context",
		Elements: []SlackBlockElement{
			{
				Type: "mrkdwn",
				Text: fmt.Sprintf("Policy: `%s` | dot-ai Kubernetes Event Controller", policy.Name),
			},
		},
	})

	return blocks
}

// createMcpDetailBlocks extracts detailed information from MCP response and creates Block Kit blocks
func (r *RemediationPolicyReconciler) createMcpDetailBlocks(mcpResponse *McpResponse) []SlackBlock {
	blocks := []SlackBlock{}
	executed := r.getMcpExecutedStatus(mcpResponse)

	if mcpResponse.Data != nil && mcpResponse.Data.Result != nil {
		result := mcpResponse.Data.Result

		// Execution time and confidence as fields
		var metaFields []SlackBlockText
		if mcpResponse.Data.ExecutionTime > 0 {
			executionTimeSeconds := mcpResponse.Data.ExecutionTime / 1000
			metaFields = append(metaFields, SlackBlockText{
				Type: "mrkdwn",
				Text: fmt.Sprintf("*Execution Time:*\n%.2fs", executionTimeSeconds),
			})
		}

		if confidence, ok := result["confidence"].(float64); ok {
			metaFields = append(metaFields, SlackBlockText{
				Type: "mrkdwn",
				Text: fmt.Sprintf("*Confidence:*\n%.0f%%", confidence*100),
			})
		}

		if len(metaFields) > 0 {
			blocks = append(blocks, SlackBlock{
				Type:   "section",
				Fields: metaFields,
			})
		}

		// Root cause analysis
		if analysis, ok := result["analysis"].(map[string]interface{}); ok {
			if rootCause, ok := analysis["rootCause"].(string); ok && rootCause != "" {
				blocks = append(blocks, SlackBlock{
					Type: "section",
					Text: &SlackBlockText{
						Type: "mrkdwn",
						Text: fmt.Sprintf("*Root Cause:*\n%s", rootCause),
					},
				})
			}
			if confLevel, ok := analysis["confidence"].(float64); ok {
				blocks = append(blocks, SlackBlock{
					Type: "section",
					Text: &SlackBlockText{
						Type: "mrkdwn",
						Text: fmt.Sprintf("*Analysis Confidence:* %.0f%%", confLevel*100),
					},
				})
			}
		}

		// Remediation commands - NO TRUNCATION, use code blocks
		if remediation, ok := result["remediation"].(map[string]interface{}); ok {
			if actions, ok := remediation["actions"].([]interface{}); ok && len(actions) > 0 {
				commandsTitle := "Commands Executed"
				if !executed {
					commandsTitle = "Recommended Commands"
				}

				blocks = append(blocks, SlackBlock{
					Type: "section",
					Text: &SlackBlockText{
						Type: "mrkdwn",
						Text: fmt.Sprintf("*%s:*", commandsTitle),
					},
				})

				// Add each command in its own code block - NO TRUNCATION
				for i, action := range actions {
					// Slack recommends max 50 blocks per message - enforce 10 command limit
					if i >= 10 {
						blocks = append(blocks, SlackBlock{
							Type: "section",
							Text: &SlackBlockText{
								Type: "mrkdwn",
								Text: fmt.Sprintf("_... and %d more commands_", len(actions)-10),
							},
						})
						break
					}

					if actionMap, ok := action.(map[string]interface{}); ok {
						if cmd, ok := actionMap["command"].(string); ok && cmd != "" {
							blocks = append(blocks, SlackBlock{
								Type: "section",
								Text: &SlackBlockText{
									Type: "mrkdwn",
									Text: fmt.Sprintf("```\n%s\n```", cmd), // Full command in code block
								},
							})
						}
					}
				}
			}
		}

		// Validation results
		if validation, ok := result["validation"].(map[string]interface{}); ok {
			if success, ok := validation["success"].(bool); ok {
				status := "❌ Failed"
				if success {
					status = "✅ Passed"
				}
				blocks = append(blocks, SlackBlock{
					Type: "section",
					Text: &SlackBlockText{
						Type: "mrkdwn",
						Text: fmt.Sprintf("*Validation:* %s", status),
					},
				})
			}
		}

		// Action count summary
		if results, ok := result["results"].([]interface{}); ok && len(results) > 0 {
			blocks = append(blocks, SlackBlock{
				Type: "section",
				Text: &SlackBlockText{
					Type: "mrkdwn",
					Text: fmt.Sprintf("*Actions Taken:* %d remediation actions", len(results)),
				},
			})
		}
	}

	// Add error details for failed responses
	if !mcpResponse.Success && mcpResponse.Error != nil {
		errorText := ""
		if mcpResponse.Error.Code != "" {
			errorText += fmt.Sprintf("*Error Code:* `%s`\n", mcpResponse.Error.Code)
		}
		if mcpResponse.Error.Details != nil {
			if reason, ok := mcpResponse.Error.Details["reason"].(string); ok && reason != "" {
				errorText += fmt.Sprintf("*Error Details:* %s", reason)
			}
		}
		if errorText != "" {
			blocks = append(blocks, SlackBlock{
				Type: "section",
				Text: &SlackBlockText{
					Type: "mrkdwn",
					Text: errorText,
				},
			})
		}
	}

	return blocks
}

// getMcpExecutedStatus checks if MCP actually executed commands or just provided recommendations
func (r *RemediationPolicyReconciler) getMcpExecutedStatus(mcpResponse *McpResponse) bool {
	if mcpResponse.Data != nil && mcpResponse.Data.Result != nil {
		if executed, ok := mcpResponse.Data.Result["executed"].(bool); ok {
			return executed
		}
	}
	return false
}

// sendSlackWebhook sends the actual HTTP request to Slack webhook
func (r *RemediationPolicyReconciler) sendSlackWebhook(ctx context.Context, webhookUrl string, message SlackMessage) error {
	logger := logf.FromContext(ctx)

	// Marshal message to JSON
	payload, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal Slack message: %w", err)
	}

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, "POST", webhookUrl, bytes.NewBuffer(payload))
	if err != nil {
		return fmt.Errorf("failed to create Slack webhook request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	// Send request with timeout
	response, err := r.HttpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send Slack webhook: %w", err)
	}
	defer response.Body.Close()

	// Check response
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		return fmt.Errorf("Slack webhook returned status %d: %s", response.StatusCode, string(body))
	}

	logger.V(1).Info("Slack webhook sent successfully",
		"statusCode", response.StatusCode,
		"payloadSize", len(payload))

	return nil
}
