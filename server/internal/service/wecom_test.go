package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestWeComService_SendTaskStatusText(t *testing.T) {
	var got weComTextRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	defer server.Close()

	svc := NewWeComService(server.URL, server.Client())
	err := svc.SendTaskStatusText(context.Background(), TaskStatusEmail{
		WorkspaceName: "Acme",
		IssueTitle:    "Ship importer",
		IssueURL:      "https://app.example/acme/issues/1",
		Status:        "completed",
		Result:        "Imported 12 customers",
	})
	if err != nil {
		t.Fatalf("SendTaskStatusText: %v", err)
	}

	if got.MsgType != "text" {
		t.Fatalf("msgtype = %q, want text", got.MsgType)
	}
	for _, want := range []string{
		"Multica task completed",
		"Workspace: Acme",
		"Issue: Ship importer",
		"Open: https://app.example/acme/issues/1",
		"Imported 12 customers",
	} {
		if !strings.Contains(got.Text.Content, want) {
			t.Fatalf("content missing %q:\n%s", want, got.Text.Content)
		}
	}
}

func TestWeComService_SendTaskStatusTextErrCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"errcode":93000,"errmsg":"bad webhook"}`))
	}))
	defer server.Close()

	svc := NewWeComService(server.URL, server.Client())
	err := svc.SendTaskStatusText(context.Background(), TaskStatusEmail{
		WorkspaceName: "Acme",
		IssueTitle:    "Ship importer",
		Status:        "completed",
	})
	if err == nil || !strings.Contains(err.Error(), "wecom errcode 93000") {
		t.Fatalf("expected wecom errcode error, got %v", err)
	}
}

func TestWeComService_RedactsWebhookURLFromTransportErrors(t *testing.T) {
	svc := NewWeComService("wecom://example.invalid/cgi-bin/webhook/send?key=secret-key", nil)
	err := svc.SendTaskStatusText(context.Background(), TaskStatusEmail{
		WorkspaceName: "Acme",
		IssueTitle:    "Ship importer",
		Status:        "completed",
	})
	if err == nil {
		t.Fatal("expected transport error")
	}
	if strings.Contains(err.Error(), "secret-key") {
		t.Fatalf("error leaked webhook key: %v", err)
	}
	if !strings.Contains(err.Error(), "[redacted-wecom-webhook]") {
		t.Fatalf("expected redacted webhook marker, got %v", err)
	}
}

func TestBuildTaskStatusWeComTextTruncatesUTF8(t *testing.T) {
	got := buildTaskStatusWeComText(TaskStatusEmail{
		WorkspaceName: "Acme",
		IssueTitle:    "Ship importer",
		Status:        "completed",
		Result:        strings.Repeat("深", maxWeComTextBytes),
	})
	if len(got) > maxWeComTextBytes {
		t.Fatalf("content length = %d, want <= %d", len(got), maxWeComTextBytes)
	}
	if !strings.Contains(got, "[truncated]") {
		t.Fatalf("expected truncated marker, got:\n%s", got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("truncated content is not valid UTF-8")
	}
}
