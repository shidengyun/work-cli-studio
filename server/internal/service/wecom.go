package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/multica-ai/multica/server/pkg/redact"
)

const maxWeComTextBytes = 1800

type WeComService struct {
	webhookURL string
	client     *http.Client
}

type weComTextRequest struct {
	MsgType string        `json:"msgtype"`
	Text    weComTextBody `json:"text"`
}

type weComTextBody struct {
	Content string `json:"content"`
}

type weComResponse struct {
	ErrCode int    `json:"errcode"`
	ErrMsg  string `json:"errmsg"`
}

func NewWeComService(webhookURL string, client *http.Client) *WeComService {
	webhookURL = strings.TrimSpace(webhookURL)
	if webhookURL == "" {
		return nil
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &WeComService{webhookURL: webhookURL, client: client}
}

func NewWeComServiceFromEnv() *WeComService {
	webhookURL := strings.TrimSpace(os.Getenv("WECOM_WEBHOOK_URL"))
	if webhookURL == "" {
		webhookURL = strings.TrimSpace(os.Getenv("WECHAT_WORK_WEBHOOK_URL"))
	}
	if webhookURL == "" {
		return nil
	}
	fmt.Println("WeComService: webhook configured")
	return NewWeComService(webhookURL, nil)
}

func (s *WeComService) SendTaskStatusText(ctx context.Context, msg TaskStatusEmail) error {
	if s == nil {
		return nil
	}
	content := buildTaskStatusWeComText(msg)
	payload := weComTextRequest{
		MsgType: "text",
		Text:    weComTextBody{Content: content},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("wecom marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.webhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("wecom request: %s", s.sanitizeErrorText(err))
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("wecom send: %s", s.sanitizeErrorText(err))
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("wecom status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var decoded weComResponse
	if len(respBody) > 0 && json.Unmarshal(respBody, &decoded) == nil && decoded.ErrCode != 0 {
		return fmt.Errorf("wecom errcode %d: %s", decoded.ErrCode, decoded.ErrMsg)
	}
	return nil
}

func (s *WeComService) sanitizeErrorText(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if s == nil || s.webhookURL == "" {
		return msg
	}
	return strings.ReplaceAll(msg, s.webhookURL, "[redacted-wecom-webhook]")
}

func buildTaskStatusWeComText(msg TaskStatusEmail) string {
	lines := []string{
		"Multica task " + strings.TrimSpace(msg.Status),
		"",
		"Workspace: " + strings.TrimSpace(msg.WorkspaceName),
		"Issue: " + strings.TrimSpace(msg.IssueTitle),
	}
	if strings.TrimSpace(msg.IssueURL) != "" {
		lines = append(lines, "Open: "+strings.TrimSpace(msg.IssueURL))
	}
	if strings.TrimSpace(msg.Error) != "" {
		lines = append(lines, "", "Error:", redact.Text(strings.TrimSpace(msg.Error)))
	}
	if strings.TrimSpace(msg.Result) != "" {
		lines = append(lines, "", "Result:", redact.Text(strings.TrimSpace(msg.Result)))
	}
	return truncateUTF8Bytes(strings.Join(lines, "\n"), maxWeComTextBytes)
}

func truncateUTF8Bytes(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}

	suffix := "\n\n[truncated]"
	limit := maxBytes - len(suffix)
	if limit <= 0 {
		return suffix[:maxBytes]
	}

	out := make([]byte, 0, limit)
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		if r == utf8.RuneError && size == 0 {
			break
		}
		if len(out)+size > limit {
			break
		}
		out = append(out, s[:size]...)
		s = s[size:]
	}
	return string(out) + suffix
}
