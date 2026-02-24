//go:build linux

package main

import (
	"context"
	"fmt"
	"testing"
)

func TestResolveManagedServiceUnitPrefersInstalledNodeWhenGatewayMissing(t *testing.T) {
	prev := readUnitStatusForManagedUnit
	defer func() { readUnitStatusForManagedUnit = prev }()
	readUnitStatusForManagedUnit = func(ctx context.Context, unit string) (unitStatus, error) {
		_ = ctx
		switch unit {
		case gopherGatewayUnitName:
			return unitStatus{LoadState: "not-found", ActiveState: "inactive"}, nil
		case gopherNodeUnitName:
			return unitStatus{LoadState: "loaded", ActiveState: "active"}, nil
		default:
			return unitStatus{}, fmt.Errorf("unexpected unit %q", unit)
		}
	}

	unit, err := resolveManagedServiceUnit(context.Background(), serviceTargetAuto)
	if err != nil {
		t.Fatalf("resolveManagedServiceUnit() error: %v", err)
	}
	if unit != gopherNodeUnitName {
		t.Fatalf("unit = %q, want %q", unit, gopherNodeUnitName)
	}
}

func TestResolveManagedServiceUnitPrefersGatewayWhenNodeMissing(t *testing.T) {
	prev := readUnitStatusForManagedUnit
	defer func() { readUnitStatusForManagedUnit = prev }()
	readUnitStatusForManagedUnit = func(ctx context.Context, unit string) (unitStatus, error) {
		_ = ctx
		switch unit {
		case gopherGatewayUnitName:
			return unitStatus{LoadState: "loaded", ActiveState: "active"}, nil
		case gopherNodeUnitName:
			return unitStatus{LoadState: "not-found", ActiveState: "inactive"}, nil
		default:
			return unitStatus{}, fmt.Errorf("unexpected unit %q", unit)
		}
	}

	unit, err := resolveManagedServiceUnit(context.Background(), serviceTargetAuto)
	if err != nil {
		t.Fatalf("resolveManagedServiceUnit() error: %v", err)
	}
	if unit != gopherGatewayUnitName {
		t.Fatalf("unit = %q, want %q", unit, gopherGatewayUnitName)
	}
}

func TestResolveManagedServiceUnitPrefersActiveUnitWhenBothInstalled(t *testing.T) {
	prev := readUnitStatusForManagedUnit
	defer func() { readUnitStatusForManagedUnit = prev }()
	readUnitStatusForManagedUnit = func(ctx context.Context, unit string) (unitStatus, error) {
		_ = ctx
		switch unit {
		case gopherGatewayUnitName:
			return unitStatus{LoadState: "loaded", ActiveState: "inactive"}, nil
		case gopherNodeUnitName:
			return unitStatus{LoadState: "loaded", ActiveState: "active"}, nil
		default:
			return unitStatus{}, fmt.Errorf("unexpected unit %q", unit)
		}
	}

	unit, err := resolveManagedServiceUnit(context.Background(), serviceTargetAuto)
	if err != nil {
		t.Fatalf("resolveManagedServiceUnit() error: %v", err)
	}
	if unit != gopherNodeUnitName {
		t.Fatalf("unit = %q, want %q", unit, gopherNodeUnitName)
	}
}

func TestResolveManagedServiceUnitErrorsWhenNoServiceInstalled(t *testing.T) {
	prev := readUnitStatusForManagedUnit
	defer func() { readUnitStatusForManagedUnit = prev }()
	readUnitStatusForManagedUnit = func(ctx context.Context, unit string) (unitStatus, error) {
		_ = ctx
		switch unit {
		case gopherGatewayUnitName, gopherNodeUnitName:
			return unitStatus{LoadState: "not-found", ActiveState: "inactive"}, nil
		default:
			return unitStatus{}, fmt.Errorf("unexpected unit %q", unit)
		}
	}

	if _, err := resolveManagedServiceUnit(context.Background(), serviceTargetAuto); err == nil {
		t.Fatalf("expected error when neither service is installed")
	}
}

func TestResolveManagedServiceUnitExplicitNodeRequiresInstall(t *testing.T) {
	prev := readUnitStatusForManagedUnit
	defer func() { readUnitStatusForManagedUnit = prev }()
	readUnitStatusForManagedUnit = func(ctx context.Context, unit string) (unitStatus, error) {
		_ = ctx
		switch unit {
		case gopherGatewayUnitName:
			return unitStatus{LoadState: "loaded", ActiveState: "active"}, nil
		case gopherNodeUnitName:
			return unitStatus{LoadState: "not-found", ActiveState: "inactive"}, nil
		default:
			return unitStatus{}, fmt.Errorf("unexpected unit %q", unit)
		}
	}

	if _, err := resolveManagedServiceUnit(context.Background(), serviceTargetNode); err == nil {
		t.Fatalf("expected error when node service is not installed")
	}
}

func TestResolveManagedServiceUnitExplicitNodeWhenInstalled(t *testing.T) {
	prev := readUnitStatusForManagedUnit
	defer func() { readUnitStatusForManagedUnit = prev }()
	readUnitStatusForManagedUnit = func(ctx context.Context, unit string) (unitStatus, error) {
		_ = ctx
		switch unit {
		case gopherGatewayUnitName:
			return unitStatus{LoadState: "loaded", ActiveState: "inactive"}, nil
		case gopherNodeUnitName:
			return unitStatus{LoadState: "loaded", ActiveState: "inactive"}, nil
		default:
			return unitStatus{}, fmt.Errorf("unexpected unit %q", unit)
		}
	}

	unit, err := resolveManagedServiceUnit(context.Background(), serviceTargetNode)
	if err != nil {
		t.Fatalf("resolveManagedServiceUnit(node) error: %v", err)
	}
	if unit != gopherNodeUnitName {
		t.Fatalf("unit = %q, want %q", unit, gopherNodeUnitName)
	}
}
