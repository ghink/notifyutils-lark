# notifyutils-lark

飞书 / Lark 群「自定义机器人」driver for notifyutils: `go.gh.ink/notifyutils/lark`.

The official SDKs (`larksuite/oapi-sdk-go`) speak the application-robot protocol — app credentials,
tenant_access_token, `im/v1/messages` — which is a different integration from a group webhook that
already exists. This driver needs none of that, so it is standard library only, plus the core's
`utils/httpx` and `utils/sign`.

## Credentials

| Key | Required | Meaning |
|-----|----------|---------|
| `webhook` | yes | The hook URL, token included: `https://open.feishu.cn/open-apis/bot/v2/hook/<token>` |
| `secret` | no | The 密钥 behind the robot's 签名校验 security setting. Empty means no signature is computed or sent |
| `cardTemplateID`, `cardTemplateVersion`, `cardVariables`, `cardTemplate` | no | Describe a card once in config instead of per message: see [Cards from the credentials](#cards-from-the-credentials) |

A missing `webhook` fails at `NewClient` with `errors.ErrDriverCredentialInvalid`. The robot's other
two security settings — 关键词 and IP 白名单 — are server-side and need nothing here; a message that
misses the keyword comes back as `code 19024`.

## Recipients means "who to @", not "where to send"

The hook URL already picks the group, so `msg.Recipients` is the mention list: **open_id or user_id
values**, which is all a custom robot may use — it holds no data permissions and is explicitly not
allowed to address anybody by email or mobile number. Empty means nobody is addressed.

| `msg.Format` | Payload | Mention syntax |
|--------------|---------|----------------|
| `plain` | `msg_type: "text"`, `content.text` | `<at user_id="ou_x">ou_x</at>`, `<at user_id="all">所有人</at>` |
| `markdown` | `msg_type: "interactive"` + `card` (schema `2.0`), one `markdown` element | `<at id=ou_x></at>`, `<at id=all></at>` |
| `html` | `errors.ErrUnsupportedFormat` | — |

`markdown` goes out as a card rather than a `post` because post's documented tags (`text`, `a`,
`at`, `img`) print their content literally, so markdown would arrive as source code, while the card
`markdown` element is documented as carrying markdown syntax itself.

Titles: a card has a headline slot, so `msg.Title` becomes `card.header.title` (and the header is
omitted entirely when there is no title). A text message has none, so the title folds onto the first
line.

## Cards from the credentials

A card does not have to be written by the sending code. Four credentials describe it once, and the
driver assembles and renders the card per message. The template dot is `model.Message` — the same
documented context as every other channel here, with `Vars` already bound into `Title` and `Text` by
the core.

| Key | Meaning |
|-----|---------|
| `cardTemplateID` | A card built in the 卡片搭建工具, referenced by its template id |
| `cardTemplateVersion` | Optional `template_version_name` for that template |
| `cardVariables` | JSON object mapping the template's placeholder names to **template strings**, e.g. `{"content":"{{.Text}}"}` |
| `cardTemplate` | A whole card JSON of your own, rendered the same way — the alternative to `cardTemplateID` |

```go
credential := map[string]string{
	lark.Webhook:         "https://open.feishu.cn/open-apis/bot/v2/hook/…",
	lark.CardTemplateID:  "AAqyBQVmUN0w",
	lark.CardVariables:   `{"content":"{{.Text}}","peer":"{{.Extras.peer}}"}`,
}
```

Decisions the driver makes so callers do not have to:

- **Variable values are always strings.** `cardVariables` must decode to `map[string]string`, so a
  phone-shaped value such as `13800138000` reaches the card as text rather than being re-typed as
  `1.38e+10` by a JSON number.
- **The envelope is assembled from Go values**, then encoded once: quotes and newlines inside a
  rendered variable cannot break out of their JSON string.
- **`{{json …}}` for whole-card templates.** `cardTemplate` interpolates into JSON *text*, so
  `{{.Title}}` with a quote in it corrupts the document — the driver refuses to send that rather
  than shipping a half-parseable card. Write `"content":{{json .Title}}` (no surrounding quotes: the
  helper emits a quoted, escaped JSON string literal) whenever the value comes from the message.
- **A card on the message wins.** `lark/card` in `Extras` overrides any credential card, and keeps
  its own rule of refusing a title or mention list it cannot place. A credential card does *not*
  refuse them: it is a template, so it can put `{{.Title}}` wherever it likes.
- **A missing key fails loudly.** Field-style lookups (`{{.Extras.peer}}`) on an absent key error at
  send time rather than printing Go's `<no value>` into the card. `{{index .Extras "peer-id"}}` is
  outside that check and still renders `<no value>`, so use the field style for optional keys.
- The rendered card still passes the documented 20 KB request-body ceiling, measured after encoding.

## Extras

| Key | Type | Effect |
|-----|------|--------|
| `lark/atUserIDs` | `[]string` / `string` | open_ids / user_ids to @ |
| `lark/atAll` | `bool` | @ the whole group; needs the group's @所有人 permission, otherwise the send fails |
| `lark/card` | any / JSON string / `[]byte` | Send this card structure verbatim under `card`, overriding both `Format` and any card configured in the credentials — a hand-built card, or a 搭建工具 template `{"type":"template","data":{"template_id":…}}`. Because it is sent untouched, a message that also carries `Title` or mentions is refused rather than silently stripped |

The keys are exported as `ExtraAtUserIDs`, `ExtraAtAll` and `ExtraCard`; `MentionAll` is the `all`
value a card's `<at id=all></at>` tag carries, and `MaxRequestBodyBytes` the ceiling below.

## Signature

With `secret` set, the body gains two fields — `timestamp` (seconds, stringified) and `sign`. The
documented recipe is easy to get backwards: the HMAC-SHA256 **key** is the string
`<timestamp>\n<secret>` and the signed **message is empty**, and the result is base64 encoded:

```go
sign.HMACSHA256Base64([]byte(timestamp+"\n"+secret), nil)
```

Both fields travel in the **request body**, not in the URL query. A timestamp older than one hour
(3600 s) fails as `code 19021`, as does clock skew on the sending host.

## Body ceiling

One request body may not exceed **20 KB**; the doc does not say whether that is 1000 or 1024 bytes,
so `MaxRequestBodyBytes = 20_000` takes the smaller reading. The check runs on the encoded body — the
JSON overhead and the signature fields count — and an over-long message returns
`errors.ErrMessageTooLong` without being sent. Nothing is truncated into a partial notification.

## Failure shape

This channel answers **HTTP 200 even when it rejects the message**, so `code` in the body
is the verdict: non-zero becomes `errors.ErrDriverSendFailed` with `code` as `DriverCode`, `msg` as
`DriverMessage` and the raw body as `DriverResponse`. A 200 whose body is not the documented
`{code,msg}` JSON is a failure as well, since it does not confirm delivery. `DriverRequestID` carries
the `X-Tt-Logid` response header, which is what the docs ask you to quote when reporting a problem.
Documented codes: `9499` Bad Request (body shape), `19021` sign mismatch, `19022` IP not allowed,
`19024` keyword not found, `11232` rate limited.

Frequency: 单租户单机器人 **100 次/分钟, 5 次/秒**; the docs advise avoiding the top and half of the
hour, where a burst is more likely to hit `11232`.

## Sources

- 自定义机器人使用指南 — <https://open.feishu.cn/document/client-docs/bot-v3/add-custom-bot> (2025-03-26):
  hook URL form; 「将`timestamp + "\n" + 密钥`当做签名字符串，使用 HmacSHA256 算法计算空字符串的签名结果，
  再进行 Base64 编码」 with `timestamp` within one hour; `timestamp`/`sign` as body fields; the
  msgtype list (text, post, image, share_chat, interactive); the `<at …>` syntax per type; 「发送消息时，
  请求体的数据大小不能超过 20 KB」; 「单租户单机器人 100 次/分钟，5 次/秒」; the `{code,msg,data}` replies
  including `9499`, `19021`, `19022`, `19024`.
- 使用自定义机器人发送飞书卡片 — <https://open.feishu.cn/document/uAjLw4CM/ukzMukzMukzM/feishu-cards/quick-start/send-message-cards-with-custom-bot>:
  「在卡片场景下，消息类型为 `interactive`」 and the card goes in the top-level `card` **object** — both
  `content` and `card` are JSON objects in the webhook body; the stringified `content` belongs to the
  `im/v1/messages` API, not to this one. Also the `{"type":"template","data":{…}}` template-card shape
  behind `lark/card`.
- 卡片 Markdown 组件 — <https://open.feishu.cn/document/uAjLw4CM/ukzMukzMukzM/feishu-cards/card-components/content-components/rich-text>:
  `<at id=open_id></at>`, `<at id=all></at>` inside a card's markdown content, and the note that a
  custom robot may only @ by `open_id` / `user_id`.
- 调用 API — <https://open.feishu.cn/document/server-docs/api-call-guide/calling-process/get->:
  「如果依然不能解决问题，可以向飞书开放平台反馈响应头中的 `x-tt-logid` 值」, the id this driver lifts
  into `DriverRequestID`.

**Not verified:** the guide states one ceiling for the whole body (20 KB) and no per-field length for
`content.text`, `post` or `card`, so the driver checks nothing else. Whether the hook response
actually carries `x-tt-logid` — the header is documented for open-platform API responses in general,
not for this endpoint — was not confirmed against the live service; `DriverRequestID` simply stays
empty when it is absent.

## Requirements

Go 1.26.0+, standard library only.

## License

[Apache License 2.0](LICENSE)
