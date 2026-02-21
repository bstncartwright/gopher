package matrix

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/bstncartwright/gopher/pkg/transport"
)

type CreatePublicRoomOptions struct {
	Name          string
	Topic         string
	CreatorUserID string
	InviteUserIDs []string
}

func (t *Transport) CreatePublicRoom(ctx context.Context, opts CreatePublicRoomOptions) (string, error) {
	return t.createRoom(ctx, createRoomOptions{
		Name:          opts.Name,
		Topic:         opts.Topic,
		CreatorUserID: opts.CreatorUserID,
		InviteUserIDs: opts.InviteUserIDs,
		Visibility:    "public",
		Preset:        "public_chat",
	})
}

type CreatePrivateRoomOptions struct {
	Name          string
	Topic         string
	CreatorUserID string
	InviteUserIDs []string
}

func (t *Transport) CreatePrivateRoom(ctx context.Context, opts CreatePrivateRoomOptions) (string, error) {
	return t.createRoom(ctx, createRoomOptions{
		Name:          opts.Name,
		Topic:         opts.Topic,
		CreatorUserID: opts.CreatorUserID,
		InviteUserIDs: opts.InviteUserIDs,
		Visibility:    "private",
		Preset:        "private_chat",
	})
}

type createRoomOptions struct {
	Name          string
	Topic         string
	CreatorUserID string
	InviteUserIDs []string
	Visibility    string
	Preset        string
}

func (t *Transport) createRoom(ctx context.Context, opts createRoomOptions) (string, error) {
	endpoint := fmt.Sprintf("%s/_matrix/client/v3/createRoom?access_token=%s",
		t.homeserverURL,
		url.QueryEscape(t.asToken),
	)
	creatorUserID := strings.TrimSpace(opts.CreatorUserID)
	if creatorUserID != "" {
		endpoint += "&user_id=" + url.QueryEscape(creatorUserID)
	}
	invitees := normalizedUniqueUserIDs(opts.InviteUserIDs, creatorUserID)
	payload := map[string]any{
		"visibility": strings.TrimSpace(opts.Visibility),
		"preset":     strings.TrimSpace(opts.Preset),
		"is_direct":  false,
	}
	if name := strings.TrimSpace(opts.Name); name != "" {
		payload["name"] = name
	}
	if topic := strings.TrimSpace(opts.Topic); topic != "" {
		payload["topic"] = topic
	}
	if len(invitees) > 0 {
		payload["invite"] = invitees
	}
	blob, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal matrix create room payload: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(blob))
	if err != nil {
		return "", fmt.Errorf("build matrix create room request: %w", err)
	}
	request.Header.Set("content-type", "application/json")
	response, err := t.httpClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("send matrix create room request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(response.Body)
		return "", fmt.Errorf("matrix create room status: %s body=%s", response.Status, strings.TrimSpace(string(body)))
	}
	decoded := struct {
		RoomID string `json:"room_id"`
	}{}
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		return "", fmt.Errorf("decode matrix create room response: %w", err)
	}
	roomID := strings.TrimSpace(decoded.RoomID)
	if roomID == "" {
		return "", fmt.Errorf("matrix create room response missing room_id")
	}
	if creatorUserID != "" {
		t.setRoomManagedUser(roomID, creatorUserID)
	}
	return roomID, nil
}

func (t *Transport) InviteToRoom(ctx context.Context, roomID string, inviteeUserID string, asUserID string) error {
	roomID = strings.TrimSpace(roomID)
	inviteeUserID = strings.TrimSpace(inviteeUserID)
	asUserID = strings.TrimSpace(asUserID)
	if roomID == "" {
		return fmt.Errorf("room id is required")
	}
	if inviteeUserID == "" {
		return fmt.Errorf("invitee user id is required")
	}
	endpoint := fmt.Sprintf("%s/_matrix/client/v3/rooms/%s/invite?access_token=%s",
		t.homeserverURL,
		url.PathEscape(roomID),
		url.QueryEscape(t.asToken),
	)
	if asUserID != "" {
		endpoint += "&user_id=" + url.QueryEscape(asUserID)
	}
	payload := map[string]string{
		"user_id": inviteeUserID,
	}
	blob, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal matrix invite payload: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(blob))
	if err != nil {
		return fmt.Errorf("build matrix invite request: %w", err)
	}
	request.Header.Set("content-type", "application/json")
	response, err := t.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("send matrix invite request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(response.Body)
		return fmt.Errorf("matrix invite status: %s body=%s", response.Status, strings.TrimSpace(string(body)))
	}
	if t.isManagedUser(inviteeUserID) {
		t.setRoomManagedUser(roomID, inviteeUserID)
	}
	return nil
}

func (t *Transport) SendMessageAs(ctx context.Context, roomID string, senderUserID string, text string) error {
	roomID = strings.TrimSpace(roomID)
	senderUserID = strings.TrimSpace(senderUserID)
	text = strings.TrimSpace(text)
	if roomID == "" {
		return fmt.Errorf("room id is required")
	}
	if text == "" {
		return nil
	}
	return t.sendMessageNow(ctx, transport.OutboundMessage{
		ConversationID: roomID,
		SenderID:       senderUserID,
		Text:           text,
	})
}

func normalizedUniqueUserIDs(values []string, exclude string) []string {
	exclude = strings.TrimSpace(exclude)
	seen := map[string]struct{}{}
	for _, value := range values {
		userID := strings.TrimSpace(value)
		if userID == "" || userID == exclude {
			continue
		}
		seen[userID] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for userID := range seen {
		out = append(out, userID)
	}
	sort.Strings(out)
	return out
}
