package lark

// Name is the key this driver registers under and the driver name carried by every
// error it returns.
const Name = "lark"

// Webhook is the custom robot's hook URL, token included
// (https://open.feishu.cn/open-apis/bot/v2/hook/<token>).
const Webhook = "webhook"

// Secret is optional. It is the 密钥 shown next to the 签名校验 security setting,
// and only needed when that setting is on: the hook URL alone is enough otherwise.
const Secret = "secret"

// ContentType is the header the documentation's curl example sends.
const ContentType = "application/json"

// MaxRequestBodyBytes is the documented ceiling for one call ("发送消息时，请求体的
// 数据大小不能超过 20 KB"). The document does not say whether KB means 1000 or 1024
// bytes, so the smaller reading wins: anything this driver lets through is
// something the upstream states it accepts. It applies to the whole body, which is
// why it is checked after marshalling.
const MaxRequestBodyBytes = 20_000

// msg_type values. A card goes out as interactive in both the guide and the card
// tutorial ("在卡片场景下，消息类型为 interactive").
const (
	MsgTypeText        = "text"
	MsgTypeInteractive = "interactive"
)

// CardSchema is the card structure version the custom robot examples use.
const CardSchema = "2.0"

// Card element and text tags, verbatim from the documentation's card samples.
const (
	CardTagMarkdown  = "markdown"
	CardTagPlainName = "plain_text"
)

// MentionAll addresses everybody in the group. The group has to have that enabled,
// otherwise the card send fails outright.
const MentionAll = "all"

// HeaderLogID names the trace id Feishu puts on every API response; the docs ask
// callers to quote it when reporting a failure.
const HeaderLogID = "X-Tt-Logid"

// Extras keys, namespaced with the driver name because the core hands one shared
// map to every routed channel.
const (
	// ExtraAtUserIDs lists open_ids or user_ids to @ ([]string or string).
	ExtraAtUserIDs = "lark/atUserIDs"
	// ExtraAtAll notifies the whole group when set to true.
	ExtraAtAll = "lark/atAll"
	// ExtraCard sends a card verbatim: a card JSON 2.0 structure, a
	// {"type":"template","data":{...}} template card, or either one as a JSON
	// string. It overrides the format-derived payload, so the message must not
	// also ask for mentions, which cannot be injected into someone else's card.
	ExtraCard = "lark/card"
)
