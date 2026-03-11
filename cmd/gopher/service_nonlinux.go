//go:build !linux

package main

import (
	"context"
	"fmt"
	"io"
)

type unsupportedServiceRuntime struct{}

func defaultServiceRuntime(stdout, stderr io.Writer) serviceRuntime {
	return &unsupportedServiceRuntime{}
}

func (r *unsupportedServiceRuntime) Install(ctx context.Context, opts serviceInstallOptions) error {
	return fmt.Errorf("service install is only supported on linux")
}

func (r *unsupportedServiceRuntime) InstallUpdater(ctx context.Context, opts serviceUpdaterInstallOptions) error {
	_ = opts
	return fmt.Errorf("service install-updater is only supported on linux")
}

func (r *unsupportedServiceRuntime) Uninstall(ctx context.Context) error {
	return fmt.Errorf("service uninstall is only supported on linux")
}

func (r *unsupportedServiceRuntime) Status(ctx context.Context, opts serviceStatusOptions) error {
	_ = opts
	return fmt.Errorf("service status is only supported on linux")
}

func (r *unsupportedServiceRuntime) Start(ctx context.Context) error {
	return fmt.Errorf("service start is only supported on linux")
}

func (r *unsupportedServiceRuntime) Stop(ctx context.Context) error {
	return fmt.Errorf("service stop is only supported on linux")
}

func (r *unsupportedServiceRuntime) Restart(ctx context.Context, opts serviceTargetOptions) error {
	_ = opts
	return fmt.Errorf("service restart is only supported on linux")
}

func (r *unsupportedServiceRuntime) Logs(ctx context.Context, opts serviceLogsOptions) error {
	_ = opts
	return fmt.Errorf("service logs is only supported on linux")
}
