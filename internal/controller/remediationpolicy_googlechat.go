// remediationpolicy_googlechat.go contains Google Chat notification types and functions
// for the RemediationPolicy controller. This file handles sending formatted
// notifications to Google Chat using Card v2 API.
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

// GoogleChatMessage represents the structure of a Google Chat webhook message using Card v2 API
type GoogleChatMessage struct {
	CardsV2 []GoogleChatCardV2 `json:"cardsV2,omitempty"`
}

// GoogleChatCardV2 represents a Card v2 structure
type GoogleChatCardV2 struct {
	CardId string         `json:"cardId,omitempty"`
	Card   GoogleChatCard `json:"card,omitempty"`
}

// GoogleChatCard represents the card content
type GoogleChatCard struct {
	Header   *GoogleChatCardHeader `json:"header,omitempty"`
	Sections []GoogleChatSection   `json:"sections,omitempty"`
}

// GoogleChatCardHeader represents the card header
type GoogleChatCardHeader struct {
	Title     string `json:"title,omitempty"`
	Subtitle  string `json:"subtitle,omitempty"`
	ImageUrl  string `json:"imageUrl,omitempty"`
	ImageType string `json:"imageType,omitempty"`
}

// GoogleChatSection represents a section in the card
type GoogleChatSection struct {
	Header  string             `json:"header,omitempty"`
	Widgets []GoogleChatWidget `json:"widgets,omitempty"`
}

// GoogleChatWidget represents a widget in a section
type GoogleChatWidget struct {
	DecoratedText *GoogleChatDecoratedText `json:"decoratedText,omitempty"`
	TextParagraph *GoogleChatTextParagraph `json:"textParagraph,omitempty"`
	Divider       *GoogleChatDivider       `json:"divider,omitempty"`
}

// GoogleChatDecoratedText represents decorated text widget
type GoogleChatDecoratedText struct {
	TopLabel string          `json:"topLabel,omitempty"`
	Text     string          `json:"text,omitempty"`
	Icon     *GoogleChatIcon `json:"icon,omitempty"`
}

// GoogleChatTextParagraph represents a text paragraph widget
type GoogleChatTextParagraph struct {
	Text string `json:"text,omitempty"`
}

// GoogleChatDivider represents a divider widget
type GoogleChatDivider struct{}

// GoogleChatIcon represents an icon
type GoogleChatIcon struct {
	KnownIcon string `json:"knownIcon,omitempty"`
}

// sendGoogleChatNotification sends a notification to Google Chat if configured
func (r *RemediationPolicyReconciler) sendGoogleChatNotification(ctx context.Context, policy *dotaiv1alpha1.RemediationPolicy, event *corev1.Event, notificationType string, mcpRequest *dotaiv1alpha1.McpRequest, mcpResponse *McpResponse) error {
	logger := logf.FromContext(ctx)

	// Check if Google Chat notifications are enabled
	if !policy.Spec.Notifications.GoogleChat.Enabled {
		logger.V(1).Info("Google Chat notifications disabled, skipping")
		return nil
	}

	// Check notification type against policy configuration
	if notificationType == "start" && !policy.Spec.Notifications.GoogleChat.NotifyOnStart {
		logger.V(1).Info("Google Chat start notifications disabled, skipping")
		return nil
	}
	if notificationType == "complete" && !policy.Spec.Notifications.GoogleChat.NotifyOnComplete {
		logger.V(1).Info("Google Chat complete notifications disabled, skipping")
		return nil
	}

	// Resolve webhook URL from Secret or plain text
	webhookUrl, err := r.resolveWebhookUrl(
		ctx,
		policy.Namespace,
		policy.Spec.Notifications.GoogleChat.WebhookUrl,
		policy.Spec.Notifications.GoogleChat.WebhookUrlSecretRef,
		"Google Chat",
	)
	if err != nil {
		logger.Error(err, "failed to resolve Google Chat webhook URL")
		// Update notification health condition
		if updateErr := r.updateNotificationHealthCondition(ctx, policy, err); updateErr != nil {
			logger.Error(updateErr, "failed to update notification health condition")
		}
		return fmt.Errorf("failed to resolve Google Chat webhook URL: %w", err)
	}
	if webhookUrl == "" {
		logger.V(1).Info("Google Chat webhook URL not configured, skipping notification")
		return nil
	}

	// Create Google Chat message
	message := r.createGoogleChatMessage(policy, event, notificationType, mcpRequest, mcpResponse)

	// Send the message
	if err := r.sendGoogleChatWebhook(ctx, webhookUrl, message); err != nil {
		logger.Error(err, "failed to send Google Chat notification",
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

	logger.Info("💬 Google Chat notification sent successfully",
		"notificationType", notificationType)
	return nil
}

// createGoogleChatMessage creates a formatted Google Chat message using Card v2 API
func (r *RemediationPolicyReconciler) createGoogleChatMessage(policy *dotaiv1alpha1.RemediationPolicy, event *corev1.Event, notificationType string, mcpRequest *dotaiv1alpha1.McpRequest, mcpResponse *McpResponse) GoogleChatMessage {
	var title, subtitle string
	var sections []GoogleChatSection

	switch notificationType {
	case "start":
		title = "🔄 Remediation Started"
		subtitle = fmt.Sprintf("Policy: %s", policy.Name)
		sections = r.createGoogleChatStartSections(policy, event, mcpRequest)

	case "complete":
		if mcpResponse != nil && mcpResponse.Success {
			executed := r.getMcpExecutedStatus(mcpResponse)
			if executed {
				title = "✅ Remediation Completed Successfully"
			} else {
				title = "📋 Analysis Completed - Manual Action Required"
			}
		} else {
			title = "❌ Remediation Failed"
		}
		subtitle = fmt.Sprintf("Policy: %s", policy.Name)
		sections = r.createGoogleChatCompleteSections(policy, event, mcpRequest, mcpResponse)
	}

	return GoogleChatMessage{
		CardsV2: []GoogleChatCardV2{
			{
				CardId: "remediation-notification",
				Card: GoogleChatCard{
					Header: &GoogleChatCardHeader{
						Title:     title,
						Subtitle:  subtitle,
						ImageType: "CIRCLE",
					},
					Sections: sections,
				},
			},
		},
	}
}

// createGoogleChatStartSections creates sections for start notifications
func (r *RemediationPolicyReconciler) createGoogleChatStartSections(policy *dotaiv1alpha1.RemediationPolicy, event *corev1.Event, mcpRequest *dotaiv1alpha1.McpRequest) []GoogleChatSection {
	return []GoogleChatSection{
		{
			Header: "Event Details",
			Widgets: []GoogleChatWidget{
				{
					DecoratedText: &GoogleChatDecoratedText{
						TopLabel: "Event Type",
						Text:     fmt.Sprintf("%s/%s", event.Type, event.Reason),
						Icon:     &GoogleChatIcon{KnownIcon: "BOOKMARK"},
					},
				},
				{
					DecoratedText: &GoogleChatDecoratedText{
						TopLabel: "Resource",
						Text:     fmt.Sprintf("%s/%s", event.InvolvedObject.Kind, event.InvolvedObject.Name),
						Icon:     &GoogleChatIcon{KnownIcon: "DESCRIPTION"},
					},
				},
				{
					DecoratedText: &GoogleChatDecoratedText{
						TopLabel: "Namespace",
						Text:     event.InvolvedObject.Namespace,
						Icon:     &GoogleChatIcon{KnownIcon: "MAP_PIN"},
					},
				},
				{
					DecoratedText: &GoogleChatDecoratedText{
						TopLabel: "Mode",
						Text:     mcpRequest.Mode,
						Icon:     &GoogleChatIcon{KnownIcon: "TICKET"},
					},
				},
			},
		},
		{
			Header: "Issue",
			Widgets: []GoogleChatWidget{
				{
					TextParagraph: &GoogleChatTextParagraph{
						Text: mcpRequest.Issue,
					},
				},
			},
		},
		{
			Widgets: []GoogleChatWidget{
				{
					TextParagraph: &GoogleChatTextParagraph{
						Text: "<i>dot-ai Kubernetes Event Controller</i>",
					},
				},
			},
		},
	}
}

// createGoogleChatCompleteSections creates sections for completion notifications
func (r *RemediationPolicyReconciler) createGoogleChatCompleteSections(policy *dotaiv1alpha1.RemediationPolicy, event *corev1.Event, mcpRequest *dotaiv1alpha1.McpRequest, mcpResponse *McpResponse) []GoogleChatSection {
	sections := []GoogleChatSection{}

	// Add result message
	if mcpResponse != nil {
		var resultText string
		if mcpResponse.Success {
			resultText = mcpResponse.GetResultMessage()
		} else {
			resultText = mcpResponse.GetErrorMessage()
		}
		sections = append(sections, GoogleChatSection{
			Header: "Result",
			Widgets: []GoogleChatWidget{
				{
					TextParagraph: &GoogleChatTextParagraph{
						Text: resultText,
					},
				},
			},
		})
	}

	// Event details section
	sections = append(sections, GoogleChatSection{
		Header: "Event Details",
		Widgets: []GoogleChatWidget{
			{
				DecoratedText: &GoogleChatDecoratedText{
					TopLabel: "Event Type",
					Text:     fmt.Sprintf("%s/%s", event.Type, event.Reason),
					Icon:     &GoogleChatIcon{KnownIcon: "BOOKMARK"},
				},
			},
			{
				DecoratedText: &GoogleChatDecoratedText{
					TopLabel: "Resource",
					Text:     fmt.Sprintf("%s/%s", event.InvolvedObject.Kind, event.InvolvedObject.Name),
					Icon:     &GoogleChatIcon{KnownIcon: "DESCRIPTION"},
				},
			},
			{
				DecoratedText: &GoogleChatDecoratedText{
					TopLabel: "Namespace",
					Text:     event.InvolvedObject.Namespace,
					Icon:     &GoogleChatIcon{KnownIcon: "MAP_PIN"},
				},
			},
			{
				DecoratedText: &GoogleChatDecoratedText{
					TopLabel: "Mode",
					Text:     mcpRequest.Mode,
					Icon:     &GoogleChatIcon{KnownIcon: "TICKET"},
				},
			},
		},
	})

	// Add MCP details if available
	if mcpResponse != nil {
		mcpSections := r.createGoogleChatMcpDetailSections(mcpResponse)
		sections = append(sections, mcpSections...)
	}

	// Original issue section
	sections = append(sections, GoogleChatSection{
		Header: "Original Issue",
		Widgets: []GoogleChatWidget{
			{
				TextParagraph: &GoogleChatTextParagraph{
					Text: mcpRequest.Issue,
				},
			},
		},
	})

	// Footer
	sections = append(sections, GoogleChatSection{
		Widgets: []GoogleChatWidget{
			{
				TextParagraph: &GoogleChatTextParagraph{
					Text: "<i>dot-ai Kubernetes Event Controller</i>",
				},
			},
		},
	})

	return sections
}

// createGoogleChatMcpDetailSections extracts detailed information from MCP response
func (r *RemediationPolicyReconciler) createGoogleChatMcpDetailSections(mcpResponse *McpResponse) []GoogleChatSection {
	sections := []GoogleChatSection{}
	executed := r.getMcpExecutedStatus(mcpResponse)

	if mcpResponse.Data != nil && mcpResponse.Data.Result != nil {
		result := mcpResponse.Data.Result

		// Execution time and confidence
		var metaWidgets []GoogleChatWidget
		if mcpResponse.Data.ExecutionTime > 0 {
			executionTimeSeconds := mcpResponse.Data.ExecutionTime / 1000
			metaWidgets = append(metaWidgets, GoogleChatWidget{
				DecoratedText: &GoogleChatDecoratedText{
					TopLabel: "Execution Time",
					Text:     fmt.Sprintf("%.2fs", executionTimeSeconds),
					Icon:     &GoogleChatIcon{KnownIcon: "CLOCK"},
				},
			})
		}

		if confidence, ok := result["confidence"].(float64); ok {
			metaWidgets = append(metaWidgets, GoogleChatWidget{
				DecoratedText: &GoogleChatDecoratedText{
					TopLabel: "Confidence",
					Text:     fmt.Sprintf("%.0f%%", confidence*100),
					Icon:     &GoogleChatIcon{KnownIcon: "CONFIRMATION_NUMBER_ICON"},
				},
			})
		}

		if len(metaWidgets) > 0 {
			sections = append(sections, GoogleChatSection{
				Header:  "Metrics",
				Widgets: metaWidgets,
			})
		}

		// Root cause analysis
		if analysis, ok := result["analysis"].(map[string]interface{}); ok {
			var analysisWidgets []GoogleChatWidget
			if rootCause, ok := analysis["rootCause"].(string); ok && rootCause != "" {
				analysisWidgets = append(analysisWidgets, GoogleChatWidget{
					TextParagraph: &GoogleChatTextParagraph{
						Text: fmt.Sprintf("<b>Root Cause:</b> %s", rootCause),
					},
				})
			}
			if confLevel, ok := analysis["confidence"].(float64); ok {
				analysisWidgets = append(analysisWidgets, GoogleChatWidget{
					DecoratedText: &GoogleChatDecoratedText{
						TopLabel: "Analysis Confidence",
						Text:     fmt.Sprintf("%.0f%%", confLevel*100),
					},
				})
			}
			if len(analysisWidgets) > 0 {
				sections = append(sections, GoogleChatSection{
					Header:  "Analysis",
					Widgets: analysisWidgets,
				})
			}
		}

		// Remediation commands
		if remediation, ok := result["remediation"].(map[string]interface{}); ok {
			if actions, ok := remediation["actions"].([]interface{}); ok && len(actions) > 0 {
				commandsTitle := "Commands Executed"
				if !executed {
					commandsTitle = "Recommended Commands"
				}

				var cmdWidgets []GoogleChatWidget
				for i, action := range actions {
					// Limit to 10 commands
					if i >= 10 {
						cmdWidgets = append(cmdWidgets, GoogleChatWidget{
							TextParagraph: &GoogleChatTextParagraph{
								Text: fmt.Sprintf("<i>... and %d more commands</i>", len(actions)-10),
							},
						})
						break
					}

					if actionMap, ok := action.(map[string]interface{}); ok {
						if cmd, ok := actionMap["command"].(string); ok && cmd != "" {
							cmdWidgets = append(cmdWidgets, GoogleChatWidget{
								TextParagraph: &GoogleChatTextParagraph{
									Text: fmt.Sprintf("<code>%s</code>", cmd),
								},
							})
						}
					}
				}

				if len(cmdWidgets) > 0 {
					sections = append(sections, GoogleChatSection{
						Header:  commandsTitle,
						Widgets: cmdWidgets,
					})
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
				sections = append(sections, GoogleChatSection{
					Header: "Validation",
					Widgets: []GoogleChatWidget{
						{
							TextParagraph: &GoogleChatTextParagraph{
								Text: status,
							},
						},
					},
				})
			}
		}

		// Action count summary
		if results, ok := result["results"].([]interface{}); ok && len(results) > 0 {
			sections = append(sections, GoogleChatSection{
				Widgets: []GoogleChatWidget{
					{
						DecoratedText: &GoogleChatDecoratedText{
							TopLabel: "Actions Taken",
							Text:     fmt.Sprintf("%d remediation actions", len(results)),
							Icon:     &GoogleChatIcon{KnownIcon: "STAR"},
						},
					},
				},
			})
		}
	}

	// Add error details for failed responses
	if !mcpResponse.Success && mcpResponse.Error != nil {
		var errorWidgets []GoogleChatWidget
		if mcpResponse.Error.Code != "" {
			errorWidgets = append(errorWidgets, GoogleChatWidget{
				DecoratedText: &GoogleChatDecoratedText{
					TopLabel: "Error Code",
					Text:     mcpResponse.Error.Code,
					Icon:     &GoogleChatIcon{KnownIcon: "BUG_REPORT"},
				},
			})
		}
		if mcpResponse.Error.Details != nil {
			if reason, ok := mcpResponse.Error.Details["reason"].(string); ok && reason != "" {
				errorWidgets = append(errorWidgets, GoogleChatWidget{
					TextParagraph: &GoogleChatTextParagraph{
						Text: fmt.Sprintf("<b>Error Details:</b> %s", reason),
					},
				})
			}
		}
		if len(errorWidgets) > 0 {
			sections = append(sections, GoogleChatSection{
				Header:  "Error",
				Widgets: errorWidgets,
			})
		}
	}

	return sections
}

// sendGoogleChatWebhook sends the actual HTTP request to Google Chat webhook
func (r *RemediationPolicyReconciler) sendGoogleChatWebhook(ctx context.Context, webhookUrl string, message GoogleChatMessage) error {
	logger := logf.FromContext(ctx)

	// Marshal message to JSON
	payload, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal Google Chat message: %w", err)
	}

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, "POST", webhookUrl, bytes.NewBuffer(payload))
	if err != nil {
		return fmt.Errorf("failed to create Google Chat webhook request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	// Send request with timeout
	response, err := r.HttpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send Google Chat webhook: %w", err)
	}
	defer response.Body.Close()

	// Check response
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		return fmt.Errorf("Google Chat webhook returned status %d: %s", response.StatusCode, string(body))
	}

	logger.V(1).Info("Google Chat webhook sent successfully",
		"statusCode", response.StatusCode,
		"payloadSize", len(payload))

	return nil
}
