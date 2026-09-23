package lark

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	stderrors "errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"go.gh.ink/notifyutils/errors"
	"go.gh.ink/notifyutils/model"
)

// logID is echoed on every response so the tests can check that the driver lifts the
// upstream trace id into the error.
const logID = "0c11d22e333f4444555566667777aaaa"

type captured struct {
	mu       sync.Mutex
	requests int
	path     string
	query    string
	body     string
	header   http.Header
}

func (c *captured) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.requests
}

// server records the last request and replies with the given status and body.
func server(status int, body string) (*httptest.Server, *captured) {
	got := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)

		got.mu.Lock()
		got.requests++
		got.path = r.URL.Path
		got.query = r.URL.RawQuery
		got.body = string(data)
		got.header = r.Header.Clone()
		got.mu.Unlock()

		w.Header().Set(HeaderLogID, logID)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))

	return srv, got
}

func paramsFor(srv *httptest.Server, credential map[string]string) model.DriverClientParam {
	if credential == nil {
		credential = map[string]string{}
	}
	credential[Webhook] = srv.URL + "/open-apis/bot/v2/hook/test-token"

	return model.DriverClientParam{
		Credential: credential,
		HTTPClient: srv.Client(),
		Marshal:    json.Marshal,
		Unmarshal:  json.Unmarshal,
	}
}

func clientFor(t *testing.T, srv *httptest.Server, credential map[string]string) model.Client {
	t.Helper()

	client, err := Driver{}.NewClient(paramsFor(srv, credential))
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	return client
}

// decode parses the captured body, because the assertions are about field names and
// nesting rather than about substrings that could match by accident.
func decode(t *testing.T, body string) map[string]any {
	t.Helper()

	var parsed map[string]any
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("request body is not JSON: %v\n%s", err, body)
	}

	return parsed
}

func object(t *testing.T, payload map[string]any, keys ...string) map[string]any {
	t.Helper()

	value := field(t, payload, keys...)

	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s is %T, want a JSON object (%#v)", strings.Join(keys, "."), value, value)
	}

	return object
}

func field(t *testing.T, payload map[string]any, keys ...string) any {
	t.Helper()

	current := payload
	for i, key := range keys {
		value, ok := current[key]
		if !ok {
			t.Fatalf("payload has no %s (path %s)", key, strings.Join(keys[:i+1], "."))
		}
		if i == len(keys)-1 {
			return value
		}
		object, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("payload path %s is %T, want an object", strings.Join(keys[:i+1], "."), value)
		}
		current = object
	}

	return nil
}

func stringField(t *testing.T, payload map[string]any, keys ...string) string {
	t.Helper()

	value, ok := field(t, payload, keys...).(string)
	if !ok {
		t.Fatalf("%s is not a string: %#v", strings.Join(keys, "."), field(t, payload, keys...))
	}

	return value
}

// elements reads the card body elements, which is where a driver-built card keeps
// its text.
func elements(t *testing.T, payload map[string]any) []map[string]any {
	t.Helper()

	raw, ok := object(t, payload, "card", "body")["elements"].([]any)
	if !ok {
		t.Fatalf("card.body.elements is not a list: %#v", object(t, payload, "card", "body"))
	}

	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		object, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("card.body.elements holds %T, want objects", item)
		}
		out = append(out, object)
	}

	return out
}

func TestName(t *testing.T) {
	if Name != "lark" {
		t.Errorf("Name = %q, want lark", Name)
	}
}

func TestNewClientRequiresWebhook(t *testing.T) {
	_, err := Driver{}.NewClient(model.DriverClientParam{
		Credential: map[string]string{},
		Marshal:    json.Marshal,
		Unmarshal:  json.Unmarshal,
	})

	if !stderrors.Is(err, errors.ErrDriverCredentialInvalid) {
		t.Fatalf("error = %v, want ErrDriverCredentialInvalid", err)
	}
	var typed *errors.NotifyutilsError
	if !stderrors.As(err, &typed) || typed.DriverName() != Name {
		t.Errorf("error should name the driver, got %v", err)
	}
}

func TestNewClientRejectsWebhookWithoutTarget(t *testing.T) {
	for _, webhook := range []string{"not a url", "open.feishu.cn/bot/v2/hook/x", "https://open.feishu.cn", "https://open.feishu.cn/"} {
		_, err := Driver{}.NewClient(model.DriverClientParam{
			Credential: map[string]string{Webhook: webhook},
			Marshal:    json.Marshal,
			Unmarshal:  json.Unmarshal,
		})
		if !stderrors.Is(err, errors.ErrDriverCredentialInvalid) {
			t.Errorf("NewClient(%q) error = %v, want ErrDriverCredentialInvalid", webhook, err)
		}
	}
}

func TestSendPlainUsesTextMsgType(t *testing.T) {
	srv, got := server(200, `{"code":0,"msg":"success","data":{}}`)
	defer srv.Close()
	client := clientFor(t, srv, nil)

	err := client.Send(context.Background(), model.Message{
		Title:  "Deploy",
		Text:   "release 1.4.2 is live on web-01",
		Format: model.FormatPlain,
		Level:  model.LevelInfo,
	})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	if got.path != "/open-apis/bot/v2/hook/test-token" || got.query != "" {
		t.Errorf("request went to %s?%s, want the hook URL untouched", got.path, got.query)
	}
	if ct := got.header.Get("Content-Type"); ct != ContentType {
		t.Errorf("Content-Type = %q, want %q", ct, ContentType)
	}

	payload := decode(t, got.body)
	if payload["msg_type"] != MsgTypeText {
		t.Errorf("msg_type = %v, want text", payload["msg_type"])
	}
	// content is a JSON object here. The webhook takes an object; the stringified
	// JSON content belongs to the im/v1 message API.
	if _, ok := payload["content"].(map[string]any); !ok {
		t.Fatalf("content is %T, want an object: %s", payload["content"], got.body)
	}
	// Vars are rendered by the driver, and the headline folds onto the first line
	// because a text message has no title slot.
	const want = "Deploy\nrelease 1.4.2 is live on web-01"
	if content := stringField(t, payload, "content", "text"); content != want {
		t.Errorf("content.text = %q, want %q", content, want)
	}
	if _, ok := payload["card"]; ok {
		t.Errorf("a plain message must not carry a card: %s", got.body)
	}
}

func TestSendMarkdownBuildsCard(t *testing.T) {
	srv, got := server(200, `{"code":0,"msg":"success","data":{}}`)
	defer srv.Close()
	client := clientFor(t, srv, nil)

	err := client.Send(context.Background(), model.Message{
		Title:  "Disk",
		Text:   "**92%** used on `db-02`",
		Format: model.FormatMarkdown,
		Level:  model.LevelWarn,
	})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	payload := decode(t, got.body)
	if payload["msg_type"] != MsgTypeInteractive {
		t.Fatalf("msg_type = %v, want interactive", payload["msg_type"])
	}
	if _, ok := payload["content"]; ok {
		t.Errorf("a card message carries card, not content: %s", got.body)
	}

	card := object(t, payload, "card")
	if card["schema"] != CardSchema {
		t.Errorf("card.schema = %v, want %q", card["schema"], CardSchema)
	}
	// The title becomes the card header, which is the card's only headline slot.
	if tag := stringField(t, payload, "card", "header", "title", "tag"); tag != CardTagPlainName {
		t.Errorf("card.header.title.tag = %q, want %q", tag, CardTagPlainName)
	}
	if title := stringField(t, payload, "card", "header", "title", "content"); title != "Disk" {
		t.Errorf("card.header.title.content = %q, want the message title", title)
	}

	list := elements(t, payload)
	if len(list) != 1 {
		t.Fatalf("card.body.elements = %d items, want one", len(list))
	}
	if list[0]["tag"] != CardTagMarkdown {
		t.Errorf("element tag = %v, want markdown so the syntax renders", list[0]["tag"])
	}
	if content, _ := list[0]["content"].(string); content != "**92%** used on `db-02`" {
		t.Errorf("element content = %v, want the rendered body", list[0]["content"])
	}
}

// Without a title the card keeps no header at all, which is what leaves the header
// bar off the rendered card.
func TestSendMarkdownWithoutTitleOmitsHeader(t *testing.T) {
	srv, got := server(200, `{"code":0,"msg":"success"}`)
	defer srv.Close()
	client := clientFor(t, srv, nil)

	if err := client.Send(context.Background(), model.Message{Text: "plain body", Format: model.FormatMarkdown}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	if _, ok := object(t, decode(t, got.body), "card")["header"]; ok {
		t.Errorf("card.header should be absent without a title: %s", got.body)
	}
}

func TestSendMarkdownWithoutContentKeepsTitle(t *testing.T) {
	srv, got := server(200, `{"code":0,"msg":"success"}`)
	defer srv.Close()
	client := clientFor(t, srv, nil)

	if err := client.Send(context.Background(), model.Message{Title: "Empty", Format: model.FormatMarkdown}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	list := elements(t, decode(t, got.body))
	if content, _ := list[0]["content"].(string); content != "" {
		t.Errorf("element content = %q, want the empty body kept as is", content)
	}
}

// The recipe is the documented one: the key is "<timestamp>\n<secret>" and the signed
// message is empty. Recomputed here with a separate crypto call path so the driver's
// use of the shared helper is checked against something independent of it.
func TestSignTravelsInTheBody(t *testing.T) {
	const secret = "s3cr3t"

	srv, got := server(200, `{"code":0,"msg":"success"}`)
	defer srv.Close()
	client := clientFor(t, srv, map[string]string{Secret: secret})

	before := time.Now().Add(-2 * time.Second).Unix()
	if err := client.Send(context.Background(), model.Message{Text: "signed", Format: model.FormatPlain}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	payload := decode(t, got.body)

	timestamp := stringField(t, payload, "timestamp")
	stamp, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		t.Fatalf("timestamp = %q, want the documented seconds-based value", timestamp)
	}
	if stamp < before || stamp > time.Now().Add(time.Minute).Unix() {
		t.Errorf("timestamp %d is outside the plausible window [%d, now]", stamp, before)
	}

	mac := hmac.New(sha256.New, []byte(timestamp+"\n"+secret))
	mac.Write(nil)
	want := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	if sign, _ := payload["sign"].(string); sign != want {
		t.Errorf("sign = %q, want %q recomputed from timestamp+key", sign, want)
	}
	// Neither field belongs in the URL.
	if got.query != "" {
		t.Errorf("query = %q, want the signature kept out of the URL", got.query)
	}
}

func TestSendWithoutSecretSendsNoSignatureFields(t *testing.T) {
	for _, credential := range []map[string]string{{}, {Secret: ""}} {
		srv, got := server(200, `{"code":0,"msg":"success"}`)
		defer srv.Close()
		client := clientFor(t, srv, credential)

		if err := client.Send(context.Background(), model.Message{Text: "x", Format: model.FormatPlain}); err != nil {
			t.Fatalf("Send() error = %v", err)
		}

		payload := decode(t, got.body)
		for _, key := range []string{"sign", "timestamp"} {
			if _, ok := payload[key]; ok {
				t.Errorf("%s should be absent without a secret: %s", key, got.body)
			}
		}
	}
}

func TestSendTextMentions(t *testing.T) {
	srv, got := server(200, `{"code":0,"msg":"success"}`)
	defer srv.Close()
	client := clientFor(t, srv, nil)

	err := client.Send(context.Background(), model.Message{
		Text:       "come quick",
		Format:     model.FormatPlain,
		Level:      model.LevelCritical,
		Recipients: []string{"ou_one", "ou_one", ""},
		Extras:     map[string]any{ExtraAtUserIDs: []string{"ou_two"}, ExtraAtAll: true},
	})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	content := stringField(t, decode(t, got.body), "content", "text")
	const want = "come quick\n" +
		`<at user_id="ou_one">ou_one</at>` + `<at user_id="ou_two">ou_two</at>` + `<at user_id="all">所有人</at>`
	if content != want {
		t.Errorf("content.text = %q, want %q", content, want)
	}
}

func TestSendCardMentions(t *testing.T) {
	srv, got := server(200, `{"code":0,"msg":"success"}`)
	defer srv.Close()
	client := clientFor(t, srv, nil)

	err := client.Send(context.Background(), model.Message{
		Text:       "build failed",
		Format:     model.FormatMarkdown,
		Recipients: []string{"ou_one"},
		Extras:     map[string]any{ExtraAtAll: true},
	})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	list := elements(t, decode(t, got.body))
	content, _ := list[0]["content"].(string)
	for _, want := range []string{"build failed", "<at id=ou_one></at>", "<at id=all></at>"} {
		if !strings.Contains(content, want) {
			t.Errorf("card element content = %q, want it to contain %q", content, want)
		}
	}
}

// A card from the 搭建工具 arrives as JSON text, and a hand-built one as Go values;
// both have to reach the body unchanged.
func TestSendRawCardPassthrough(t *testing.T) {
	const raw = `{"schema":"2.0","header":{"title":{"tag":"plain_text","content":"Built"}},"body":{"elements":[{"tag":"markdown","content":"from the card tool"}]}}`

	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("fixture is not JSON: %v", err)
	}

	tests := map[string]any{
		"json string": raw,
		"bytes":       []byte(raw),
		"go values":   decoded,
	}

	for name, card := range tests {
		t.Run(name, func(t *testing.T) {
			srv, got := server(200, `{"code":0,"msg":"success"}`)
			defer srv.Close()
			client := clientFor(t, srv, nil)

			err := client.Send(context.Background(), model.Message{
				Format: model.FormatMarkdown,
				Extras: map[string]any{ExtraCard: card},
			})
			if err != nil {
				t.Fatalf("Send() error = %v", err)
			}

			payload := decode(t, got.body)
			if payload["msg_type"] != MsgTypeInteractive {
				t.Errorf("msg_type = %v, want interactive", payload["msg_type"])
			}
			// Compared structurally: the driver re-encodes what it was given, and JSON
			// key order is not part of the card.
			if !reflect.DeepEqual(payload["card"], any(decoded)) {
				sent, _ := json.Marshal(payload["card"])
				t.Errorf("card = %s, want the caller's structure unchanged", sent)
			}
			if _, ok := payload["content"]; ok {
				t.Errorf("a card message must not also carry content: %s", got.body)
			}
		})
	}
}

// A raw card is somebody else's payload: anything the driver would have to inject
// into it is refused instead of being dropped on the floor.
func TestSendRawCardRefusesMessagePartsItCannotPlace(t *testing.T) {
	for name, msg := range map[string]model.Message{
		"mentions": {Format: model.FormatMarkdown, Recipients: []string{"ou_one"}, Extras: map[string]any{ExtraCard: map[string]any{"schema": CardSchema}}},
		"title":    {Title: "Lost", Format: model.FormatMarkdown, Extras: map[string]any{ExtraCard: map[string]any{"schema": CardSchema}}},
	} {
		t.Run(name, func(t *testing.T) {
			srv, got := server(200, `{"code":0,"msg":"success"}`)
			defer srv.Close()
			client := clientFor(t, srv, nil)

			err := client.Send(context.Background(), msg)
			if !stderrors.Is(err, errors.ErrDriverSendFailed) {
				t.Fatalf("error = %v, want ErrDriverSendFailed", err)
			}
			var typed *errors.NotifyutilsError
			if stderrors.As(err, &typed) && !strings.Contains(typed.DriverMessage(), ExtraCard) {
				t.Errorf("DriverMessage() = %q, want it to name the offending key", typed.DriverMessage())
			}
			if got.count() != 0 {
				t.Errorf("a payload that loses part of the message must not be sent")
			}
		})
	}
}

func TestSendRejectsMalformedRawCard(t *testing.T) {
	srv, got := server(200, `{"code":0,"msg":"success"}`)
	defer srv.Close()
	client := clientFor(t, srv, nil)

	for _, card := range []string{`{"schema":`, `"just text"`, `[1,2]`} {
		err := client.Send(context.Background(), model.Message{Format: model.FormatMarkdown, Extras: map[string]any{ExtraCard: card}})
		if !stderrors.Is(err, errors.ErrDriverSendFailed) {
			t.Errorf("card %q: error = %v, want ErrDriverSendFailed", card, err)
		}
	}
	if got.count() != 0 {
		t.Errorf("a broken card must not be sent")
	}
}

func TestSendRejectsHTMLFormat(t *testing.T) {
	srv, got := server(200, `{"code":0,"msg":"success"}`)
	defer srv.Close()
	client := clientFor(t, srv, nil)

	err := client.Send(context.Background(), model.Message{Text: "<b>hi</b>", Format: model.FormatHTML})
	if !stderrors.Is(err, errors.ErrUnsupportedFormat) {
		t.Fatalf("error = %v, want ErrUnsupportedFormat", err)
	}
	var typed *errors.NotifyutilsError
	if !stderrors.As(err, &typed) || typed.DriverName() != Name {
		t.Errorf("error should name the driver, got %v", err)
	}
	if got.count() != 0 {
		t.Errorf("an unrenderable format must not reach the network")
	}
}

func TestSendRejectsUnknownFormat(t *testing.T) {
	srv, _ := server(200, `{"code":0,"msg":"success"}`)
	defer srv.Close()
	client := clientFor(t, srv, nil)

	if err := client.Send(context.Background(), model.Message{Text: "x", Format: "yaml"}); !stderrors.Is(err, errors.ErrUnsupportedFormat) {
		t.Fatalf("error = %v, want ErrUnsupportedFormat", err)
	}
}

// The ceiling covers the encoded body, so the JSON overhead counts too: a text that
// still fits alone crosses the line once wrapped.
func TestSendRejectsOverlongBody(t *testing.T) {
	srv, got := server(200, `{"code":0,"msg":"success"}`)
	defer srv.Close()
	client := clientFor(t, srv, nil)

	err := client.Send(context.Background(), model.Message{
		Text:   strings.Repeat("a", MaxRequestBodyBytes),
		Format: model.FormatPlain,
	})
	if !stderrors.Is(err, errors.ErrMessageTooLong) {
		t.Fatalf("error = %v, want ErrMessageTooLong", err)
	}
	var typed *errors.NotifyutilsError
	if stderrors.As(err, &typed) && !strings.Contains(typed.DriverMessage(), strconv.Itoa(MaxRequestBodyBytes)) {
		t.Errorf("DriverMessage() = %q, want it to quote the ceiling", typed.DriverMessage())
	}
	if got.count() != 0 {
		t.Errorf("an over-long message must not be sent, truncated or not")
	}

	// The same message well under the ceiling still goes out.
	short := strings.Repeat("a", 1024)
	if err := client.Send(context.Background(), model.Message{Text: short, Format: model.FormatPlain}); err != nil {
		t.Fatalf("Send() error = %v, want the under-limit message to go out", err)
	}
	if got.count() != 1 {
		t.Errorf("requests = %d, want the short message to be the only further send", got.count())
	}
	if content := stringField(t, decode(t, got.body), "content", "text"); content != short {
		t.Errorf("content.text length = %d, want the untruncated body", len(content))
	}
}

// A rejection arrives as HTTP 200 with the verdict in the body, which is the failure
// mode this channel makes easiest to miss.
func TestSendTreatsCodeInHTTP200AsFailure(t *testing.T) {
	srv, _ := server(200, `{"code":19021,"msg":"sign match fail or timestamp is not within one hour from current time"}`)
	defer srv.Close()
	client := clientFor(t, srv, nil)

	err := client.Send(context.Background(), model.Message{Text: "x", Format: model.FormatPlain})
	if !stderrors.Is(err, errors.ErrDriverSendFailed) {
		t.Fatalf("error = %v, want ErrDriverSendFailed", err)
	}
	var typed *errors.NotifyutilsError
	if !stderrors.As(err, &typed) {
		t.Fatalf("error should be a *NotifyutilsError, got %T", err)
	}
	if typed.DriverName() != Name {
		t.Errorf("DriverName() = %q, want %q", typed.DriverName(), Name)
	}
	if typed.DriverCode() != "19021" {
		t.Errorf("DriverCode() = %q, want 19021", typed.DriverCode())
	}
	if !strings.Contains(typed.DriverMessage(), "sign match fail") {
		t.Errorf("DriverMessage() = %q, want the upstream wording", typed.DriverMessage())
	}
	if typed.DriverRequestID() != logID {
		t.Errorf("DriverRequestID() = %q, want the trace id %q", typed.DriverRequestID(), logID)
	}
	if typed.DriverResponse() == nil {
		t.Errorf("DriverResponse() = nil, want the raw body")
	}
}

func TestSendTreatsUnreadableBodyAsFailure(t *testing.T) {
	for _, body := range []string{"success", `{"msg":"no code"}`, ``} {
		srv, _ := server(200, body)

		client := clientFor(t, srv, nil)
		err := client.Send(context.Background(), model.Message{Text: "x", Format: model.FormatPlain})
		if !stderrors.Is(err, errors.ErrDriverSendFailed) {
			t.Errorf("body %q: error = %v, want ErrDriverSendFailed", body, err)
		}
		srv.Close()
	}
}

func TestSendNon2xxFails(t *testing.T) {
	srv, _ := server(403, `{"code":19022,"msg":"Ip Not Allowed"}`)
	defer srv.Close()
	client := clientFor(t, srv, nil)

	err := client.Send(context.Background(), model.Message{Text: "x", Format: model.FormatPlain})
	if !stderrors.Is(err, errors.ErrDriverSendFailed) {
		t.Fatalf("error = %v, want ErrDriverSendFailed", err)
	}
	var typed *errors.NotifyutilsError
	if !stderrors.As(err, &typed) || typed.DriverCode() != "403" {
		t.Errorf("error = %v, want DriverCode 403", err)
	}
}

func TestSendHonoursContext(t *testing.T) {
	srv, _ := server(200, `{"code":0,"msg":"success"}`)
	defer srv.Close()
	client := clientFor(t, srv, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := client.Send(ctx, model.Message{Text: "x", Format: model.FormatPlain})
	if err == nil {
		t.Fatalf("Send() error = nil, want the cancelled context to abort the request")
	}
	if !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("Send() error = %v, want the transport failure caused by the context", err)
	}
}

func TestSendDoesNotMutateMessage(t *testing.T) {
	srv, _ := server(200, `{"code":0,"msg":"success"}`)
	defer srv.Close()
	client := clientFor(t, srv, nil)

	ids := []string{"ou_one"}
	msg := model.Message{
		Title:      "T",
		Text:       "raw ${host}",
		Format:     model.FormatPlain,
		Recipients: ids,
		Extras:     map[string]any{ExtraAtUserIDs: ids, ExtraAtAll: true},
	}

	if err := client.Send(context.Background(), msg); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if msg.Text != "raw ${host}" || msg.Title != "T" {
		t.Errorf("the message was rewritten: %+v", msg)
	}
	if len(msg.Recipients) != 1 || msg.Recipients[0] != "ou_one" {
		t.Errorf("Recipients were rewritten: %v", msg.Recipients)
	}
	if extras := msg.ExtraStrings(ExtraAtUserIDs); len(extras) != 1 || extras[0] != "ou_one" {
		t.Errorf("Extras were rewritten: %v", msg.Extras)
	}
}
