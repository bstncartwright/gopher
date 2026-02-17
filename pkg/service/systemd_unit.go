package service

import (
	"fmt"
	"strings"
)

type GatewayUnitConfig struct {
	Description string
	ExecStart   string
	User        string
	Group       string
	WorkingDir  string
	EnvFile     string
}

type UpdateUnitConfig struct {
	Description string
	ExecStart   string
	EnvFile     string
}

type UpdateTimerConfig struct {
	Description string
	OnCalendar  string
}

func RenderGatewayUnit(cfg GatewayUnitConfig) (string, error) {
	if strings.TrimSpace(cfg.ExecStart) == "" {
		return "", fmt.Errorf("gateway exec start is required")
	}
	description := strings.TrimSpace(cfg.Description)
	if description == "" {
		description = "gopher gateway service"
	}
	user := strings.TrimSpace(cfg.User)
	if user == "" {
		user = "root"
	}
	group := strings.TrimSpace(cfg.Group)
	if group == "" {
		group = user
	}
	workingDir := strings.TrimSpace(cfg.WorkingDir)
	if workingDir == "" {
		workingDir = "/var/lib/gopher"
	}
	envFile := strings.TrimSpace(cfg.EnvFile)
	if envFile == "" {
		envFile = "/etc/gopher/gopher.env"
	}

	unit := `[Unit]
Description=%s
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=%s
Group=%s
WorkingDirectory=%s
EnvironmentFile=-%s
ExecStart=%s
Restart=on-failure
RestartSec=5s
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
`
	return fmt.Sprintf(unit, description, user, group, workingDir, envFile, cfg.ExecStart), nil
}

func RenderUpdateServiceUnit(cfg UpdateUnitConfig) (string, error) {
	if strings.TrimSpace(cfg.ExecStart) == "" {
		return "", fmt.Errorf("update exec start is required")
	}
	description := strings.TrimSpace(cfg.Description)
	if description == "" {
		description = "gopher gateway updater"
	}
	envFile := strings.TrimSpace(cfg.EnvFile)
	if envFile == "" {
		envFile = "/etc/gopher/gopher.env"
	}
	unit := `[Unit]
Description=%s
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
EnvironmentFile=-%s
ExecStart=%s
`
	return fmt.Sprintf(unit, description, envFile, cfg.ExecStart), nil
}

func RenderUpdateTimerUnit(cfg UpdateTimerConfig) (string, error) {
	onCalendar := strings.TrimSpace(cfg.OnCalendar)
	if onCalendar == "" {
		onCalendar = "hourly"
	}
	description := strings.TrimSpace(cfg.Description)
	if description == "" {
		description = "gopher gateway update timer"
	}
	unit := `[Unit]
Description=%s

[Timer]
OnCalendar=%s
Persistent=true
Unit=gopher-gateway-update.service

[Install]
WantedBy=timers.target
`
	return fmt.Sprintf(unit, description, onCalendar), nil
}
