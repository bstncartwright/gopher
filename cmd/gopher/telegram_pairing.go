package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type telegramPairRequest struct {
	ChatID         string `json:"chat_id"`
	UserID         string `json:"user_id"`
	Conversation   string `json:"conversation_name"`
	SenderUsername string `json:"sender_username"`
	RequestedAt    string `json:"requested_at"`
	LastSeenAt     string `json:"last_seen_at"`
}

type telegramPairingState struct {
	PairedChatID string               `json:"paired_chat_id"`
	PairedUserID string               `json:"paired_user_id"`
	Pending      *telegramPairRequest `json:"pending,omitempty"`
}

func resolveTelegramPairingPath(dataDir string) string {
	dataDir = filepath.Clean(strings.TrimSpace(dataDir))
	if dataDir == "" {
		return filepath.Join(".gopher", "telegram", "pairing.json")
	}
	return filepath.Join(dataDir, "telegram", "pairing.json")
}

func readTelegramPairingState(dataDir string) (telegramPairingState, error) {
	path := resolveTelegramPairingPath(dataDir)
	blob, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return telegramPairingState{}, nil
		}
		return telegramPairingState{}, fmt.Errorf("read telegram pairing state %s: %w", path, err)
	}
	var state telegramPairingState
	if err := json.Unmarshal(blob, &state); err != nil {
		return telegramPairingState{}, fmt.Errorf("parse telegram pairing state %s: %w", path, err)
	}
	state.PairedChatID = strings.TrimSpace(state.PairedChatID)
	state.PairedUserID = strings.TrimSpace(state.PairedUserID)
	if state.Pending != nil {
		state.Pending.ChatID = strings.TrimSpace(state.Pending.ChatID)
		state.Pending.UserID = strings.TrimSpace(state.Pending.UserID)
		state.Pending.Conversation = strings.TrimSpace(state.Pending.Conversation)
		state.Pending.SenderUsername = strings.TrimSpace(state.Pending.SenderUsername)
		state.Pending.RequestedAt = strings.TrimSpace(state.Pending.RequestedAt)
		state.Pending.LastSeenAt = strings.TrimSpace(state.Pending.LastSeenAt)
		if state.Pending.ChatID == "" || state.Pending.UserID == "" {
			state.Pending = nil
		}
	}
	return state, nil
}

func writeTelegramPairingState(dataDir string, state telegramPairingState) error {
	path := resolveTelegramPairingPath(dataDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create telegram pairing dir %s: %w", filepath.Dir(path), err)
	}
	state.PairedChatID = strings.TrimSpace(state.PairedChatID)
	state.PairedUserID = strings.TrimSpace(state.PairedUserID)
	if state.Pending != nil {
		state.Pending.ChatID = strings.TrimSpace(state.Pending.ChatID)
		state.Pending.UserID = strings.TrimSpace(state.Pending.UserID)
		state.Pending.Conversation = strings.TrimSpace(state.Pending.Conversation)
		state.Pending.SenderUsername = strings.TrimSpace(state.Pending.SenderUsername)
		state.Pending.RequestedAt = strings.TrimSpace(state.Pending.RequestedAt)
		state.Pending.LastSeenAt = strings.TrimSpace(state.Pending.LastSeenAt)
		if state.Pending.ChatID == "" || state.Pending.UserID == "" {
			state.Pending = nil
		}
	}
	blob, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal telegram pairing state %s: %w", path, err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(blob, '\n'), 0o600); err != nil {
		return fmt.Errorf("write telegram pairing state %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename telegram pairing state %s: %w", path, err)
	}
	return nil
}

func upsertPendingTelegramPairing(dataDir string, request telegramPairRequest) error {
	state, err := readTelegramPairingState(dataDir)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	request.ChatID = strings.TrimSpace(request.ChatID)
	request.UserID = strings.TrimSpace(request.UserID)
	if request.ChatID == "" || request.UserID == "" {
		return nil
	}
	request.RequestedAt = strings.TrimSpace(request.RequestedAt)
	request.LastSeenAt = now
	if request.RequestedAt == "" {
		request.RequestedAt = now
	}
	state.Pending = &request
	return writeTelegramPairingState(dataDir, state)
}
