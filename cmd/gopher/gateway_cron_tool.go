package main

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/bstncartwright/gopher/pkg/agentcore"
	"github.com/bstncartwright/gopher/pkg/gateway"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

type gatewayCronToolService struct {
	service *gateway.CronService
}

func newGatewayCronToolService(service *gateway.CronService) *gatewayCronToolService {
	return &gatewayCronToolService{service: service}
}

func (s *gatewayCronToolService) CreateCronJob(ctx context.Context, req agentcore.CronCreateRequest) (agentcore.CronJob, error) {
	job, err := s.service.Create(ctx, gateway.CronCreateInput{
		SessionID: req.SessionID,
		Message:   req.Message,
		CronExpr:  req.CronExpr,
		Timezone:  req.Timezone,
		CreatedBy: req.CreatedBy,
	})
	if err != nil {
		return agentcore.CronJob{}, err
	}
	return toAgentCronJob(job), nil
}

func (s *gatewayCronToolService) ListCronJobs(ctx context.Context, req agentcore.CronListRequest) ([]agentcore.CronJob, error) {
	jobs, err := s.service.List(ctx, gateway.CronListOptions{SessionID: req.SessionID})
	if err != nil {
		return nil, err
	}
	out := make([]agentcore.CronJob, 0, len(jobs))
	for _, job := range jobs {
		out = append(out, toAgentCronJob(job))
	}
	return out, nil
}

func (s *gatewayCronToolService) DeleteCronJob(ctx context.Context, jobID string) (bool, error) {
	return s.service.Delete(ctx, jobID)
}

func (s *gatewayCronToolService) PauseCronJob(ctx context.Context, jobID string) (agentcore.CronJob, error) {
	job, err := s.service.Pause(ctx, jobID)
	if err != nil {
		return agentcore.CronJob{}, err
	}
	return toAgentCronJob(job), nil
}

func (s *gatewayCronToolService) ResumeCronJob(ctx context.Context, jobID string) (agentcore.CronJob, error) {
	job, err := s.service.Resume(ctx, jobID)
	if err != nil {
		return agentcore.CronJob{}, err
	}
	return toAgentCronJob(job), nil
}

func toAgentCronJob(job gateway.CronJob) agentcore.CronJob {
	out := agentcore.CronJob{
		ID:        job.ID,
		SessionID: job.SessionID,
		Message:   job.Message,
		CronExpr:  job.CronExpr,
		Timezone:  job.Timezone,
		Enabled:   job.Enabled,
		CreatedBy: job.CreatedBy,
		CreatedAt: job.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt: job.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
	if job.LastRunAt != nil {
		value := job.LastRunAt.UTC().Format(time.RFC3339Nano)
		out.LastRunAt = &value
	}
	if job.NextRunAt != nil {
		value := job.NextRunAt.UTC().Format(time.RFC3339Nano)
		out.NextRunAt = &value
	}
	return out
}

func collectHeartbeatSchedules(runtime *gatewayAgentRuntime) []gateway.HeartbeatSchedule {
	if runtime == nil {
		return nil
	}
	out := make([]gateway.HeartbeatSchedule, 0, len(runtime.Agents))
	for actorID, agent := range runtime.Agents {
		if agent == nil || !agent.Heartbeat.Enabled || agent.Heartbeat.Every <= 0 {
			continue
		}
		activeHours := gateway.HeartbeatActiveHours{
			Enabled:     agent.Heartbeat.ActiveHours.Enabled,
			Start:       agent.Heartbeat.ActiveHours.Start,
			End:         agent.Heartbeat.ActiveHours.End,
			StartMinute: agent.Heartbeat.ActiveHours.StartMinute,
			EndMinute:   agent.Heartbeat.ActiveHours.EndMinute,
			Timezone:    strings.TrimSpace(agent.Heartbeat.ActiveHours.Timezone),
			Location:    agent.Heartbeat.ActiveHours.Location,
		}
		if activeHours.Enabled && activeHours.Location == nil {
			fallbackTimezone := strings.TrimSpace(agent.Config.UserTimezone)
			if fallbackTimezone != "" {
				if location, err := time.LoadLocation(fallbackTimezone); err == nil {
					activeHours.Location = location
					if activeHours.Timezone == "" {
						activeHours.Timezone = fallbackTimezone
					}
				}
			}
			if activeHours.Location == nil {
				activeHours.Location = time.Local
				if activeHours.Timezone == "" {
					activeHours.Timezone = time.Local.String()
				}
			}
		}
		out = append(out, gateway.HeartbeatSchedule{
			AgentID:     actorID,
			Every:       agent.Heartbeat.Every,
			Prompt:      agent.Heartbeat.Prompt,
			AckMaxChars: agent.Heartbeat.AckMaxChars,
			SessionID:   sessionrt.SessionID(strings.TrimSpace(agent.Heartbeat.SessionID)),
			Workspace:   strings.TrimSpace(agent.Workspace),
			ActiveHours: activeHours,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return string(out[i].AgentID) < string(out[j].AgentID)
	})
	return out
}
