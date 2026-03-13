package main

import (
	"context"
	"log/slog"
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
	slog.Debug(
		"gateway_cron_tool: creating cron job",
		"session_id", req.SessionID,
		"title", strings.TrimSpace(req.Title),
		"mode", strings.TrimSpace(req.Mode),
		"notify_actor_id", strings.TrimSpace(req.NotifyActorID),
		"target_agent", strings.TrimSpace(req.TargetAgent),
	)
	job, err := s.service.Create(ctx, gateway.CronCreateInput{
		SessionID:     req.SessionID,
		Title:         req.Title,
		Message:       req.Message,
		CronExpr:      req.CronExpr,
		Timezone:      req.Timezone,
		Mode:          req.Mode,
		NotifyActorID: req.NotifyActorID,
		TargetAgent:   req.TargetAgent,
		ModelPolicy:   req.ModelPolicy,
		CreatedBy:     req.CreatedBy,
	})
	if err != nil {
		slog.Error("gateway_cron_tool: failed to create cron job", "session_id", req.SessionID, "error", err)
		return agentcore.CronJob{}, err
	}
	slog.Info("gateway_cron_tool: cron job created", "job_id", job.ID, "session_id", job.SessionID, "mode", job.Mode)
	return toAgentCronJob(job), nil
}

func (s *gatewayCronToolService) ListCronJobs(ctx context.Context, req agentcore.CronListRequest) ([]agentcore.CronJob, error) {
	slog.Debug("gateway_cron_tool: listing cron jobs", "session_id", req.SessionID)
	jobs, err := s.service.List(ctx, gateway.CronListOptions{SessionID: req.SessionID})
	if err != nil {
		slog.Error("gateway_cron_tool: failed to list cron jobs", "session_id", req.SessionID, "error", err)
		return nil, err
	}
	out := make([]agentcore.CronJob, 0, len(jobs))
	for _, job := range jobs {
		out = append(out, toAgentCronJob(job))
	}
	slog.Debug("gateway_cron_tool: listed cron jobs", "session_id", req.SessionID, "count", len(out))
	return out, nil
}

func (s *gatewayCronToolService) DeleteCronJob(ctx context.Context, jobID string) (bool, error) {
	slog.Debug("gateway_cron_tool: deleting cron job", "job_id", strings.TrimSpace(jobID))
	deleted, err := s.service.Delete(ctx, jobID)
	if err != nil {
		slog.Error("gateway_cron_tool: failed to delete cron job", "job_id", strings.TrimSpace(jobID), "error", err)
		return false, err
	}
	slog.Info("gateway_cron_tool: cron job deleted", "job_id", strings.TrimSpace(jobID), "deleted", deleted)
	return deleted, nil
}

func (s *gatewayCronToolService) PauseCronJob(ctx context.Context, jobID string) (agentcore.CronJob, error) {
	slog.Debug("gateway_cron_tool: pausing cron job", "job_id", strings.TrimSpace(jobID))
	job, err := s.service.Pause(ctx, jobID)
	if err != nil {
		slog.Error("gateway_cron_tool: failed to pause cron job", "job_id", strings.TrimSpace(jobID), "error", err)
		return agentcore.CronJob{}, err
	}
	slog.Info("gateway_cron_tool: cron job paused", "job_id", job.ID)
	return toAgentCronJob(job), nil
}

func (s *gatewayCronToolService) ResumeCronJob(ctx context.Context, jobID string) (agentcore.CronJob, error) {
	slog.Debug("gateway_cron_tool: resuming cron job", "job_id", strings.TrimSpace(jobID))
	job, err := s.service.Resume(ctx, jobID)
	if err != nil {
		slog.Error("gateway_cron_tool: failed to resume cron job", "job_id", strings.TrimSpace(jobID), "error", err)
		return agentcore.CronJob{}, err
	}
	slog.Info("gateway_cron_tool: cron job resumed", "job_id", job.ID, "next_run_at", job.NextRunAt)
	return toAgentCronJob(job), nil
}

func toAgentCronJob(job gateway.CronJob) agentcore.CronJob {
	out := agentcore.CronJob{
		ID:             job.ID,
		SessionID:      job.SessionID,
		Title:          job.Title,
		Message:        job.Message,
		CronExpr:       job.CronExpr,
		Timezone:       job.Timezone,
		Mode:           job.Mode,
		NotifyActorID:  job.NotifyActorID,
		TargetAgent:    job.TargetAgent,
		ModelPolicy:    job.ModelPolicy,
		Enabled:        job.Enabled,
		CreatedBy:      job.CreatedBy,
		CreatedAt:      job.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:      job.UpdatedAt.UTC().Format(time.RFC3339Nano),
		LastRunStatus:  job.LastRunStatus,
		LastRunSummary: job.LastRunSummary,
		LastRunError:   job.LastRunError,
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
		slog.Debug("gateway_cron_tool: runtime missing while collecting heartbeat schedules")
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
	slog.Debug("gateway_cron_tool: collected heartbeat schedules", "count", len(out))
	return out
}
