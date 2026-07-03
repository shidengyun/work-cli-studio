package emailpoller

import "testing"

func TestParseIssueCommandRequiresChinesePrefix(t *testing.T) {
	cmd, ok := ParseIssueCommand("创建任务：周日去参加晚宴", "查一下周日南京的天气")
	if !ok {
		t.Fatal("expected command")
	}
	if cmd.Title != "周日去参加晚宴" {
		t.Fatalf("title = %q", cmd.Title)
	}
	if cmd.Description != "查一下周日南京的天气" {
		t.Fatalf("description = %q", cmd.Description)
	}
	if _, ok := ParseIssueCommand("周日去参加晚宴", "body"); ok {
		t.Fatal("expected subject without prefix to be ignored")
	}
}

func TestParseMessageMultipartText(t *testing.T) {
	raw := []byte("From: 外部 <outsider@example.com>\r\n" +
		"To: manda@wacai.com\r\n" +
		"Subject: 创建任务：周日去参加晚宴\r\n" +
		"Message-Id: <msg-1@example.com>\r\n" +
		"Content-Type: multipart/alternative; boundary=abc\r\n\r\n" +
		"--abc\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n查一下周日南京的天气\r\n" +
		"--abc\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p>ignored</p>\r\n" +
		"--abc--\r\n")

	msg, err := ParseMessage(raw)
	if err != nil {
		t.Fatalf("ParseMessage: %v", err)
	}
	if msg.From != "outsider@example.com" {
		t.Fatalf("from = %q", msg.From)
	}
	if msg.BodyText() != "查一下周日南京的天气" {
		t.Fatalf("body = %q", msg.BodyText())
	}
}

func TestParseMessageQuotedPrintableBody(t *testing.T) {
	raw := []byte("From: outsider@example.com\r\n" +
		"To: manda@wacai.com\r\n" +
		"Subject: 创建任务：测试\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: quoted-printable\r\n\r\n" +
		"hello=0Aworld\r\n")

	msg, err := ParseMessage(raw)
	if err != nil {
		t.Fatalf("ParseMessage: %v", err)
	}
	if msg.BodyText() != "hello\nworld" {
		t.Fatalf("body = %q", msg.BodyText())
	}
}

func TestLimitUIDsKeepsNewest(t *testing.T) {
	got := limitUIDs([]uint32{1, 2, 3, 4, 5}, 2)
	if len(got) != 2 || got[0] != 4 || got[1] != 5 {
		t.Fatalf("got %#v", got)
	}
}
