package lark

import (
	"net/http"
	"net/url"

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

	return Client{
		Webhook: webhook,
		Secret:  params.Credential[Secret],

		Client: params.HTTPClient,

		Marshal:   params.Marshal,
		Unmarshal: params.Unmarshal,
	}, nil
}
