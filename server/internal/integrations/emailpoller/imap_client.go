package emailpoller

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

type Message struct {
	UID uint32
	Raw []byte
}

type MailClient interface {
	FetchUnseen(limit int) ([]Message, error)
	MarkSeen(seqNums []uint32) error
	Close() error
}

type DialerConfig struct {
	Host        string
	Port        string
	Username    string
	Password    string
	Mailbox     string
	TLSMode     string
	TLSInsecure bool
	TLSLegacy   bool
	Timeout     time.Duration
}

type IMAPClient struct {
	conn net.Conn
	r    *bufio.Reader
	tag  int
}

func DialIMAP(cfg DialerConfig) (*IMAPClient, error) {
	if cfg.Host == "" {
		return nil, fmt.Errorf("imap host is required")
	}
	if cfg.Port == "" {
		cfg.Port = "993"
	}
	if cfg.Mailbox == "" {
		cfg.Mailbox = "INBOX"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	addr := net.JoinHostPort(cfg.Host, cfg.Port)
	tlsCfg := &tls.Config{
		ServerName:         cfg.Host,
		InsecureSkipVerify: cfg.TLSInsecure, //nolint:gosec // opt-in for private mail servers
	}
	if cfg.TLSLegacy {
		tlsCfg.MaxVersion = tls.VersionTLS12
		tlsCfg.CipherSuites = []uint16{
			tls.TLS_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_RSA_WITH_AES_256_CBC_SHA,
			tls.TLS_RSA_WITH_AES_128_CBC_SHA,
		}
	}
	var conn net.Conn
	var err error
	if strings.EqualFold(cfg.TLSMode, "starttls") {
		conn, err = net.DialTimeout("tcp", addr, cfg.Timeout)
	} else {
		dialer := &net.Dialer{Timeout: cfg.Timeout}
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, tlsCfg)
	}
	if err != nil {
		return nil, fmt.Errorf("imap dial %s: %w", addr, err)
	}
	if err := conn.SetDeadline(time.Now().Add(cfg.Timeout)); err != nil {
		conn.Close()
		return nil, err
	}
	c := &IMAPClient{conn: conn, r: bufio.NewReader(conn)}
	if _, err := c.r.ReadString('\n'); err != nil {
		conn.Close()
		return nil, fmt.Errorf("imap greeting: %w", err)
	}
	if strings.EqualFold(cfg.TLSMode, "starttls") {
		if _, err := c.command("STARTTLS"); err != nil {
			conn.Close()
			return nil, fmt.Errorf("imap STARTTLS: %w", err)
		}
		tlsConn := tls.Client(conn, tlsCfg)
		if err := tlsConn.Handshake(); err != nil {
			conn.Close()
			return nil, fmt.Errorf("imap tls handshake: %w", err)
		}
		c.conn = tlsConn
		c.r = bufio.NewReader(tlsConn)
	}
	if cfg.Username != "" {
		if _, err := c.command("LOGIN " + quoteIMAP(cfg.Username) + " " + quoteIMAP(cfg.Password)); err != nil {
			conn.Close()
			return nil, fmt.Errorf("imap LOGIN: %w", err)
		}
	}
	if _, err := c.command("SELECT " + quoteMailbox(cfg.Mailbox)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("imap SELECT: %w", err)
	}
	return c, nil
}

func (c *IMAPClient) FetchUnseen(limit int) ([]Message, error) {
	lines, err := c.command("UID SEARCH UNSEEN")
	if err != nil {
		return nil, err
	}
	seqs := parseSearchSeqs(lines)
	seqs = limitUIDs(seqs, limit)
	messages := make([]Message, 0, len(seqs))
	for _, uid := range seqs {
		raw, err := c.fetchRFC822(uid)
		if err != nil {
			return messages, err
		}
		messages = append(messages, Message{UID: uid, Raw: raw})
	}
	return messages, nil
}

func limitUIDs(uids []uint32, limit int) []uint32 {
	if limit > 0 && len(uids) > limit {
		return uids[len(uids)-limit:]
	}
	return uids
}

func (c *IMAPClient) MarkSeen(seqNums []uint32) error {
	if len(seqNums) == 0 {
		return nil
	}
	parts := make([]string, len(seqNums))
	for i, uid := range seqNums {
		parts[i] = strconv.FormatUint(uint64(uid), 10)
	}
	_, err := c.command("UID STORE " + strings.Join(parts, ",") + " +FLAGS.SILENT (\\Seen)")
	return err
}

func (c *IMAPClient) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	_, _ = c.command("LOGOUT")
	return c.conn.Close()
}

func (c *IMAPClient) fetchRFC822(uid uint32) ([]byte, error) {
	tag := c.nextTag()
	cmd := fmt.Sprintf("%s UID FETCH %d (BODY.PEEK[])\r\n", tag, uid)
	if _, err := io.WriteString(c.conn, cmd); err != nil {
		return nil, err
	}
	var raw []byte
	for {
		line, err := c.r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(trimmed, tag+" ") {
			if strings.Contains(strings.ToUpper(trimmed), " OK") {
				return raw, nil
			}
			return nil, fmt.Errorf("imap command failed: %s", trimmed)
		}
		start := strings.LastIndex(trimmed, "{")
		if start < 0 || !strings.HasSuffix(trimmed, "}") {
			continue
		}
		sizeText := strings.TrimSuffix(trimmed[start+1:], "}")
		size, err := strconv.Atoi(sizeText)
		if err != nil || size < 0 {
			continue
		}
		raw = make([]byte, size)
		if _, err := io.ReadFull(c.r, raw); err != nil {
			return nil, err
		}
		// Consume the CRLF after the literal and the closing FETCH line.
		if _, err := c.r.ReadString('\n'); err != nil {
			return nil, err
		}
	}
}

func (c *IMAPClient) command(cmd string) ([]string, error) {
	tag := c.nextTag()
	if _, err := io.WriteString(c.conn, tag+" "+cmd+"\r\n"); err != nil {
		return nil, err
	}
	var lines []string
	for {
		line, err := c.r.ReadString('\n')
		if err != nil {
			return lines, err
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(trimmed, tag+" ") {
			if strings.Contains(strings.ToUpper(trimmed), " OK") {
				return lines, nil
			}
			return lines, fmt.Errorf("imap command failed: %s", trimmed)
		}
		lines = append(lines, trimmed)
	}
}

func (c *IMAPClient) nextTag() string {
	c.tag++
	return fmt.Sprintf("A%04d", c.tag)
}

func parseSearchSeqs(lines []string) []uint32 {
	var seqs []uint32
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) == 0 || strings.ToUpper(fields[0]) != "*" {
			continue
		}
		if len(fields) < 2 || strings.ToUpper(fields[1]) != "SEARCH" {
			continue
		}
		for _, field := range fields[2:] {
			n, err := strconv.ParseUint(field, 10, 32)
			if err == nil && n > 0 {
				seqs = append(seqs, uint32(n))
			}
		}
	}
	return seqs
}

func quoteMailbox(s string) string {
	if s == "INBOX" {
		return s
	}
	return quoteIMAP(s)
}

func quoteIMAP(s string) string {
	escaped := strings.ReplaceAll(s, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `"` + escaped + `"`
}
