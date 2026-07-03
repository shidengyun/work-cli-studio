package emailpoller

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"html"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"regexp"
	"strings"
)

const IssueSubjectPrefix = "创建任务："

var whitespaceRE = regexp.MustCompile(`\s+`)

type IssueCommand struct {
	Title       string
	Description string
}

type ParsedEmail struct {
	From        string
	Subject     string
	TextBody    string
	HTMLBody    string
	MessageID   string
	Attachments []Attachment
}

type Attachment struct {
	Filename    string
	ContentType string
	Size        int64
}

func ParseIssueCommand(subject, body string) (IssueCommand, bool) {
	subject = strings.TrimSpace(subject)
	if !strings.HasPrefix(subject, IssueSubjectPrefix) {
		return IssueCommand{}, false
	}
	title := strings.TrimSpace(strings.TrimPrefix(subject, IssueSubjectPrefix))
	if title == "" {
		return IssueCommand{}, false
	}
	return IssueCommand{Title: title, Description: strings.TrimSpace(body)}, true
}

func ParseMessage(raw []byte) (ParsedEmail, error) {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return ParsedEmail{}, err
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	if err != nil {
		subject = msg.Header.Get("Subject")
	}
	parsed := ParsedEmail{
		From:      extractAddress(msg.Header.Get("From")),
		Subject:   strings.TrimSpace(subject),
		MessageID: strings.TrimSpace(msg.Header.Get("Message-Id")),
	}
	contentType := msg.Header.Get("Content-Type")
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType = "text/plain"
	}
	body, err := io.ReadAll(io.LimitReader(msg.Body, 8<<20))
	if err != nil {
		return ParsedEmail{}, err
	}
	body, err = decodeTransfer(msg.Header.Get("Content-Transfer-Encoding"), body)
	if err != nil {
		return ParsedEmail{}, err
	}
	if strings.HasPrefix(strings.ToLower(mediaType), "multipart/") {
		if err := parseMultipart(&parsed, mediaType, params["boundary"], body); err != nil {
			return ParsedEmail{}, err
		}
	} else {
		assignBodyPart(&parsed, mediaType, body)
	}
	return parsed, nil
}

func parseMultipart(parsed *ParsedEmail, mediaType, boundary string, body []byte) error {
	if boundary == "" {
		return fmt.Errorf("multipart message missing boundary")
	}
	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		partBody, err := io.ReadAll(io.LimitReader(part, 8<<20))
		_ = part.Close()
		if err != nil {
			return err
		}
		partBody, err = decodeTransfer(part.Header.Get("Content-Transfer-Encoding"), partBody)
		if err != nil {
			return err
		}
		contentType := part.Header.Get("Content-Type")
		partType, params, err := mime.ParseMediaType(contentType)
		if err != nil {
			partType = "text/plain"
		}
		if strings.HasPrefix(strings.ToLower(partType), "multipart/") {
			if err := parseMultipart(parsed, partType, params["boundary"], partBody); err != nil {
				return err
			}
			continue
		}
		if filename := part.FileName(); filename != "" {
			parsed.Attachments = append(parsed.Attachments, Attachment{
				Filename:    filename,
				ContentType: partType,
				Size:        int64(len(partBody)),
			})
			continue
		}
		assignBodyPart(parsed, partType, partBody)
	}
}

func assignBodyPart(parsed *ParsedEmail, mediaType string, body []byte) {
	switch strings.ToLower(mediaType) {
	case "text/plain":
		if strings.TrimSpace(parsed.TextBody) == "" {
			parsed.TextBody = strings.TrimSpace(string(body))
		}
	case "text/html":
		if strings.TrimSpace(parsed.HTMLBody) == "" {
			parsed.HTMLBody = strings.TrimSpace(string(body))
		}
	}
}

func decodeTransfer(encoding string, body []byte) ([]byte, error) {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "", "7bit", "8bit", "binary":
		return body, nil
	case "quoted-printable":
		return io.ReadAll(quotedprintable.NewReader(bytes.NewReader(body)))
	case "base64":
		return io.ReadAll(base64.NewDecoder(base64.StdEncoding, bytes.NewReader(body)))
	default:
		return body, nil
	}
}

func (e ParsedEmail) BodyText() string {
	if strings.TrimSpace(e.TextBody) != "" {
		return strings.TrimSpace(e.TextBody)
	}
	if strings.TrimSpace(e.HTMLBody) != "" {
		return htmlToText(e.HTMLBody)
	}
	return ""
}

func htmlToText(input string) string {
	input = strings.ReplaceAll(input, "\r\n", "\n")
	input = strings.ReplaceAll(input, "\r", "\n")
	replacer := strings.NewReplacer(
		"<br>", "\n", "<br/>", "\n", "<br />", "\n",
		"</p>", "\n", "</div>", "\n", "</li>", "\n",
		"</h1>", "\n", "</h2>", "\n", "</h3>", "\n",
	)
	text := replacer.Replace(input)
	var out bytes.Buffer
	inTag := false
	for _, r := range text {
		switch r {
		case '<':
			inTag = true
		case '>':
			inTag = false
		default:
			if !inTag {
				out.WriteRune(r)
			}
		}
	}
	decoded := html.UnescapeString(out.String())
	decoded = strings.ReplaceAll(decoded, "\u00a0", " ")
	lines := strings.Split(decoded, "\n")
	clean := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(whitespaceRE.ReplaceAllString(line, " "))
		if line != "" {
			clean = append(clean, line)
		}
	}
	return strings.Join(clean, "\n")
}

func extractAddress(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if addr, err := mail.ParseAddress(raw); err == nil {
		return strings.ToLower(strings.TrimSpace(addr.Address))
	}
	if idx := strings.LastIndex(raw, "<"); idx >= 0 && strings.HasSuffix(raw, ">") {
		raw = strings.TrimSuffix(raw[idx+1:], ">")
	}
	return strings.ToLower(strings.TrimSpace(raw))
}

func attachmentSummary(atts []Attachment) string {
	if len(atts) == 0 {
		return ""
	}
	lines := make([]string, 0, len(atts)+1)
	lines = append(lines, "附件：")
	for _, a := range atts {
		name := strings.TrimSpace(a.Filename)
		if name == "" {
			name = "unnamed"
		}
		if a.Size > 0 {
			lines = append(lines, fmt.Sprintf("- %s (%s, %d bytes)", name, a.ContentType, a.Size))
		} else {
			lines = append(lines, fmt.Sprintf("- %s (%s)", name, a.ContentType))
		}
	}
	return strings.Join(lines, "\n")
}
