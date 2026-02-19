package matrix

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const defaultAvatarContentType = "image/png"

type ManagedAvatar struct {
	MXCURL      string
	ContentType string
	Data        []byte
}

type ManagedAvatarProvider func(ctx context.Context, userID string) (ManagedAvatar, error)

func (t *Transport) ensureManagedUserProfile(ctx context.Context, userID string) error {
	if t == nil || t.avatarProvider == nil {
		return nil
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil
	}
	existingAvatarURL, err := t.getProfileAvatarURL(ctx, userID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(existingAvatarURL) != "" {
		return nil
	}

	avatar, err := t.avatarProvider(ctx, userID)
	if err != nil {
		return err
	}
	mxcURL := strings.TrimSpace(avatar.MXCURL)
	if mxcURL == "" {
		if len(avatar.Data) == 0 {
			return nil
		}
		mxcURL, err = t.uploadAvatar(ctx, userID, avatar)
		if err != nil {
			return err
		}
	}
	return t.setProfileAvatarURL(ctx, userID, mxcURL)
}

func (t *Transport) getProfileAvatarURL(ctx context.Context, userID string) (string, error) {
	endpoint := fmt.Sprintf("%s/_matrix/client/v3/profile/%s/avatar_url?access_token=%s",
		t.homeserverURL,
		url.PathEscape(strings.TrimSpace(userID)),
		url.QueryEscape(t.asToken),
	)
	endpoint += "&user_id=" + url.QueryEscape(strings.TrimSpace(userID))
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("build matrix profile avatar query request: %w", err)
	}
	response, err := t.httpClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("send matrix profile avatar query request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return "", nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(response.Body)
		return "", fmt.Errorf("matrix profile avatar query status: %s body=%s", response.Status, strings.TrimSpace(string(body)))
	}
	payload := struct {
		AvatarURL string `json:"avatar_url"`
	}{}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("decode matrix profile avatar query payload: %w", err)
	}
	return strings.TrimSpace(payload.AvatarURL), nil
}

func (t *Transport) uploadAvatar(ctx context.Context, userID string, avatar ManagedAvatar) (string, error) {
	contentType := strings.TrimSpace(avatar.ContentType)
	if contentType == "" {
		contentType = defaultAvatarContentType
	}
	endpoint := fmt.Sprintf("%s/_matrix/media/v3/upload?access_token=%s",
		t.homeserverURL,
		url.QueryEscape(t.asToken),
	)
	endpoint += "&user_id=" + url.QueryEscape(strings.TrimSpace(userID))
	endpoint += "&filename=" + url.QueryEscape("avatar")
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(avatar.Data))
	if err != nil {
		return "", fmt.Errorf("build matrix avatar upload request: %w", err)
	}
	request.Header.Set("content-type", contentType)
	response, err := t.httpClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("send matrix avatar upload request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(response.Body)
		return "", fmt.Errorf("matrix avatar upload status: %s body=%s", response.Status, strings.TrimSpace(string(body)))
	}
	payload := struct {
		ContentURI string `json:"content_uri"`
	}{}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("decode matrix avatar upload payload: %w", err)
	}
	mxcURL := strings.TrimSpace(payload.ContentURI)
	if mxcURL == "" {
		return "", fmt.Errorf("matrix avatar upload response missing content_uri")
	}
	return mxcURL, nil
}

func (t *Transport) setProfileAvatarURL(ctx context.Context, userID string, avatarURL string) error {
	avatarURL = strings.TrimSpace(avatarURL)
	if avatarURL == "" {
		return nil
	}
	endpoint := fmt.Sprintf("%s/_matrix/client/v3/profile/%s/avatar_url?access_token=%s",
		t.homeserverURL,
		url.PathEscape(strings.TrimSpace(userID)),
		url.QueryEscape(t.asToken),
	)
	endpoint += "&user_id=" + url.QueryEscape(strings.TrimSpace(userID))
	payload := map[string]string{"avatar_url": avatarURL}
	blob, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal matrix profile avatar update payload: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(blob))
	if err != nil {
		return fmt.Errorf("build matrix profile avatar update request: %w", err)
	}
	request.Header.Set("content-type", "application/json")
	response, err := t.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("send matrix profile avatar update request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(response.Body)
		return fmt.Errorf("matrix profile avatar update status: %s body=%s", response.Status, strings.TrimSpace(string(body)))
	}
	return nil
}
