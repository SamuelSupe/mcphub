package backend

import (
	"context"
	"net/http"
)

type credentialTransport struct {
	base      http.RoundTripper
	authorize func(context.Context) (http.Header, error)
}

func (t *credentialTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	headers, err := t.authorize(req.Context())
	if err != nil {
		return nil, err
	}
	copy := req.Clone(req.Context())
	for name, values := range headers {
		copy.Header[name] = values
	}
	return t.base.RoundTrip(copy)
}
