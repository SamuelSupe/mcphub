package app

import "context"

func (a *App) personalAccountStatus(ctx context.Context, rt *runtime, endpoint, issuer, subject string) string {
	c, ok := rt.cfg.Backend(endpoint)
	if !ok || c.Credentials == nil || c.Credentials.Mode != "personal" {
		return "not_required"
	}
	if subject == "" {
		return "subject_required"
	}
	if a.credentials == nil || a.store == nil {
		return "unavailable"
	}
	binding, err := a.store.CredentialBinding(ctx, issuer, subject, c.EndpointUID)
	if err != nil {
		return "unavailable"
	}
	_, status := a.credentials.Status(ctx, accountEndpoint(c), binding)
	return status
}
