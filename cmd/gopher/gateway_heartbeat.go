package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bstncartwright/gopher/pkg/agentcore"
	"github.com/bstncartwright/gopher/pkg/gateway"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
	"github.com/pelletier/go-toml/v2"
)

type gatewayHeartbeatToolService struct {
	agents map[sessionrt.ActorID]*agentcore.Agent
	runner *gateway.HeartbeatRunner

	mu sync.Mutex
}

func newGatewayHeartbeatToolService(
	agents map[sessionrt.ActorID]*agentcore.Agent,
	runner *gateway.HeartbeatRunner,
) *gatewayHeartbeatToolService {
	return &gatewayHeartbeatToolService{
		agents: agents,
		runner: runner,
	}
}

func (s *gatewayHeartbeatToolService) GetHeartbeat(_ context.Context, agentID string) (agentcore.HeartbeatState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, agent, err := s.resolveAgent(agentID)
	if err != nil {
		slog.Warn("gateway_heartbeat_tool: get heartbeat failed", "agent_id", agentID, "error", err)
		return agentcore.HeartbeatState{}, err
	}
	state := heartbeatStateFromAgent(agent)
	slog.Debug("gateway_heartbeat_tool: get heartbeat", "agent_id", agentID, "enabled", state.Enabled, "every", state.Every)
	return state, nil
}

func (s *gatewayHeartbeatToolService) SetHeartbeat(_ context.Context, req agentcore.HeartbeatSetRequest) (agentcore.HeartbeatState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	slog.Debug("gateway_heartbeat_tool: set heartbeat request", "agent_id", req.AgentID, "every", req.Every)
	if s.runner == nil {
		slog.Warn("gateway_heartbeat_tool: heartbeat runner unavailable")
		return agentcore.HeartbeatState{}, fmt.Errorf("heartbeat runner is unavailable")
	}
	actorID, agent, err := s.resolveAgent(req.AgentID)
	if err != nil {
		slog.Warn("gateway_heartbeat_tool: resolve agent failed", "agent_id", req.AgentID, "error", err)
		return agentcore.HeartbeatState{}, err
	}
	every := strings.TrimSpace(req.Every)
	if every == "" {
		slog.Warn("gateway_heartbeat_tool: every is required", "agent_id", actorID)
		return agentcore.HeartbeatState{}, fmt.Errorf("every is required")
	}
	if req.AckMaxChars != nil && *req.AckMaxChars <= 0 {
		slog.Warn("gateway_heartbeat_tool: invalid ack_max_chars", "agent_id", actorID, "ack_max_chars", *req.AckMaxChars)
		return agentcore.HeartbeatState{}, fmt.Errorf("ack_max_chars must be > 0")
	}
	if req.UserTimezone != nil {
		timezone := strings.TrimSpace(*req.UserTimezone)
		if timezone != "" {
			if _, err := time.LoadLocation(timezone); err != nil {
				slog.Warn("gateway_heartbeat_tool: invalid timezone", "agent_id", actorID, "timezone", timezone, "error", err)
				return agentcore.HeartbeatState{}, fmt.Errorf("invalid user_timezone %q: %w", timezone, err)
			}
		}
	}
	if req.ActiveHours != nil {
		start := strings.TrimSpace(req.ActiveHours.Start)
		end := strings.TrimSpace(req.ActiveHours.End)
		if start == "" || end == "" {
			slog.Warn("gateway_heartbeat_tool: invalid active_hours", "agent_id", actorID, "start", start, "end", end)
			return agentcore.HeartbeatState{}, fmt.Errorf("active_hours.start and active_hours.end are required")
		}
		timezone := strings.TrimSpace(req.ActiveHours.Timezone)
		if timezone != "" {
			if _, err := time.LoadLocation(timezone); err != nil {
				slog.Warn("gateway_heartbeat_tool: invalid active_hours.timezone", "agent_id", actorID, "timezone", timezone, "error", err)
				return agentcore.HeartbeatState{}, fmt.Errorf("invalid active_hours.timezone %q: %w", timezone, err)
			}
		}
	}

	configPath, err := resolveAgentConfigPath(agent.Workspace)
	if err != nil {
		return agentcore.HeartbeatState{}, err
	}
	doc, err := readConfigDocument(configPath)
	if err != nil {
		slog.Error("gateway_heartbeat_tool: read config failed", "agent_id", actorID, "path", configPath, "error", err)
		return agentcore.HeartbeatState{}, err
	}
	heartbeatDoc := map[string]any{
		"every": every,
	}
	if req.Prompt != nil {
		prompt := strings.TrimSpace(*req.Prompt)
		if prompt != "" {
			heartbeatDoc["prompt"] = prompt
		}
	}
	if req.AckMaxChars != nil {
		heartbeatDoc["ack_max_chars"] = *req.AckMaxChars
	}
	if req.Session != nil {
		sessionID := strings.TrimSpace(*req.Session)
		if sessionID != "" {
			heartbeatDoc["session"] = sessionID
		}
	}
	if req.ActiveHours != nil {
		heartbeatDoc["active_hours"] = map[string]any{
			"start": strings.TrimSpace(req.ActiveHours.Start),
			"end":   strings.TrimSpace(req.ActiveHours.End),
		}
		timezone := strings.TrimSpace(req.ActiveHours.Timezone)
		if timezone != "" {
			heartbeatDoc["active_hours"].(map[string]any)["timezone"] = timezone
		}
	}
	doc["heartbeat"] = heartbeatDoc
	if req.UserTimezone != nil {
		timezone := strings.TrimSpace(*req.UserTimezone)
		if timezone == "" {
			delete(doc, "user_timezone")
		} else {
			doc["user_timezone"] = timezone
		}
	}
	updatedConfig, updatedHeartbeat, err := s.persistConfigAndHydrateAgent(configPath, doc)
	if err != nil {
		slog.Error("gateway_heartbeat_tool: persist config failed", "agent_id", actorID, "error", err)
		return agentcore.HeartbeatState{}, err
	}
	agent.Config = updatedConfig
	agent.Heartbeat = updatedHeartbeat
	if updatedHeartbeat.Enabled {
		if err := s.runner.UpsertSchedule(gateway.HeartbeatSchedule{
			AgentID:     actorID,
			Every:       updatedHeartbeat.Every,
			Prompt:      updatedHeartbeat.Prompt,
			AckMaxChars: updatedHeartbeat.AckMaxChars,
			SessionID:   sessionrt.SessionID(strings.TrimSpace(updatedHeartbeat.SessionID)),
			Workspace:   strings.TrimSpace(agent.Workspace),
			ActiveHours: gateway.HeartbeatActiveHours{
				Enabled:     updatedHeartbeat.ActiveHours.Enabled,
				Start:       updatedHeartbeat.ActiveHours.Start,
				End:         updatedHeartbeat.ActiveHours.End,
				StartMinute: updatedHeartbeat.ActiveHours.StartMinute,
				EndMinute:   updatedHeartbeat.ActiveHours.EndMinute,
				Timezone:    updatedHeartbeat.ActiveHours.Timezone,
				Location:    updatedHeartbeat.ActiveHours.Location,
			},
		}); err != nil {
			slog.Error("gateway_heartbeat_tool: upsert schedule failed", "agent_id", actorID, "error", err)
			return agentcore.HeartbeatState{}, err
		}
	} else {
		s.runner.RemoveSchedule(actorID)
	}
	state := heartbeatStateFromAgent(agent)
	slog.Info("gateway_heartbeat_tool: heartbeat config updated", "agent_id", actorID, "enabled", state.Enabled, "every", state.Every, "timezone", state.UserTimezone)
	return state, nil
}

func (s *gatewayHeartbeatToolService) DisableHeartbeat(_ context.Context, agentID string) (agentcore.HeartbeatState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	slog.Debug("gateway_heartbeat_tool: disable heartbeat request", "agent_id", agentID)
	if s.runner == nil {
		slog.Warn("gateway_heartbeat_tool: heartbeat runner unavailable for disable")
		return agentcore.HeartbeatState{}, fmt.Errorf("heartbeat runner is unavailable")
	}
	actorID, agent, err := s.resolveAgent(agentID)
	if err != nil {
		slog.Warn("gateway_heartbeat_tool: resolve agent failed for disable", "agent_id", agentID, "error", err)
		return agentcore.HeartbeatState{}, err
	}

	configPath, err := resolveAgentConfigPath(agent.Workspace)
	if err != nil {
		return agentcore.HeartbeatState{}, err
	}
	doc, err := readConfigDocument(configPath)
	if err != nil {
		slog.Error("gateway_heartbeat_tool: read config failed for disable", "agent_id", actorID, "path", configPath, "error", err)
		return agentcore.HeartbeatState{}, err
	}
	delete(doc, "heartbeat")
	updatedConfig, updatedHeartbeat, err := s.persistConfigAndHydrateAgent(configPath, doc)
	if err != nil {
		slog.Error("gateway_heartbeat_tool: persist config failed for disable", "agent_id", actorID, "error", err)
		return agentcore.HeartbeatState{}, err
	}
	agent.Config = updatedConfig
	agent.Heartbeat = updatedHeartbeat
	s.runner.RemoveSchedule(actorID)
	state := heartbeatStateFromAgent(agent)
	slog.Info("gateway_heartbeat_tool: heartbeat config disabled", "agent_id", actorID)
	return state, nil
}

func (s *gatewayHeartbeatToolService) resolveAgent(agentID string) (sessionrt.ActorID, *agentcore.Agent, error) {
	if s == nil || len(s.agents) == 0 {
		return "", nil, fmt.Errorf("heartbeat service is unavailable")
	}
	actorID := sessionrt.ActorID(strings.TrimSpace(agentID))
	if strings.TrimSpace(string(actorID)) == "" {
		return "", nil, fmt.Errorf("agent id is required")
	}
	agent, ok := s.agents[actorID]
	if !ok || agent == nil {
		return "", nil, fmt.Errorf("unknown agent %q", actorID)
	}
	return actorID, agent, nil
}

func (s *gatewayHeartbeatToolService) persistConfigAndHydrateAgent(path string, doc map[string]any) (agentcore.AgentConfig, agentcore.AgentHeartbeat, error) {
	if err := writeConfigDocument(path, doc); err != nil {
		return agentcore.AgentConfig{}, agentcore.AgentHeartbeat{}, err
	}
	blob, err := marshalConfigDocument(path, doc)
	if err != nil {
		return agentcore.AgentConfig{}, agentcore.AgentHeartbeat{}, fmt.Errorf("marshal updated config: %w", err)
	}
	updatedConfig := agentcore.AgentConfig{}
	if err := unmarshalConfigDocument(path, blob, &updatedConfig); err != nil {
		return agentcore.AgentConfig{}, agentcore.AgentHeartbeat{}, fmt.Errorf("decode updated config: %w", err)
	}
	updatedHeartbeat, err := agentcore.NormalizeHeartbeatConfig(updatedConfig.Heartbeat)
	if err != nil {
		return agentcore.AgentConfig{}, agentcore.AgentHeartbeat{}, err
	}
	return updatedConfig, updatedHeartbeat, nil
}

func heartbeatStateFromAgent(agent *agentcore.Agent) agentcore.HeartbeatState {
	state := agentcore.HeartbeatState{}
	if agent == nil {
		return state
	}
	state.UserTimezone = strings.TrimSpace(agent.Config.UserTimezone)
	if !agent.Heartbeat.Enabled || agent.Heartbeat.Every <= 0 {
		return state
	}
	state.Enabled = true
	state.Every = agent.Heartbeat.Every.String()
	state.Prompt = agent.Heartbeat.Prompt
	state.AckMaxChars = agent.Heartbeat.AckMaxChars
	state.Session = strings.TrimSpace(agent.Heartbeat.SessionID)
	if agent.Heartbeat.ActiveHours.Enabled {
		state.ActiveHours = &agentcore.HeartbeatActiveHoursConfig{
			Start:    agent.Heartbeat.ActiveHours.Start,
			End:      agent.Heartbeat.ActiveHours.End,
			Timezone: agent.Heartbeat.ActiveHours.Timezone,
		}
	}
	return state
}

func resolveAgentConfigPath(workspace string) (string, error) {
	candidates := []string{
		filepath.Join(workspace, "config.toml"),
		filepath.Join(workspace, "config.json"),
	}
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", fmt.Errorf("stat config %s: %w", candidate, err)
		}
		if !info.IsDir() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("agent config not found in workspace %s", workspace)
}

func readConfigDocument(path string) (map[string]any, error) {
	blob, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	doc := map[string]any{}
	if err := unmarshalConfigDocument(path, blob, &doc); err != nil {
		return nil, fmt.Errorf("decode config %s: %w", path, err)
	}
	return doc, nil
}

func writeConfigDocument(path string, doc map[string]any) error {
	blob, err := marshalConfigDocument(path, doc)
	if err != nil {
		return fmt.Errorf("encode config %s: %w", path, err)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir config dir %s: %w", dir, err)
	}
	tmpFile, err := os.CreateTemp(dir, "config.tmp-*")
	if err != nil {
		return fmt.Errorf("create temp config file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if _, err := tmpFile.Write(blob); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("write temp config file: %w", err)
	}
	if err := tmpFile.Chmod(0o644); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("chmod temp config file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close temp config file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace config file: %w", err)
	}
	return nil
}

func marshalConfigDocument(path string, doc map[string]any) ([]byte, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".toml":
		blob, err := toml.Marshal(doc)
		if err != nil {
			return nil, err
		}
		return append(blob, '\n'), nil
	case ".json":
		blob, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return nil, err
		}
		return append(blob, '\n'), nil
	default:
		return nil, fmt.Errorf("unsupported config format %q", filepath.Ext(path))
	}
}

func unmarshalConfigDocument(path string, blob []byte, out any) error {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".toml":
		return toml.Unmarshal(blob, out)
	case ".json":
		return json.Unmarshal(blob, out)
	default:
		return fmt.Errorf("unsupported config format %q", filepath.Ext(path))
	}
}
