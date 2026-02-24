package main

import (
	"os"
	"path/filepath"
	"strings"

	sessionrt "github.com/bstncartwright/gopher/pkg/session"
	"github.com/bstncartwright/gopher/pkg/transport"
)

const maxMatrixAttachmentBytes = 20 << 20

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
		workspace := strings.TrimSpace(workspaceByActor[agentID])
		if workspace == "" {
			return nil
		}
		candidates := extractToolResultPathCandidates(event.Payload)
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

func extractToolResultPathCandidates(payload any) []string {
	root, ok := payload.(map[string]any)
	if !ok {
		return nil
	}
	result, exists := root["result"]
	if !exists {
		return nil
	}
	candidates := collectPathCandidates(result)
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

func collectPathCandidates(value any) []string {
	switch typed := value.(type) {
	case string:
		return []string{typed}
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			out = append(out, collectPathCandidates(item)...)
		}
		return out
	case []string:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			out = append(out, collectPathCandidates(item)...)
		}
		return out
	case map[string]any:
		out := []string{}
		for key, item := range typed {
			switch strings.ToLower(strings.TrimSpace(key)) {
			case "path", "file", "paths", "files", "result":
				out = append(out, collectPathCandidates(item)...)
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
