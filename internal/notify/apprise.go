package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type AppriseSettings struct {
	URL  string `json:"url"`
	Tag  string `json:"tag,omitempty"`
	Urls string `json:"urls,omitempty"`
}

type Apprise struct {
	url    string
	tag    string
	urls   string
	client *http.Client
}

func NewApprise(url, tag, urls string) *Apprise {
	return &Apprise{
		url:    strings.TrimRight(url, "/"),
		tag:    tag,
		urls:   urls,
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

func (a *Apprise) Name() string { return "apprise" }

func (a *Apprise) Send(ctx context.Context, event Event) error {
	title := formatTitle(event.Type)
	body := formatMessage(event)

	// Map Sentinel events to Apprise notification types.
	msgType := "info"
	switch event.Type {
	case EventUpdateSucceeded, EventRollbackOK:
		msgType = "success"
	case EventUpdateFailed, EventRollbackFailed:
		msgType = "failure"
	}

	var endpoint string
	payload := map[string]string{
		"title": title,
		"body":  body,
		"type":  msgType,
	}

	if a.tag != "" {
		endpoint = a.url + "/notify/" + a.tag
	} else {
		endpoint = a.url + "/notify/"
		payload["urls"] = a.urls
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal apprise payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("create apprise request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("send apprise request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("apprise returned %s", resp.Status)
	}
	return nil
}
