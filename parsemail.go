package parsemail

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"io/ioutil"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"path"
	"strings"
	"time"

	"golang.org/x/text/encoding/htmlindex"
	"golang.org/x/text/transform"
)

const contentTypeMultipartMixed = "multipart/mixed"
const contentTypeMultipartAlternative = "multipart/alternative"
const contentTypeMultipartRelated = "multipart/related"
const contentTypeTextHtml = "text/html"
const contentTypeTextPlain = "text/plain"

var mimeWordDecoder = &mime.WordDecoder{CharsetReader: charsetReader}

// Parse reads an email message into an Email value.
func Parse(r io.Reader) (email Email, err error) {
	if r == nil {
		return email, fmt.Errorf("cannot parse email from a nil reader")
	}

	msg, err := mail.ReadMessage(r)
	if err != nil {
		return email, err
	}

	email, err = createEmailFromHeader(msg.Header)
	if err != nil {
		return email, err
	}

	email.ContentType = msg.Header.Get("Content-Type")
	contentType, params, err := parseContentType(email.ContentType)
	if err != nil {
		return email, err
	}

	if strings.HasPrefix(contentType, "multipart/") {
		parsed, parseErr := parseMultipart(msg.Body, params["boundary"], contentType, params["start"])
		if parseErr != nil {
			return email, parseErr
		}

		email.TextBody = parsed.textBody
		email.HTMLBody = parsed.htmlBody
		email.Attachments = parsed.attachments
		email.EmbeddedFiles = parsed.embeddedFiles
		return email, nil
	}

	switch contentType {
	case contentTypeTextPlain, contentTypeTextHtml:
		body, decodeErr := decodeText(msg.Body, msg.Header.Get("Content-Transfer-Encoding"), params["charset"])
		if decodeErr != nil {
			return email, decodeErr
		}

		body = trimFinalLineBreak(body)
		if contentType == contentTypeTextPlain {
			email.TextBody = body
		} else {
			email.HTMLBody = body
		}
	default:
		email.Content, err = decodeContent(msg.Body, msg.Header.Get("Content-Transfer-Encoding"))
	}

	return email, err
}

func createEmailFromHeader(header mail.Header) (email Email, err error) {
	hp := headerParser{header: &header}

	email.Subject = decodeMimeSentence(header.Get("Subject"))
	email.From = hp.parseAddressList(header.Get("From"))
	email.Sender = hp.parseAddress(header.Get("Sender"))
	email.ReplyTo = hp.parseAddressList(header.Get("Reply-To"))
	email.To = hp.parseAddressList(header.Get("To"))
	email.Cc = hp.parseAddressList(header.Get("Cc"))
	email.Bcc = hp.parseAddressList(header.Get("Bcc"))
	email.Date = hp.parseTime(header.Get("Date"))
	email.ResentFrom = hp.parseAddressList(header.Get("Resent-From"))
	email.ResentSender = hp.parseAddress(header.Get("Resent-Sender"))
	email.ResentTo = hp.parseAddressList(header.Get("Resent-To"))
	email.ResentCc = hp.parseAddressList(header.Get("Resent-Cc"))
	email.ResentBcc = hp.parseAddressList(header.Get("Resent-Bcc"))
	email.ResentMessageID = hp.parseMessageId(header.Get("Resent-Message-ID"))
	email.MessageID = hp.parseMessageId(header.Get("Message-ID"))
	email.InReplyTo = hp.parseMessageIdList(header.Get("In-Reply-To"))
	email.References = hp.parseMessageIdList(header.Get("References"))
	email.ResentDate = hp.parseTime(header.Get("Resent-Date"))

	if hp.err != nil {
		return email, hp.err
	}

	email.Header, err = decodeHeaderMime(header)
	return email, err
}

func parseContentType(contentTypeHeader string) (contentType string, params map[string]string, err error) {
	if strings.TrimSpace(contentTypeHeader) == "" {
		return contentTypeTextPlain, map[string]string{}, nil
	}

	contentType, params, err = mime.ParseMediaType(contentTypeHeader)
	if err != nil {
		return "", nil, err
	}

	return strings.ToLower(contentType), params, nil
}

type parsedContent struct {
	textBody      string
	htmlBody      string
	attachments   []Attachment
	embeddedFiles []EmbeddedFile
}

func (pc *parsedContent) merge(other parsedContent) {
	pc.textBody += other.textBody
	pc.htmlBody += other.htmlBody
	pc.attachments = append(pc.attachments, other.attachments...)
	pc.embeddedFiles = append(pc.embeddedFiles, other.embeddedFiles...)
}

func parseMultipart(msg io.Reader, boundary, contentType string, relatedStart ...string) (parsedContent, error) {
	var parsed parsedContent
	if boundary == "" {
		return parsed, fmt.Errorf("multipart content type %q has no boundary", contentType)
	}
	rootContentID := ""
	if len(relatedStart) > 0 {
		rootContentID = strings.Trim(strings.TrimSpace(relatedStart[0]), "<>")
	}

	mr := multipart.NewReader(msg, boundary)
	partIndex := 0
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			return parsed, nil
		}
		if err != nil {
			return parsed, err
		}

		partContentID := strings.Trim(strings.TrimSpace(part.Header.Get("Content-ID")), "<>")
		isRelatedRoot := false
		if contentType == contentTypeMultipartRelated {
			if rootContentID == "" {
				isRelatedRoot = partIndex == 0
			} else {
				isRelatedRoot = partContentID == rootContentID
			}
		}
		isRelatedResource := contentType == contentTypeMultipartRelated && !isRelatedRoot

		partContent, err := parsePart(part, isRelatedResource, isRelatedRoot)
		if closeErr := part.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
		if err != nil {
			return parsed, err
		}

		parsed.merge(partContent)
		partIndex++
	}
}

func parsePart(part *multipart.Part, relatedResource, relatedRoot bool) (parsedContent, error) {
	var parsed parsedContent
	contentType, params, err := parseContentType(part.Header.Get("Content-Type"))
	if err != nil {
		return parsed, err
	}

	disposition, dispositionParams := parseContentDisposition(part.Header.Get("Content-Disposition"))
	filename := dispositionParams["filename"]
	if filename == "" {
		filename = params["name"]
	}
	filename = sanitizeFilename(decodeMimeSentence(filename))
	cid := strings.Trim(strings.TrimSpace(part.Header.Get("Content-ID")), "<>")

	if disposition == "attachment" {
		attachment, err := decodeAttachmentWithMetadata(part, filename, contentType)
		if err != nil {
			return parsed, err
		}
		parsed.attachments = append(parsed.attachments, attachment)
		return parsed, nil
	}

	if relatedResource {
		embedded, err := decodeEmbeddedFileWithMetadata(part, cid, part.Header.Get("Content-Type"))
		if err != nil {
			return parsed, err
		}
		parsed.embeddedFiles = append(parsed.embeddedFiles, embedded)
		return parsed, nil
	}

	if strings.HasPrefix(contentType, "multipart/") {
		return parseMultipart(part, params["boundary"], contentType, params["start"])
	}

	if filename != "" {
		attachment, err := decodeAttachmentWithMetadata(part, filename, contentType)
		if err != nil {
			return parsed, err
		}
		parsed.attachments = append(parsed.attachments, attachment)
		return parsed, nil
	}

	if contentType == contentTypeTextPlain || contentType == contentTypeTextHtml {
		body, err := decodeText(part, part.Header.Get("Content-Transfer-Encoding"), params["charset"])
		if err != nil {
			return parsed, err
		}
		body = trimFinalLineBreak(body)
		if contentType == contentTypeTextPlain {
			parsed.textBody = body
		} else {
			parsed.htmlBody = body
		}
		return parsed, nil
	}

	if (!relatedRoot && cid != "") || disposition == "inline" {
		embedded, err := decodeEmbeddedFileWithMetadata(part, cid, part.Header.Get("Content-Type"))
		if err != nil {
			return parsed, err
		}
		parsed.embeddedFiles = append(parsed.embeddedFiles, embedded)
		return parsed, nil
	}

	// Preserve unrecognized leaf parts instead of rejecting the whole message.
	attachment, err := decodeAttachmentWithMetadata(part, filename, contentType)
	if err != nil {
		return parsed, err
	}
	parsed.attachments = append(parsed.attachments, attachment)
	return parsed, nil
}

func parseMultipartRelated(msg io.Reader, boundary string) (textBody, htmlBody string, embeddedFiles []EmbeddedFile, err error) {
	parsed, err := parseMultipart(msg, boundary, contentTypeMultipartRelated)
	return parsed.textBody, parsed.htmlBody, parsed.embeddedFiles, err
}

func parseMultipartAlternative(msg io.Reader, boundary string) (textBody, htmlBody string, embeddedFiles []EmbeddedFile, err error) {
	parsed, err := parseMultipart(msg, boundary, contentTypeMultipartAlternative)
	return parsed.textBody, parsed.htmlBody, parsed.embeddedFiles, err
}

func parseMultipartMixed(msg io.Reader, boundary string) (textBody, htmlBody string, attachments []Attachment, embeddedFiles []EmbeddedFile, err error) {
	parsed, err := parseMultipart(msg, boundary, contentTypeMultipartMixed)
	return parsed.textBody, parsed.htmlBody, parsed.attachments, parsed.embeddedFiles, err
}

func sanitizeFilename(filename string) string {
	if filename == "" {
		return ""
	}
	return path.Base(strings.Replace(filename, "\\", "/", -1))
}

func parseContentDisposition(value string) (string, map[string]string) {
	if strings.TrimSpace(value) == "" {
		return "", map[string]string{}
	}

	disposition, params, err := mime.ParseMediaType(value)
	if err != nil {
		return strings.ToLower(strings.TrimSpace(strings.Split(value, ";")[0])), map[string]string{}
	}
	return strings.ToLower(disposition), params
}

func decodeMimeSentence(s string) string {
	decoded, err := mimeWordDecoder.DecodeHeader(s)
	if err != nil {
		return s
	}
	return decoded
}

func decodeHeaderMime(header mail.Header) (mail.Header, error) {
	parsedHeader := make(mail.Header, len(header))
	for headerName, headerData := range header {
		parsedHeaderData := make([]string, 0, len(headerData))
		for _, headerValue := range headerData {
			parsedHeaderData = append(parsedHeaderData, decodeMimeSentence(headerValue))
		}
		parsedHeader[headerName] = parsedHeaderData
	}
	return parsedHeader, nil
}

func isEmbeddedFile(part *multipart.Part) bool {
	disposition, _ := parseContentDisposition(part.Header.Get("Content-Disposition"))
	return strings.TrimSpace(part.Header.Get("Content-ID")) != "" || disposition == "inline"
}

func decodeEmbeddedFile(part *multipart.Part) (EmbeddedFile, error) {
	contentID := strings.Trim(strings.TrimSpace(part.Header.Get("Content-ID")), "<>")
	return decodeEmbeddedFileWithMetadata(part, contentID, part.Header.Get("Content-Type"))
}

func decodeEmbeddedFileWithMetadata(part *multipart.Part, contentID, contentType string) (EmbeddedFile, error) {
	decoded, err := decodeContent(part, part.Header.Get("Content-Transfer-Encoding"))
	if err != nil {
		return EmbeddedFile{}, err
	}
	return EmbeddedFile{CID: contentID, Data: decoded, ContentType: contentType}, nil
}

func isAttachment(part *multipart.Part) bool {
	disposition, dispositionParams := parseContentDisposition(part.Header.Get("Content-Disposition"))
	if disposition == "attachment" || dispositionParams["filename"] != "" {
		return true
	}
	_, contentTypeParams, err := parseContentType(part.Header.Get("Content-Type"))
	return err == nil && contentTypeParams["name"] != ""
}

func decodeAttachment(part *multipart.Part) (Attachment, error) {
	contentType, params, err := parseContentType(part.Header.Get("Content-Type"))
	if err != nil {
		return Attachment{}, err
	}
	_, dispositionParams := parseContentDisposition(part.Header.Get("Content-Disposition"))
	filename := dispositionParams["filename"]
	if filename == "" {
		filename = params["name"]
	}
	return decodeAttachmentWithMetadata(part, sanitizeFilename(decodeMimeSentence(filename)), contentType)
}

func decodeAttachmentWithMetadata(part *multipart.Part, filename, contentType string) (Attachment, error) {
	decoded, err := decodeContent(part, part.Header.Get("Content-Transfer-Encoding"))
	if err != nil {
		return Attachment{}, err
	}
	return Attachment{Filename: filename, Data: decoded, ContentType: contentType}, nil
}

func decodeContent(content io.Reader, encoding string) (io.Reader, error) {
	var decoded io.Reader
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "base64":
		decoded = base64.NewDecoder(base64.StdEncoding, content)
	case "quoted-printable":
		decoded = quotedprintable.NewReader(content)
	case "", "7bit", "8bit", "binary":
		decoded = content
	default:
		return nil, fmt.Errorf("unknown content-transfer-encoding: %s", encoding)
	}

	b, err := ioutil.ReadAll(decoded)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(b), nil
}

func decodeText(content io.Reader, transferEncoding, charset string) (string, error) {
	decoded, err := decodeContent(content, transferEncoding)
	if err != nil {
		return "", err
	}

	reader, err := charsetReader(charset, decoded)
	if err != nil {
		return "", err
	}
	b, err := ioutil.ReadAll(reader)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func charsetReader(charset string, input io.Reader) (io.Reader, error) {
	charset = strings.ToLower(strings.TrimSpace(charset))
	switch charset {
	case "", "utf-8", "utf8", "us-ascii", "ascii":
		return input, nil
	}

	encoding, err := htmlindex.Get(charset)
	if err != nil {
		return nil, fmt.Errorf("unsupported charset %q", charset)
	}
	return transform.NewReader(input, encoding.NewDecoder()), nil
}

func trimFinalLineBreak(body string) string {
	if strings.HasSuffix(body, "\r\n") {
		return strings.TrimSuffix(body, "\r\n")
	}
	return strings.TrimSuffix(body, "\n")
}

type headerParser struct {
	header *mail.Header
	err    error
}

func (hp *headerParser) parseAddress(s string) *mail.Address {
	if hp.err != nil || strings.TrimSpace(s) == "" {
		return nil
	}

	parser := &mail.AddressParser{WordDecoder: mimeWordDecoder}
	address, err := parser.Parse(s)
	if err != nil {
		hp.err = err
		return nil
	}
	return address
}

func (hp *headerParser) parseAddressList(s string) []*mail.Address {
	if hp.err != nil || strings.TrimSpace(s) == "" {
		return nil
	}

	parser := &mail.AddressParser{WordDecoder: mimeWordDecoder}
	addresses, err := parser.ParseList(s)
	if err != nil {
		hp.err = err
		return nil
	}
	return addresses
}

func (hp *headerParser) parseTime(s string) time.Time {
	if hp.err != nil || strings.TrimSpace(s) == "" {
		return time.Time{}
	}

	parsed, err := mail.ParseDate(s)
	if err == nil {
		return parsed
	}

	formats := []string{
		time.RFC1123Z,
		"Mon, 2 Jan 2006 15:04:05 -0700",
		time.RFC1123Z + " (MST)",
		"Mon, 2 Jan 2006 15:04:05 -0700 (MST)",
	}
	for _, format := range formats {
		parsed, err = time.Parse(format, s)
		if err == nil {
			return parsed
		}
	}

	hp.err = err
	return time.Time{}
}

func (hp *headerParser) parseMessageId(s string) string {
	ids := parseMessageIDs(s)
	if len(ids) > 0 {
		return ids[0]
	}
	return strings.Trim(s, "<> \t\r\n")
}

func (hp *headerParser) parseMessageIdList(s string) []string {
	return parseMessageIDs(s)
}

func parseMessageIDs(s string) []string {
	var result []string
	remainder := s
	for {
		start := strings.Index(remainder, "<")
		if start < 0 {
			break
		}
		end := strings.Index(remainder[start+1:], ">")
		if end < 0 {
			break
		}
		id := strings.TrimSpace(remainder[start+1 : start+1+end])
		if id != "" {
			result = append(result, id)
		}
		remainder = remainder[start+end+2:]
	}

	if len(result) > 0 {
		return result
	}
	for _, field := range strings.Fields(s) {
		id := strings.Trim(field, "<>, \t\r\n")
		if id != "" {
			result = append(result, id)
		}
	}
	return result
}

// Attachment contains an attachment's filename, media type and decoded data.
type Attachment struct {
	Filename    string
	ContentType string
	Data        io.Reader
}

// EmbeddedFile contains an inline file's content ID, media type and decoded data.
type EmbeddedFile struct {
	CID         string
	ContentType string
	Data        io.Reader
}

// Email contains the parsed RFC 5322 headers, bodies and MIME files.
type Email struct {
	Header mail.Header

	Subject    string
	Sender     *mail.Address
	From       []*mail.Address
	ReplyTo    []*mail.Address
	To         []*mail.Address
	Cc         []*mail.Address
	Bcc        []*mail.Address
	Date       time.Time
	MessageID  string
	InReplyTo  []string
	References []string

	ResentFrom      []*mail.Address
	ResentSender    *mail.Address
	ResentTo        []*mail.Address
	ResentDate      time.Time
	ResentCc        []*mail.Address
	ResentBcc       []*mail.Address
	ResentMessageID string

	ContentType string
	Content     io.Reader

	HTMLBody string
	TextBody string

	Attachments   []Attachment
	EmbeddedFiles []EmbeddedFile
}
