package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/bstncartwright/gopher/pkg/agentcore"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
	"github.com/bstncartwright/gopher/pkg/transport"
)

const maxMatrixAttachmentBytes = 20 << 20

var autoAttachmentToolNames = map[string]struct{}{
	"write": {},
}

func newMatrixAttachmentResolver(runtime *gatewayAgentRuntime) func(conversationID string, agentID sessionrt.ActorID, event sessionrt.Event) []transport.OutboundAttachment {
	if runtime == nil || len(runtime.Agents) == 0 {
		return nil
	}
	workspaceByActor := make(map[sessionrt.ActorID]string, len(runtime.Agents))
	for actorID, agent := range runtime.Agents {
		if agent == nil {
			continue
		}
		workspace := filepath.Clean(strings.TrimSpace(agent.Workspace))
		if workspace == "" {
			continue
		}
		workspaceByActor[actorID] = workspace
	}
	if len(workspaceByActor) == 0 {
		return nil
	}

	return func(_ string, agentID sessionrt.ActorID, event sessionrt.Event) []transport.OutboundAttachment {
		if event.Type != sessionrt.EventToolResult {
			return nil
		}
		root, ok := event.Payload.(map[string]any)
		if !ok || !shouldResolveMatrixAttachments(root) {
			return nil
		}
		workspace := strings.TrimSpace(workspaceByActor[agentID])
		if workspace == "" {
			return nil
		}
		candidates := extractToolResultPathCandidates(root)
		if len(candidates) == 0 {
			return nil
		}
		out := make([]transport.OutboundAttachment, 0, len(candidates))
		seen := make(map[string]struct{}, len(candidates))
		for _, candidate := range candidates {
			resolved, ok := resolveAttachmentCandidatePath(workspace, candidate)
			if !ok {
				continue
			}
			if _, exists := seen[resolved]; exists {
				continue
			}
			info, err := os.Stat(resolved)
			if err != nil || info == nil || info.IsDir() || !info.Mode().IsRegular() {
				continue
			}
			if info.Size() > maxMatrixAttachmentBytes {
				continue
			}
			seen[resolved] = struct{}{}
			out = append(out, transport.OutboundAttachment{
				Path: resolved,
				Name: filepath.Base(resolved),
			})
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}
}

func shouldResolveMatrixAttachments(root map[string]any) bool {
	if strings.ToLower(strings.TrimSpace(stringValue(root["status"]))) != string(agentcore.ToolStatusOK) {
		return false
	}
	if hasExplicitAttachmentCandidates(root["result"]) {
		return true
	}
	_, allowed := autoAttachmentToolNames[strings.ToLower(strings.TrimSpace(stringValue(root["name"])))]
	return allowed
}

func hasExplicitAttachmentCandidates(value any) bool {
	resultMap, ok := value.(map[string]any)
	if !ok {
		return false
	}
	for key := range resultMap {
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "attachment", "attachments", "attachment_path", "attachment_paths":
			return true
		}
	}
	return false
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	default:
		return ""
	}
}

func extractToolResultPathCandidates(root map[string]any) []string {
	result, exists := root["result"]
	if !exists {
		return nil
	}
	hasExplicit := hasExplicitAttachmentCandidates(result)
	allowedAuto := false
	if !hasExplicit {
		_, allowedAuto = autoAttachmentToolNames[strings.ToLower(strings.TrimSpace(stringValue(root["name"])))]
	}
	if !hasExplicit && !allowedAuto {
		return nil
	}
	candidates := collectPathCandidates(result, hasExplicit)
	if len(candidates) == 0 {
		return nil
	}
	out := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if _, exists := seen[candidate]; exists {
			continue
		}
		seen[candidate] = struct{}{}
		out = append(out, candidate)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func collectPathCandidates(value any, explicitOnly bool) []string {
	switch typed := value.(type) {
	case string:
		return []string{typed}
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			out = append(out, collectPathCandidates(item, explicitOnly)...)
		}
		return out
	case []string:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			out = append(out, collectPathCandidates(item, explicitOnly)...)
		}
		return out
	case map[string]any:
		out := []string{}
		for key, item := range typed {
			keyValue := strings.ToLower(strings.TrimSpace(key))
			switch keyValue {
			case "attachment", "attachments", "attachment_path", "attachment_paths":
				out = append(out, collectPathCandidates(item, false)...)
			case "path", "file", "paths", "files", "result":
				if explicitOnly {
					continue
				}
				out = append(out, collectPathCandidates(item, false)...)
			}
		}
		return out
	default:
		return nil
	}
}

func resolveAttachmentCandidatePath(workspace, candidate string) (string, bool) {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return "", false
	}
	if strings.Contains(candidate, "://") {
		return "", false
	}
	pathValue := candidate
	if !filepath.IsAbs(pathValue) {
		pathValue = filepath.Join(workspace, pathValue)
	}
	abs, err := filepath.Abs(pathValue)
	if err != nil {
		return "", false
	}
	abs = filepath.Clean(abs)
	if !isWithinPath(abs, workspace) {
		return "", false
	}
	return abs, true
}
