package emailpoller

import (
	"context"
	"os"
	"testing"

	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type fakeQueries struct {
	member db.Member
}

func TestConfigFromEnvReadsTLSLegacy(t *testing.T) {
	t.Setenv("MULTICA_EMAIL_POLL_ENABLED", "true")
	t.Setenv("MULTICA_EMAIL_POLL_WORKSPACE_ID", "22222222-2222-2222-2222-222222222222")
	t.Setenv("MULTICA_EMAIL_POLL_CREATOR_USER_ID", "11111111-1111-1111-1111-111111111111")
	t.Setenv("MULTICA_EMAIL_POLL_IMAP_HOST", "mail.example.com")
	t.Setenv("MULTICA_EMAIL_POLL_USERNAME", "manda@example.com")
	t.Setenv("MULTICA_EMAIL_POLL_PASSWORD", "secret")
	t.Setenv("MULTICA_EMAIL_POLL_TLS_LEGACY", "true")
	t.Setenv("SMTP_PASSWORD", "")
	t.Setenv("SMTP_PASSWORD_ENCRYPTED", "")
	t.Setenv("SMTP_PASSWORD_SECRET_KEY", "")
	t.Setenv("SMTP_PASSWORD_SECRET_KEY_FILE", "")

	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
	}
	if !cfg.TLSLegacy {
		t.Fatal("TLSLegacy = false")
	}
}

func TestConfigFromEnvDisabledWithUnsetEnv(t *testing.T) {
	t.Setenv("MULTICA_EMAIL_POLL_ENABLED", "")
	os.Unsetenv("MULTICA_EMAIL_POLL_ENABLED")

	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
	}
	if cfg.Enabled {
		t.Fatal("Enabled = true")
	}
}

func (q fakeQueries) GetMemberByUserAndWorkspace(context.Context, db.GetMemberByUserAndWorkspaceParams) (db.Member, error) {
	return q.member, nil
}

type fakeIssueCreator struct {
	calls  int
	params service.IssueCreateParams
}

func (c *fakeIssueCreator) Create(_ context.Context, p service.IssueCreateParams, _ service.IssueCreateOpts) (service.IssueCreateResult, error) {
	c.calls++
	c.params = p
	return service.IssueCreateResult{Issue: db.Issue{ID: util.MustParseUUID("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")}}, nil
}

type fakeMailClient struct {
	messages []Message
	seen     []uint32
}

func (c *fakeMailClient) FetchUnseen(int) ([]Message, error) {
	return c.messages, nil
}

func (c *fakeMailClient) MarkSeen(seqNums []uint32) error {
	c.seen = append(c.seen, seqNums...)
	return nil
}

func (c *fakeMailClient) Close() error { return nil }

func TestProcessMessageCreatesIssueForPrefixedSubject(t *testing.T) {
	userID := util.MustParseUUID("11111111-1111-1111-1111-111111111111")
	workspaceID := util.MustParseUUID("22222222-2222-2222-2222-222222222222")
	creator := &fakeIssueCreator{}
	raw := []byte("From: outsider@example.com\r\n" +
		"To: manda@wacai.com\r\n" +
		"Subject: 创建任务：周日去参加晚宴\r\n" +
		"Message-Id: <msg-1@example.com>\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n\r\n" +
		"查一下周日南京的天气\r\n")

	handled, err := processMessage(context.Background(), fakeQueries{member: db.Member{UserID: userID}}, creator, Config{
		WorkspaceID:   workspaceID,
		CreatorUserID: userID,
		AllowedTo:     "manda@wacai.com",
	}, Message{UID: 7, Raw: raw})
	if err != nil {
		t.Fatalf("processMessage: %v", err)
	}
	if !handled {
		t.Fatal("expected handled")
	}
	if creator.calls != 1 {
		t.Fatalf("Create calls = %d", creator.calls)
	}
	if creator.params.Title != "周日去参加晚宴" {
		t.Fatalf("title = %q", creator.params.Title)
	}
	if creator.params.Status != "in_progress" {
		t.Fatalf("status = %q", creator.params.Status)
	}
	if creator.params.Description.String == "" || !creator.params.Description.Valid {
		t.Fatalf("description missing: %#v", creator.params.Description)
	}
}

func TestPollOnceMarksOnlyHandledMessagesSeen(t *testing.T) {
	userID := util.MustParseUUID("11111111-1111-1111-1111-111111111111")
	workspaceID := util.MustParseUUID("22222222-2222-2222-2222-222222222222")
	client := &fakeMailClient{messages: []Message{
		{UID: 1, Raw: []byte("From: a@example.com\r\nTo: manda@wacai.com\r\nSubject: 创建任务：A\r\n\r\nbody")},
		{UID: 2, Raw: []byte("From: a@example.com\r\nTo: manda@wacai.com\r\nSubject: hello\r\n\r\nbody")},
	}}
	creator := &fakeIssueCreator{}
	pollOnce(context.Background(), fakeQueries{member: db.Member{UserID: userID}}, creator, Config{
		WorkspaceID:   workspaceID,
		CreatorUserID: userID,
		AllowedTo:     "manda@wacai.com",
		BatchSize:     10,
		ClientFactory: func(context.Context, Config) (MailClient, error) {
			return client, nil
		},
	})
	if creator.calls != 1 {
		t.Fatalf("Create calls = %d", creator.calls)
	}
	if len(client.seen) != 1 || client.seen[0] != 1 {
		t.Fatalf("seen = %#v", client.seen)
	}
}

func TestProcessMessageIgnoresWrongRecipient(t *testing.T) {
	userID := util.MustParseUUID("11111111-1111-1111-1111-111111111111")
	creator := &fakeIssueCreator{}
	raw := []byte("From: outsider@example.com\r\n" +
		"To: other@example.com\r\n" +
		"Subject: 创建任务：周日去参加晚宴\r\n\r\nbody")

	handled, err := processMessage(context.Background(), fakeQueries{member: db.Member{UserID: userID}}, creator, Config{
		CreatorUserID: userID,
		AllowedTo:     "manda@wacai.com",
	}, Message{UID: 7, Raw: raw})
	if err != nil {
		t.Fatalf("processMessage: %v", err)
	}
	if handled || creator.calls != 0 {
		t.Fatalf("expected ignored, handled=%v calls=%d", handled, creator.calls)
	}
}
