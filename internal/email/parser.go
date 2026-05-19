package email

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"sort"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html/charset"
)

var allowedCustomHeaders = map[string]struct{}{
	"Archived-At":                   {},
	"Auto-Submitted":                {},
	"Comments":                      {},
	"Content-Language":              {},
	"Importance":                    {},
	"In-Reply-To":                   {},
	"Keywords":                      {},
	"List-Archive":                  {},
	"List-Help":                     {},
	"List-Id":                       {},
	"List-Owner":                    {},
	"List-Post":                     {},
	"List-Subscribe":                {},
	"List-Unsubscribe":              {},
	"List-Unsubscribe-Post":         {},
	"Organization":                  {},
	"Precedence":                    {},
	"References":                    {},
	"Require-Recipient-Valid-Since": {},
	"Sensitivity":                   {},
}

type ParseOptions struct {
	EnvelopeFrom string
	Recipients   []string
	FromOverride string
}

type ParsedMessage struct {
	Message     Message
	TraceHeader map[string]string
}

func Parse(raw []byte, opts ParseOptions) (Message, error) {
	parsed, err := ParseWithTrace(raw, opts)
	if err != nil {
		return Message{}, err
	}
	return parsed.Message, nil
}

func ParseWithTrace(raw []byte, opts ParseOptions) (ParsedMessage, error) {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return ParsedMessage{}, fmt.Errorf("read message: %w", err)
	}

	header := textproto.MIMEHeader(msg.Header)
	from, err := selectSender(header, opts.EnvelopeFrom, opts.FromOverride)
	if err != nil {
		return ParsedMessage{}, err
	}

	to := parseAddressHeader(header, "To")
	cc := parseAddressHeader(header, "Cc")
	to, cc, bcc := reconcileRecipients(to, cc, opts.Recipients)
	if len(to) == 0 {
		return ParsedMessage{}, fmt.Errorf("no recipients found")
	}

	body := parsedBody{}
	if err := parseEntity(header, msg.Body, &body); err != nil {
		return ParsedMessage{}, err
	}
	if body.Text == "" && body.HTML == "" && len(body.Attachments) == 0 {
		return ParsedMessage{}, fmt.Errorf("message must include a text body, html body, or attachment")
	}

	return ParsedMessage{
		Message: Message{
			From:        from,
			ReplyTo:     firstAddress(header, "Reply-To"),
			To:          to,
			CC:          cc,
			BCC:         bcc,
			Subject:     decodeHeader(header.Get("Subject")),
			Text:        body.Text,
			HTML:        body.HTML,
			Headers:     customHeaders(header),
			Attachments: body.Attachments,
			RawSize:     len(raw),
		},
		TraceHeader: traceHeaders(header),
	}, nil
}

type headerGetter interface {
	Get(string) string
}

func traceHeaders(header headerGetter) map[string]string {
	headers := map[string]string{}
	if traceparent := header.Get("Traceparent"); traceparent != "" {
		headers["traceparent"] = strings.TrimSpace(traceparent)
	}
	if tracestate := header.Get("Tracestate"); tracestate != "" {
		headers["tracestate"] = strings.TrimSpace(tracestate)
	}
	if len(headers) == 0 {
		return nil
	}
	return headers
}

type parsedBody struct {
	Text        string
	HTML        string
	Attachments []Attachment
}

func parseEntity(header textproto.MIMEHeader, r io.Reader, out *parsedBody) error {
	contentType := header.Get("Content-Type")
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType == "" {
		mediaType = "text/plain"
		params = map[string]string{}
	}
	mediaType = strings.ToLower(mediaType)

	if strings.HasPrefix(mediaType, "multipart/") {
		if mediaType == "multipart/signed" || mediaType == "multipart/encrypted" {
			return nil
		}
		boundary := params["boundary"]
		if boundary == "" {
			return fmt.Errorf("multipart message missing boundary")
		}
		reader := multipart.NewReader(r, boundary)
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				return fmt.Errorf("read MIME part: %w", err)
			}
			if err := parseEntity(part.Header, part, out); err != nil {
				_ = part.Close()
				return err
			}
			_ = part.Close()
		}
		return nil
	}

	data, err := io.ReadAll(transferReader(header.Get("Content-Transfer-Encoding"), r))
	if err != nil {
		return fmt.Errorf("read MIME body: %w", err)
	}

	disposition, dispositionParams, _ := mime.ParseMediaType(header.Get("Content-Disposition"))
	filename := dispositionParams["filename"]
	if filename == "" {
		_, contentTypeParams, _ := mime.ParseMediaType(contentType)
		filename = contentTypeParams["name"]
	}

	if isAttachment(disposition, filename, header.Get("Content-ID")) {
		contentID := cleanContentID(header.Get("Content-ID"))
		filename = decodeHeader(filename)
		if filename == "" {
			filename = fallbackFilename(contentID, len(out.Attachments)+1)
		}
		out.Attachments = append(out.Attachments, Attachment{
			Content:     base64.StdEncoding.EncodeToString(data),
			Filename:    filename,
			Type:        attachmentType(mediaType),
			Disposition: attachmentDisposition(disposition),
			ContentID:   contentID,
		})
		return nil
	}

	switch mediaType {
	case "text/plain":
		if out.Text == "" {
			out.Text = decodeText(data, params["charset"])
		}
	case "text/html":
		if out.HTML == "" {
			out.HTML = decodeText(data, params["charset"])
		}
	}

	return nil
}

func transferReader(encoding string, r io.Reader) io.Reader {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "base64":
		return base64.NewDecoder(base64.StdEncoding, r)
	case "quoted-printable":
		return quotedprintable.NewReader(r)
	default:
		return r
	}
}

func decodeText(data []byte, label string) string {
	if label == "" || strings.EqualFold(label, "utf-8") || strings.EqualFold(label, "us-ascii") {
		return string(data)
	}
	reader, err := charset.NewReaderLabel(label, bytes.NewReader(data))
	if err != nil {
		return string(data)
	}
	decoded, err := io.ReadAll(reader)
	if err != nil {
		return string(data)
	}
	return string(decoded)
}

func isAttachment(disposition, filename, contentID string) bool {
	disposition = strings.ToLower(disposition)
	return disposition == "attachment" || disposition == "inline" || filename != "" || contentID != ""
}

func attachmentType(mediaType string) string {
	if mediaType == "" {
		return "application/octet-stream"
	}
	return mediaType
}

func attachmentDisposition(disposition string) string {
	if strings.EqualFold(disposition, "inline") {
		return "inline"
	}
	return "attachment"
}

func cleanContentID(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "<")
	value = strings.TrimSuffix(value, ">")
	return value
}

func fallbackFilename(contentID string, index int) string {
	if contentID != "" {
		return contentID
	}
	return fmt.Sprintf("attachment-%d", index)
}

func selectSender(header textproto.MIMEHeader, envelopeFrom, override string) (Address, error) {
	for _, candidate := range []string{override, header.Get("From"), envelopeFrom} {
		addr := parseAddress(candidate)
		if addr.Address != "" {
			return addr, nil
		}
	}
	return Address{}, fmt.Errorf("no sender found")
}

func firstAddress(header textproto.MIMEHeader, name string) *Address {
	raw := strings.TrimSpace(header.Get(name))
	if raw == "" {
		return nil
	}
	addresses, err := mail.ParseAddressList(raw)
	if err != nil || len(addresses) == 0 {
		return nil
	}
	address := Address{
		Address: normalizeAddress(addresses[0].Address),
		Name:    decodeHeader(addresses[0].Name),
	}
	if address.Address == "" {
		return nil
	}
	return &address
}

func parseAddressHeader(header textproto.MIMEHeader, name string) []string {
	raw := strings.TrimSpace(header.Get(name))
	if raw == "" {
		return nil
	}
	addresses, err := mail.ParseAddressList(raw)
	if err != nil {
		return nil
	}
	result := make([]string, 0, len(addresses))
	seen := map[string]struct{}{}
	for _, address := range addresses {
		value := normalizeAddress(address.Address)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result
}

func parseAddress(value string) Address {
	value = strings.TrimSpace(value)
	if value == "" {
		return Address{}
	}
	addr, err := mail.ParseAddress(value)
	if err == nil {
		return Address{
			Address: normalizeAddress(addr.Address),
			Name:    decodeHeader(addr.Name),
		}
	}
	return Address{Address: normalizeAddress(value)}
}

func normalizeAddress(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "<")
	value = strings.TrimSuffix(value, ">")
	return value
}

func reconcileRecipients(to, cc, envelope []string) ([]string, []string, []string) {
	to = dedupeAddresses(to)
	cc = removeKnown(dedupeAddresses(cc), to)

	envelope = dedupeAddresses(envelope)
	if len(envelope) == 0 {
		if len(to) > 0 {
			return to, cc, nil
		}
		if len(cc) > 0 {
			return []string{cc[0]}, cc[1:], nil
		}
		return nil, nil, nil
	}

	envelopeSet := make(map[string]struct{}, len(envelope))
	for _, recipient := range envelope {
		envelopeSet[strings.ToLower(recipient)] = struct{}{}
	}

	to = keepKnown(to, envelopeSet)
	cc = removeKnown(keepKnown(cc, envelopeSet), to)

	visible := make(map[string]struct{}, len(to)+len(cc))
	for _, recipient := range to {
		visible[strings.ToLower(recipient)] = struct{}{}
	}
	for _, recipient := range cc {
		visible[strings.ToLower(recipient)] = struct{}{}
	}

	bcc := make([]string, 0, len(envelope))
	for _, recipient := range envelope {
		if _, ok := visible[strings.ToLower(recipient)]; ok {
			continue
		}
		bcc = append(bcc, recipient)
	}

	if len(to) == 0 {
		if len(cc) > 0 {
			to = []string{cc[0]}
			cc = cc[1:]
		} else if len(bcc) > 0 {
			to = []string{bcc[0]}
			bcc = bcc[1:]
		}
	}

	return to, cc, bcc
}

func dedupeAddresses(values []string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		addr := normalizeAddress(value)
		if addr == "" {
			continue
		}
		key := strings.ToLower(addr)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, addr)
	}
	return result
}

func removeKnown(values, known []string) []string {
	seen := map[string]struct{}{}
	for _, value := range known {
		seen[strings.ToLower(value)] = struct{}{}
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[strings.ToLower(value)]; ok {
			continue
		}
		result = append(result, value)
	}
	return result
}

func keepKnown(values []string, known map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := known[strings.ToLower(value)]; ok {
			result = append(result, value)
		}
	}
	return result
}

func customHeaders(header textproto.MIMEHeader) map[string]string {
	result := map[string]string{}
	whitelistedCount := 0
	totalBytes := 0
	names := make([]string, 0, len(header))
	for rawName := range header {
		names = append(names, rawName)
	}
	sort.Strings(names)

	for _, rawName := range names {
		values := header[rawName]
		name := textproto.CanonicalMIMEHeaderKey(rawName)
		if !isAllowedCustomHeader(name) || !validHeaderName(name) {
			continue
		}
		value := sanitizeHeaderValue(strings.Join(values, ", "))
		if value == "" || len(value) > 2048 || !utf8.ValidString(value) {
			continue
		}
		if !strings.HasPrefix(name, "X-") {
			if whitelistedCount >= 20 {
				continue
			}
		}
		entryBytes := len(name) + 2 + len(value) + 2
		if totalBytes+entryBytes > 16*1024 {
			continue
		}
		result[name] = value
		if !strings.HasPrefix(name, "X-") {
			whitelistedCount++
		}
		totalBytes += entryBytes
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func isAllowedCustomHeader(name string) bool {
	if strings.HasPrefix(name, "X-") {
		return true
	}
	_, ok := allowedCustomHeaders[name]
	return ok
}

func validHeaderName(name string) bool {
	if name == "" || len(name) > 100 {
		return false
	}
	if strings.HasPrefix(name, "X-") {
		if len(name) == 2 {
			return false
		}
		for _, r := range name[2:] {
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
				continue
			}
			return false
		}
		return true
	}
	for _, r := range name {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return false
	}
	return true
}

func sanitizeHeaderValue(value string) string {
	value = strings.ReplaceAll(value, "\r\n", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "\r", " ")
	return strings.TrimSpace(value)
}

func decodeHeader(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	decoded, err := (&mime.WordDecoder{}).DecodeHeader(value)
	if err != nil {
		return value
	}
	return decoded
}
