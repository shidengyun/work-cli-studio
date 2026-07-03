package emailpoller

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type Queries interface {
	GetMemberByUserAndWorkspace(ctx context.Context, arg db.GetMemberByUserAndWorkspaceParams) (db.Member, error)
	ListAgents(ctx context.Context, workspaceID pgtype.UUID) ([]db.Agent, error)
}

type IssueCreator interface {
	Create(ctx context.Context, p service.IssueCreateParams, opts service.IssueCreateOpts) (service.IssueCreateResult, error)
}

type Config struct {
	Enabled       bool
	Host          string
	Port          string
	Username      string
	Password      string
	Mailbox       string
	TLSMode       string
	TLSInsecure   bool
	TLSLegacy     bool
	Interval      time.Duration
	BatchSize     int
	WorkspaceID   pgtype.UUID
	CreatorUserID pgtype.UUID
	AssigneeType  pgtype.Text
	AssigneeID    pgtype.UUID
	AllowedTo     string
	ClientFactory func(context.Context, Config) (MailClient, error)
}

func ConfigFromEnv() (Config, error) {
	if os.Getenv("MULTICA_EMAIL_POLL_ENABLED") != "true" {
		return Config{}, nil
	}
	workspaceID, err := requiredUUIDEnv("MULTICA_EMAIL_POLL_WORKSPACE_ID")
	if err != nil {
		return Config{}, err
	}
	creatorUserID, err := requiredUUIDEnv("MULTICA_EMAIL_POLL_CREATOR_USER_ID")
	if err != nil {
		return Config{}, err
	}
	password, err := emailPollPasswordFromEnv()
	if err != nil {
		return Config{}, err
	}
	host := strings.TrimSpace(os.Getenv("MULTICA_EMAIL_POLL_IMAP_HOST"))
	if host == "" {
		host = strings.TrimSpace(os.Getenv("SMTP_HOST"))
	}
	if host == "" {
		return Config{}, errors.New("MULTICA_EMAIL_POLL_IMAP_HOST is required")
	}
	username := strings.TrimSpace(os.Getenv("MULTICA_EMAIL_POLL_USERNAME"))
	if username == "" {
		username = strings.TrimSpace(os.Getenv("SMTP_USERNAME"))
	}
	if username == "" {
		return Config{}, errors.New("MULTICA_EMAIL_POLL_USERNAME is required")
	}
	interval := parseDurationEnv("MULTICA_EMAIL_POLL_INTERVAL", time.Minute)
	batchSize := parsePositiveIntEnv("MULTICA_EMAIL_POLL_BATCH_SIZE", 10)
	tlsMode := strings.ToLower(strings.TrimSpace(os.Getenv("MULTICA_EMAIL_POLL_TLS")))
	if tlsMode == "" {
		tlsMode = "implicit"
	}
	port := strings.TrimSpace(os.Getenv("MULTICA_EMAIL_POLL_IMAP_PORT"))
	if port == "" {
		if tlsMode == "starttls" {
			port = "143"
		} else {
			port = "993"
		}
	}

	var assigneeID pgtype.UUID
	var assigneeType pgtype.Text
	assigneeTypeText := strings.TrimSpace(os.Getenv("MULTICA_EMAIL_POLL_DEFAULT_ASSIGNEE_TYPE"))
	assigneeIDText := strings.TrimSpace(os.Getenv("MULTICA_EMAIL_POLL_DEFAULT_ASSIGNEE_ID"))
	if assigneeTypeText != "" || assigneeIDText != "" {
		if assigneeTypeText == "" || assigneeIDText == "" {
			return Config{}, errors.New("MULTICA_EMAIL_POLL_DEFAULT_ASSIGNEE_TYPE and MULTICA_EMAIL_POLL_DEFAULT_ASSIGNEE_ID must be set together")
		}
		assigneeID, err = util.ParseUUID(assigneeIDText)
		if err != nil {
			return Config{}, fmt.Errorf("invalid MULTICA_EMAIL_POLL_DEFAULT_ASSIGNEE_ID: %w", err)
		}
		assigneeType = pgtype.Text{String: assigneeTypeText, Valid: true}
	}

	return Config{
		Enabled:       true,
		Host:          host,
		Port:          port,
		Username:      username,
		Password:      password,
		Mailbox:       firstNonEmpty(os.Getenv("MULTICA_EMAIL_POLL_MAILBOX"), "INBOX"),
		TLSMode:       tlsMode,
		TLSInsecure:   os.Getenv("MULTICA_EMAIL_POLL_TLS_INSECURE") == "true",
		TLSLegacy:     os.Getenv("MULTICA_EMAIL_POLL_TLS_LEGACY") == "true",
		Interval:      interval,
		BatchSize:     batchSize,
		WorkspaceID:   workspaceID,
		CreatorUserID: creatorUserID,
		AssigneeType:  assigneeType,
		AssigneeID:    assigneeID,
		AllowedTo:     strings.ToLower(strings.TrimSpace(os.Getenv("MULTICA_EMAIL_POLL_ALLOWED_TO"))),
	}, nil
}

func Run(ctx context.Context, queries Queries, issueCreator IssueCreator, cfg Config) {
	if !cfg.Enabled {
		return
	}
	if cfg.Interval <= 0 {
		cfg.Interval = time.Minute
	}
	slog.Info("email poller starting", "host", cfg.Host, "username", cfg.Username, "mailbox", cfg.Mailbox, "interval", cfg.Interval.String())
	pollOnce(ctx, queries, issueCreator, cfg)
	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("email poller stopped")
			return
		case <-ticker.C:
			pollOnce(ctx, queries, issueCreator, cfg)
		}
	}
}

func pollOnce(ctx context.Context, queries Queries, issueCreator IssueCreator, cfg Config) {
	client, err := newMailClient(ctx, cfg)
	if err != nil {
		slog.Warn("email poller: connect failed", "error", err)
		return
	}
	defer client.Close()
	messages, err := client.FetchUnseen(cfg.BatchSize)
	if err != nil {
		slog.Warn("email poller: fetch unseen failed", "error", err)
		return
	}
	var seen []uint32
	for _, msg := range messages {
		handled, err := processMessage(ctx, queries, issueCreator, cfg, msg)
		if err != nil {
			slog.Warn("email poller: process message failed", "uid", msg.UID, "error", err)
			continue
		}
		if handled {
			seen = append(seen, msg.UID)
		}
	}
	if err := client.MarkSeen(seen); err != nil {
		slog.Warn("email poller: mark seen failed", "error", err)
	}
}

func processMessage(ctx context.Context, queries Queries, issueCreator IssueCreator, cfg Config, msg Message) (bool, error) {
	parsed, err := ParseMessage(msg.Raw)
	if err != nil {
		return false, err
	}
	if cfg.AllowedTo != "" && !messageMatchesAllowedTo(msg.Raw, cfg.AllowedTo) {
		return false, nil
	}
	cmd, ok := ParseIssueCommand(parsed.Subject, parsed.BodyText())
	if !ok {
		return false, nil
	}
	member, err := queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		UserID:      cfg.CreatorUserID,
		WorkspaceID: cfg.WorkspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, errors.New("configured creator is not a workspace member")
		}
		return false, err
	}
	assigneeType, assigneeID, err := resolveAssignee(ctx, queries, cfg)
	if err != nil {
		return false, err
	}
	description := buildIssueDescription(cmd.Description, parsed)
	res, err := issueCreator.Create(ctx, service.IssueCreateParams{
		WorkspaceID:    cfg.WorkspaceID,
		Title:          cmd.Title,
		Description:    pgtype.Text{String: description, Valid: description != ""},
		Status:         "in_progress",
		Priority:       "none",
		AssigneeType:   assigneeType,
		AssigneeID:     assigneeID,
		CreatorType:    "member",
		CreatorID:      member.UserID,
		AllowDuplicate: false,
	}, service.IssueCreateOpts{
		ActorID:  util.UUIDToString(member.UserID),
		Platform: "email",
	})
	if errors.Is(err, service.ErrActiveDuplicate) {
		if res.DuplicateIssue != nil {
			slog.Info("email poller: duplicate issue ignored", "uid", msg.UID, "title", cmd.Title)
		}
		return true, nil
	}
	if err != nil {
		return false, err
	}
	slog.Info("email poller: issue created", "issue_id", util.UUIDToString(res.Issue.ID), "title", cmd.Title, "from", parsed.From)
	return true, nil
}

func resolveAssignee(ctx context.Context, queries Queries, cfg Config) (pgtype.Text, pgtype.UUID, error) {
	if cfg.AssigneeType.Valid && cfg.AssigneeID.Valid {
		return cfg.AssigneeType, cfg.AssigneeID, nil
	}
	if cfg.AssigneeType.Valid != cfg.AssigneeID.Valid {
		return pgtype.Text{}, pgtype.UUID{}, errors.New("configured default assignee type and id must be set together")
	}

	agents, err := queries.ListAgents(ctx, cfg.WorkspaceID)
	if err != nil {
		return pgtype.Text{}, pgtype.UUID{}, fmt.Errorf("list workspace agents: %w", err)
	}
	for _, agent := range agents {
		if agent.RuntimeID.Valid {
			return pgtype.Text{String: "agent", Valid: true}, agent.ID, nil
		}
	}
	if len(agents) == 0 {
		return pgtype.Text{}, pgtype.UUID{}, errors.New("workspace has no active agents")
	}
	return pgtype.Text{}, pgtype.UUID{}, errors.New("workspace has no active agents with a runtime")
}

func newMailClient(ctx context.Context, cfg Config) (MailClient, error) {
	if cfg.ClientFactory != nil {
		return cfg.ClientFactory(ctx, cfg)
	}
	return DialIMAP(DialerConfig{
		Host:        cfg.Host,
		Port:        cfg.Port,
		Username:    cfg.Username,
		Password:    cfg.Password,
		Mailbox:     cfg.Mailbox,
		TLSMode:     cfg.TLSMode,
		TLSInsecure: cfg.TLSInsecure,
		TLSLegacy:   cfg.TLSLegacy,
		Timeout:     30 * time.Second,
	})
}

func buildIssueDescription(body string, email ParsedEmail) string {
	parts := make([]string, 0, 3)
	if strings.TrimSpace(body) != "" {
		parts = append(parts, strings.TrimSpace(body))
	}
	if summary := attachmentSummary(email.Attachments); summary != "" {
		parts = append(parts, summary)
	}
	source := []string{"来源：邮件"}
	if email.From != "" {
		source = append(source, "发件人："+email.From)
	}
	if email.MessageID != "" {
		source = append(source, "Message-ID："+email.MessageID)
	}
	parts = append(parts, strings.Join(source, "\n"))
	return strings.Join(parts, "\n\n")
}

func messageMatchesAllowedTo(raw []byte, allowed string) bool {
	msg, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		return false
	}
	for _, key := range []string{"To", "Cc", "Delivered-To", "X-Original-To"} {
		value := msg.Header.Get(key)
		for _, part := range strings.Split(value, ",") {
			if extractAddress(part) == allowed {
				return true
			}
		}
	}
	return false
}

func emailPollPasswordFromEnv() (string, error) {
	if password := os.Getenv("MULTICA_EMAIL_POLL_PASSWORD"); password != "" {
		return password, nil
	}
	if encrypted := strings.TrimSpace(os.Getenv("MULTICA_EMAIL_POLL_PASSWORD_ENCRYPTED")); encrypted != "" {
		return decryptPassword(encrypted, "MULTICA_EMAIL_POLL_PASSWORD_SECRET_KEY", "MULTICA_EMAIL_POLL_PASSWORD_SECRET_KEY_FILE")
	}
	if password := os.Getenv("SMTP_PASSWORD"); password != "" {
		return password, nil
	}
	if encrypted := strings.TrimSpace(os.Getenv("SMTP_PASSWORD_ENCRYPTED")); encrypted != "" {
		return decryptPassword(encrypted, "SMTP_PASSWORD_SECRET_KEY", "SMTP_PASSWORD_SECRET_KEY_FILE")
	}
	return "", errors.New("MULTICA_EMAIL_POLL_PASSWORD or SMTP_PASSWORD is required")
}

func decryptPassword(encrypted, keyEnv, keyFileEnv string) (string, error) {
	key, err := passwordSecretKey(keyEnv, keyFileEnv)
	if err != nil {
		return "", err
	}
	box, err := secretbox.New(key)
	if err != nil {
		return "", err
	}
	sealed, err := base64.StdEncoding.DecodeString(encrypted)
	if err != nil {
		return "", fmt.Errorf("%s is not valid base64: %w", keyEnv, err)
	}
	plain, err := box.Open(sealed)
	if err != nil {
		return "", fmt.Errorf("encrypted email poll password decrypt failed: %w", err)
	}
	return string(plain), nil
}

func passwordSecretKey(envVar, fileEnvVar string) ([]byte, error) {
	if strings.TrimSpace(os.Getenv(envVar)) != "" {
		return secretbox.LoadKey(envVar)
	}
	path := strings.TrimSpace(os.Getenv(fileEnvVar))
	if path == "" {
		return nil, fmt.Errorf("%s or %s is required when encrypted password is set", envVar, fileEnvVar)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", fileEnvVar, err)
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("%s is not valid base64: %w", fileEnvVar, err)
	}
	if len(key) != secretbox.KeySize {
		return nil, fmt.Errorf("%s decodes to %d bytes, expected %d", fileEnvVar, len(key), secretbox.KeySize)
	}
	return key, nil
}

func requiredUUIDEnv(name string) (pgtype.UUID, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return pgtype.UUID{}, fmt.Errorf("%s is required", name)
	}
	id, err := util.ParseUUID(raw)
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("invalid %s: %w", name, err)
	}
	return id, nil
}

func parseDurationEnv(name string, def time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def
	}
	v, err := time.ParseDuration(raw)
	if err != nil || v <= 0 {
		slog.Warn("invalid env var, using default", "name", name, "value", raw, "default", def.String(), "error", err)
		return def
	}
	return v
}

func parsePositiveIntEnv(name string, def int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		slog.Warn("invalid env var, using default", "name", name, "value", raw, "default", def, "error", err)
		return def
	}
	return n
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
