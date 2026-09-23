package lark

import (
	"context"
	"strconv"
	"strings"

	"go.gh.ink/notifyutils/errors"
	"go.gh.ink/notifyutils/model"
	"go.gh.ink/notifyutils/utils/httpx"
	"go.gh.ink/notifyutils/utils/sign"
)

// Payload is the request body. timestamp and sign only appear when the robot has
// 签名校验 switched on, and a card message puts the card structure in the top-level
// card field instead of content. Note that the webhook takes both content and card
// as JSON objects: the stringified-JSON content is a quirk of the im/v1 message API,
// and copying it here is rejected as 9499 Bad Request.
type Payload struct {
	Timestamp string `json:"timestamp,omitempty"`
	Sign      string `json:"sign,omitempty"`
	MsgType   string `json:"msg_type"`
	Content   any    `json:"content,omitempty"`
	Card      any    `json:"card,omitempty"`
}

// TextContent is content for msg_type text.
type TextContent struct {
	Text string `json:"text"`
}

// Card is the structure this driver builds for a markdown message; ExtraCard bypasses
// it. Only the fields the driver has a source for are modelled.
type Card struct {
	Schema string      `json:"schema"`
	Header *CardHeader `json:"header,omitempty"`
	Body   CardBody    `json:"body"`
}

// CardHeader is where the message title goes when the message becomes a card, which
// is the one place a card has for a headline.
type CardHeader struct {
	Title CardText `json:"title"`
}

type CardText struct {
	Tag     string `json:"tag"`
	Content string `json:"content"`
}

type CardBody struct {
	Elements []CardElement `json:"elements"`
}

type CardElement struct {
	Tag     string `json:"tag"`
	Content string `json:"content"`
}

// reply is the response envelope. StatusCode/StatusMessage are documented as
// redundant legacy fields, so only code decides success.
type reply struct {
	Code *int   `json:"code"`
	Msg  string `json:"msg"`
}

func (c Client) Send(ctx context.Context, msg model.Message) error {
	payload, err := c.build(msg)
	if err != nil {
		return err
	}

	c.sign(payload)

	body, err := c.Marshal(*payload)
	if err != nil {
		return err
	}

	// The ceiling is stated for the whole request body, so it can only be measured
	// once the payload is encoded. Refusing beats letting the upstream cut the
	// message: half a notification reads as an all-clear.
	if len(body) > MaxRequestBodyBytes {
		return errors.ErrMessageTooLong.
			WithDriverName(Name).
			WithDriverMessage("request body is " + strconv.Itoa(len(body)) +
				" bytes, the documented ceiling is " + strconv.Itoa(MaxRequestBodyBytes))
	}

	resp, err := httpx.Do(ctx, c.Client, httpx.Request{
		URL:    c.Webhook,
		Header: map[string]string{"Content-Type": ContentType},
		Body:   body,
	})
	if err != nil {
		return err
	}

	failure := errors.ErrDriverSendFailed.
		WithDriverName(Name).
		WithDriverRequestID(resp.Header.Get(HeaderLogID)).
		WithDriverResponse(string(resp.Body))

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return failure.
			WithDriverCode(strconv.Itoa(resp.StatusCode)).
			WithDriverMessage(string(resp.Body))
	}

	// A rejection also arrives as HTTP 200, with the verdict in the body.
	var parsed reply
	if err := c.Unmarshal(resp.Body, &parsed); err != nil || parsed.Code == nil {
		return failure.WithDriverMessage("response is not the documented {code,msg} JSON")
	}
	if *parsed.Code != 0 {
		return failure.
			WithDriverCode(strconv.Itoa(*parsed.Code)).
			WithDriverMessage(parsed.Msg)
	}

	return nil
}

// sign stamps the payload when the robot has 签名校验 enabled. The documented recipe
// is the reverse of what it looks like: the HMAC-SHA256 key is the string
// "<timestamp>\n<secret>" and the signed message is empty. timestamp counts in
// seconds and has to stay within one hour of the upstream clock, and both fields
// travel in the body, not in the URL query.
func (c Client) sign(payload *Payload) {
	if c.Secret == "" {
		return
	}

	timestamp := strconv.FormatInt(sign.UnixSeconds(), 10)

	payload.Timestamp = timestamp
	payload.Sign = sign.HMACSHA256Base64([]byte(timestamp+"\n"+c.Secret), nil)
}

func (c Client) build(msg model.Message) (*Payload, error) {
	body := msg.Text
	mentions := mentions(msg)

	// A caller-supplied card is the payload, so it wins over anything derived from
	// Format. It is sent untouched, which means the parts of the message a card can
	// only express inside its own structure have to be refused rather than dropped:
	// the title and the @ list belong in the card the caller wrote.
	if raw := msg.Extra(ExtraCard); raw != nil {
		if len(mentions) > 0 || msg.Title != "" {
			return nil, errors.ErrDriverSendFailed.
				WithDriverName(Name).
				WithDriverMessage(ExtraCard + " is sent verbatim: put the title and any @ tags inside that card")
		}

		card, err := c.card(raw)
		if err != nil {
			return nil, err
		}

		return &Payload{MsgType: MsgTypeInteractive, Card: card}, nil
	}

	switch msg.Format {
	case model.FormatPlain:
		// A text message has no headline slot, so the title folds into the first
		// line, and the mentions go inline: text has no mention field.
		content := foldTitle(msg.Title, body)
		content = appendAfter(content, textMentions(mentions))

		return &Payload{MsgType: MsgTypeText, Content: TextContent{Text: content}}, nil

	case model.FormatMarkdown:
		// A card rather than a post: post's own tags (text, a, at, img) render their
		// content literally, so markdown would arrive as source code, while the card
		// markdown element is documented as carrying markdown syntax.
		content := appendAfter(body, cardMentions(mentions))

		card := &Card{
			Schema: CardSchema,
			Body:   CardBody{Elements: []CardElement{{Tag: CardTagMarkdown, Content: content}}},
		}
		if msg.Title != "" {
			card.Header = &CardHeader{Title: CardText{Tag: CardTagPlainName, Content: msg.Title}}
		}

		return &Payload{MsgType: MsgTypeInteractive, Card: card}, nil
	}

	return nil, errors.ErrUnsupportedFormat.
		WithDriverName(Name).
		WithDriverMessage("this channel renders plain as text and markdown as a card, not " + string(msg.Format))
}

// card accepts a card written either as Go values or as JSON, since a card structure
// that came out of the 搭建工具 or a template is usually on hand as text.
func (c Client) card(raw any) (any, error) {
	var encoded string

	switch value := raw.(type) {
	case string:
		encoded = value
	case []byte:
		encoded = string(value)
	default:
		return raw, nil
	}

	var parsed any
	if err := c.Unmarshal([]byte(encoded), &parsed); err != nil {
		return nil, errors.ErrDriverSendFailed.
			WithDriverName(Name).
			WithDriverMessage(ExtraCard + " is not valid JSON: " + err.Error()).
			WithDriverResponse(encoded)
	}
	if _, ok := parsed.(map[string]any); !ok {
		return nil, errors.ErrDriverSendFailed.
			WithDriverName(Name).
			WithDriverMessage(ExtraCard + " must decode to a card object").
			WithDriverResponse(encoded)
	}

	return parsed, nil
}

// mentions resolves whom the message @s. Recipients and ExtraAtUserIDs both hold
// open_id or user_id values: a custom robot holds no data permissions at all, which
// is precisely why the documentation rules out addressing anybody by email or mobile
// number.
func mentions(msg model.Message) []string {
	var ids []string

	ids = append(ids, msg.Recipients...)
	ids = append(ids, msg.ExtraStrings(ExtraAtUserIDs)...)
	if all, _ := msg.ExtraBool(ExtraAtAll); all {
		ids = append(ids, MentionAll)
	}

	return unique(ids)
}

// textMentions renders the @ tags a text message accepts. The id doubles as the
// display name on purpose: when the upstream cannot resolve an id it shows the name
// instead, so a bad mention stays visible rather than silently disappearing.
func textMentions(ids []string) string {
	tags := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == MentionAll {
			// 所有人 is the wording the documentation's own example uses.
			tags = append(tags, `<at user_id="all">所有人</at>`)

			continue
		}

		tags = append(tags, `<at user_id="`+id+`">`+id+`</at>`)
	}

	return strings.Join(tags, "")
}

// cardMentions is the card flavour of the same list, which the card markdown
// component documents with nothing between the tags.
func cardMentions(ids []string) string {
	tags := make([]string, 0, len(ids))
	for _, id := range ids {
		tags = append(tags, "<at id="+id+"></at>")
	}

	return strings.Join(tags, " ")
}

// foldTitle puts the headline on the first line, which is all a channel without a
// title field can do with it.
func foldTitle(title, body string) string {
	if title == "" {
		return body
	}

	return appendAfter(title, body)
}

func appendAfter(head, tail string) string {
	switch {
	case head == "":
		return tail
	case tail == "":
		return head
	default:
		return head + "\n" + tail
	}
}

// unique drops blanks and repeats, keeping the order the caller wrote. It always
// allocates, because the driver may not write into the slices the message carries.
func unique(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	if len(out) == 0 {
		return nil
	}

	return out
}
