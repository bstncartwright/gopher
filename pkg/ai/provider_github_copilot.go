package ai

import (
	"regexp"
	"strings"
)

const (
	defaultGitHubCopilotBaseURL = "https://api.individual.githubcopilot.com"
	githubCopilotUserAgent      = "GitHubCopilotChat/0.35.0"
	githubCopilotEditorVersion  = "vscode/1.107.0"
	githubCopilotPluginVersion  = "copilot-chat/0.35.0"
	githubCopilotIntegrationID  = "vscode-chat"
)

var githubCopilotProxyEndpointPattern = regexp.MustCompile(`(?:^|;)proxy-ep=([^;]+)`)

func defaultGitHubCopilotHeaders() map[string]string {
	return map[string]string{
		"User-Agent":             githubCopilotUserAgent,
		"Editor-Version":         githubCopilotEditorVersion,
		"Editor-Plugin-Version":  githubCopilotPluginVersion,
		"Copilot-Integration-Id": githubCopilotIntegrationID,
	}
}

func resolveGitHubCopilotBaseURL(accessToken string) string {
	match := githubCopilotProxyEndpointPattern.FindStringSubmatch(strings.TrimSpace(accessToken))
	if len(match) < 2 {
		return defaultGitHubCopilotBaseURL
	}
	host := strings.TrimSpace(match[1])
	if host == "" {
		return defaultGitHubCopilotBaseURL
	}
	if strings.HasPrefix(host, "proxy.") {
		host = "api." + strings.TrimPrefix(host, "proxy.")
	}
	if !strings.HasPrefix(host, "http://") && !strings.HasPrefix(host, "https://") {
		host = "https://" + host
	}
	return strings.TrimRight(host, "/")
}

func inferGitHubCopilotInitiator(messages []Message) string {
	if len(messages) == 0 {
		return "user"
	}
	last := messages[len(messages)-1]
	if last.Role != RoleUser {
		return "agent"
	}
	return "user"
}

func hasGitHubCopilotVisionInput(messages []Message) bool {
	for _, msg := range messages {
		blocks, ok := msg.ContentBlocks()
		if !ok {
			continue
		}
		for _, block := range blocks {
			if block.Type == ContentTypeImage {
				return true
			}
		}
	}
	return false
}

func buildGitHubCopilotDynamicHeaders(messages []Message) map[string]string {
	headers := map[string]string{
		"X-Initiator":   inferGitHubCopilotInitiator(messages),
		"Openai-Intent": "conversation-edits",
	}
	if hasGitHubCopilotVisionInput(messages) {
		headers["Copilot-Vision-Request"] = "true"
	}
	return headers
}
