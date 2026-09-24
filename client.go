package lark

import (
	"net/http"
	"net/url"
	"text/template"

	"go.gh.ink/notifyutils/errors"
	"go.gh.ink/notifyutils/model"
)

// Client is one configured group robot. It is used as a value throughout: it holds
// no mutable state, so a copy is cheap and the model.Client contract — safe for
// concurrent Send calls — needs no locking.
type Client struct {
	// Webhook is the address sent to verbatim.
	Webhook string
	// Secret is empty when the robot has no 签名校验 configured, and then the
	// request carries neither timestamp nor sign.
	Secret string

	// Card defaults, all optional and compiled at construction so a broken template
	// fails when the channel is built rather than on the first alert.
	// CardTemplate is a whole card JSON rendered against model.Message;
	// CardTemplateID names a 搭建工具 template whose placeholders are filled from
	// CardVariables, whose values are rendered the same way. Setting both is refused.
	CardTemplate        *template.Template
	CardTemplateID      string
	CardTemplateVersion string
	CardVariables       map[string]*template.Template

	Client *http.Client

	Marshal   func(any) ([]byte, error)
	Unmarshal func([]byte, any) error
}

type Driver struct{}

func (d Driver) NewClient(params model.DriverClientParam) (model.Client, error) {
	webhook := params.Credential[Webhook]

	// The token is the last path segment of the hook URL, but its shape is not what
	// authenticates the call here — the whole URL is the credential. So only the
	// bare minimum is checked, which keeps a self-hosted gateway or a rewrite proxy
	// in front of the hook usable.
	target, err := url.Parse(webhook)
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" || target.Path == "/" || target.Path == "" {
		return nil, errors.ErrDriverCredentialInvalid.
			WithDriverName(Name).
			WithDriverMessage("webhook must be the custom robot hook URL, token included").
			WithDriverResponse(webhook)
	}

	client := Client{
		Webhook: webhook,
		Secret:  params.Credential[Secret],

		Client: params.HTTPClient,

		Marshal:   params.Marshal,
		Unmarshal: params.Unmarshal,
	}

	if err := client.configureCards(params.Credential); err != nil {
		return nil, err
	}

	return client, nil
}

// configureCards compiles the card credentials. Every rejection here is a combination
// that would otherwise send something the upstream reads as a valid request but not as
// the card the configuration describes.
func (c *Client) configureCards(credential map[string]string) error {
	rawCard := credential[CardTemplate]
	rawID := credential[CardTemplateID]
	rawVariables := credential[CardVariables]

	if rawCard != "" && rawID != "" {
		return credentialProblem("cardTemplate and cardTemplateID are alternatives: pick one card mode")
	}
	if rawVariables != "" && rawID == "" {
		return credentialProblem("cardVariables needs cardTemplateID to fill")
	}
	if credential[CardTemplateVersion] != "" && rawID == "" {
		return credentialProblem("cardTemplateVersion needs cardTemplateID to qualify")
	}

	if rawCard != "" {
		parsed, err := c.cardTemplate(CardTemplate).Parse(rawCard)
		if err != nil {
			return credentialProblem(CardTemplate + ": " + err.Error())
		}
		c.CardTemplate = parsed
		return nil
	}

	if rawID == "" {
		return nil
	}
	c.CardTemplateID = rawID
	c.CardTemplateVersion = credential[CardTemplateVersion]

	if rawVariables == "" {
		return nil
	}

	// Values stay strings all the way in: a variable rendered as a JSON number would
	// let a phone-like value arrive re-typed, and the card offers no way to notice.
	var variables map[string]string
	if err := c.Unmarshal([]byte(rawVariables), &variables); err != nil {
		return credentialProblem(CardVariables + " must be a JSON object of template strings: " + err.Error())
	}

	compiled := make(map[string]*template.Template, len(variables))
	for key, text := range variables {
		parsed, err := c.cardTemplate(key).Parse(text)
		if err != nil {
			return credentialProblem(CardVariables + " [" + key + "]: " + err.Error())
		}
		compiled[key] = parsed
	}
	c.CardVariables = compiled

	return nil
}

// cardTemplate starts a compiled card template with the driver's options and helpers.
//
// The json helper exists because a whole-card template writes into JSON text: a title
// containing a quote or a newline interpolated as {{.Title}} breaks the document, while
// {{json .Title}} emits it as an escaped JSON string literal, quotes included.
func (c Client) cardTemplate(name string) *template.Template {
	return template.New(name).Option(missingKeyOption).Funcs(template.FuncMap{
		"json": func(value any) (string, error) {
			encoded, err := c.Marshal(value)
			if err != nil {
				return "", err
			}
			return string(encoded), nil
		},
	})
}

func credentialProblem(message string) error {
	return errors.ErrDriverCredentialInvalid.
		WithDriverName(Name).
		WithDriverMessage(message)
}
