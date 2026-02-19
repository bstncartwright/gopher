package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bstncartwright/gopher/pkg/agentcore"
	"github.com/bstncartwright/gopher/pkg/gateway"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

type gatewayHeartbeatToolService struct {
	agents map[sessionrt.ActorID]*agentcore.Agent
	runner *gateway.HeartbeatRunner
	logger *log.Logger

	mu sync.Mutex
}

func newGatewayHeartbeatToolService(
	agents map[sessionrt.ActorID]*agentcore.Agent,
	runner *gateway.HeartbeatRunner,
	logger *log.Logger,
) *gatewayHeartbeatToolService {
	return &gatewayHeartbeatToolService{
		agents: agents,
		runner: runner,
		logger: logger,
	}
}

func (s *gatewayHeartbeatToolService) GetHeartbeat(_ context.Context, agentID string) (agentcore.HeartbeatState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, agent, err := s.resolveAgent(agentID)
	if err != nil {
		return agentcore.HeartbeatState{}, err
	}
	return heartbeatStateFromAgent(agent), nil
}

func (s *gatewayHeartbeatToolService) SetHeartbeat(_ context.Context, req agentcore.HeartbeatSetRequest) (agentcore.HeartbeatState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runner == nil {
		return agentcore.HeartbeatState{}, fmt.Errorf("heartbeat runner is unavailable")
	}
	actorID, agent, err := s.resolveAgent(req.AgentID)
	if err != nil {
		return agentcore.HeartbeatState{}, err
	}
	every := strings.TrimSpace(req.Every)
	if every == "" {
		return agentcore.HeartbeatState{}, fmt.Errorf("every is required")
	}
	if req.AckMaxChars != nil && *req.AckMaxChars <= 0 {
		return agentcore.HeartbeatState{}, fmt.Errorf("ack_max_chars must be > 0")
	}
	if req.UserTimezone != nil {
		timezone := strings.TrimSpace(*req.UserTimezone)
		if timezone != "" {
			if _, err := time.LoadLocation(timezone); err != nil {
				return agentcore.HeartbeatState{}, fmt.Errorf("invalid user_timezone %q: %w", timezone, err)
			}
		}
	}

	configPath := filepath.Join(agent.Workspace, "config.json")
	doc, err := readJSONDocument(configPath)
	if err != nil {
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
			Timezone:    strings.TrimSpace(updatedConfig.UserTimezone),
		}); err != nil {
			return agentcore.HeartbeatState{}, err
		}
	} else {
		s.runner.RemoveSchedule(actorID)
	}
	state := heartbeatStateFromAgent(agent)
	if s.logger != nil {
		s.logger.Printf("heartbeat config updated agent=%s enabled=%t every=%s timezone=%s", actorID, state.Enabled, state.Every, state.UserTimezone)
	}
	return state, nil
}

func (s *gatewayHeartbeatToolService) DisableHeartbeat(_ context.Context, agentID string) (agentcore.HeartbeatState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runner == nil {
		return agentcore.HeartbeatState{}, fmt.Errorf("heartbeat runner is unavailable")
	}
	actorID, agent, err := s.resolveAgent(agentID)
	if err != nil {
		return agentcore.HeartbeatState{}, err
	}

	configPath := filepath.Join(agent.Workspace, "config.json")
	doc, err := readJSONDocument(configPath)
	if err != nil {
		return agentcore.HeartbeatState{}, err
	}
	delete(doc, "heartbeat")
	updatedConfig, updatedHeartbeat, err := s.persistConfigAndHydrateAgent(configPath, doc)
	if err != nil {
		return agentcore.HeartbeatState{}, err
	}
	agent.Config = updatedConfig
	agent.Heartbeat = updatedHeartbeat
	s.runner.RemoveSchedule(actorID)
	state := heartbeatStateFromAgent(agent)
	if s.logger != nil {
		s.logger.Printf("heartbeat config disabled agent=%s", actorID)
	}
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
	if err := writeJSONDocument(path, doc); err != nil {
		return agentcore.AgentConfig{}, agentcore.AgentHeartbeat{}, err
	}
	blob, err := json.Marshal(doc)
	if err != nil {
		return agentcore.AgentConfig{}, agentcore.AgentHeartbeat{}, fmt.Errorf("marshal updated config: %w", err)
	}
	updatedConfig := agentcore.AgentConfig{}
	if err := json.Unmarshal(blob, &updatedConfig); err != nil {
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
	return state
}

func readJSONDocument(path string) (map[string]any, error) {
	blob, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	doc := map[string]any{}
	if err := json.Unmarshal(blob, &doc); err != nil {
		return nil, fmt.Errorf("decode config %s: %w", path, err)
	}
	return doc, nil
}

func writeJSONDocument(path string, doc map[string]any) error {
	blob, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config %s: %w", path, err)
	}
	blob = append(blob, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir config dir %s: %w", dir, err)
	}
	tmpFile, err := os.CreateTemp(dir, "config.json.tmp-*")
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
