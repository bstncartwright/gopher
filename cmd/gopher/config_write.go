package main

import (
	"fmt"
	"os"
)

var (
	configFileStat   = os.Stat
	configFileWrite  = os.WriteFile
	configFileRename = os.Rename
	configFileRemove = os.Remove
)

func writeConfigFileWithBackup(target string, content []byte) error {
	info, err := configFileStat(target)
	if err != nil {
		if os.IsNotExist(err) {
			if err := configFileWrite(target, content, 0o644); err != nil {
				return fmt.Errorf("write config file %s: %w", target, err)
			}
			return nil
		}
		return fmt.Errorf("stat config file %s: %w", target, err)
	}
	if info.IsDir() {
		return fmt.Errorf("config path %s is a directory", target)
	}

	mode := info.Mode().Perm()
	if mode == 0 {
		mode = 0o644
	}
	backupPath := target + ".bak"
	if err := configFileRemove(backupPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove existing backup %s: %w", backupPath, err)
	}
	if err := configFileRename(target, backupPath); err != nil {
		return fmt.Errorf("backup config file %s: %w", target, err)
	}
	if err := configFileWrite(target, content, mode); err != nil {
		restoreErr := restoreConfigBackup(target, backupPath)
		if restoreErr != nil {
			return fmt.Errorf("write config file %s: %w (restore backup: %v)", target, err, restoreErr)
		}
		return fmt.Errorf("write config file %s: %w", target, err)
	}
	_ = configFileRemove(backupPath)
	return nil
}

func restoreConfigBackup(target, backupPath string) error {
	if err := configFileRemove(target); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove failed config file %s: %w", target, err)
	}
	if err := configFileRename(backupPath, target); err != nil {
		return fmt.Errorf("restore backup file %s: %w", backupPath, err)
	}
	return nil
}
