package app

import (
	"context"
	"time"
)

func (a *App) runApprovalMaintenance() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		ctx, cancel := context.WithTimeout(a.ctx, 10*time.Second)
		err := a.store.MaintainApprovals(ctx, a.currentConfig().Admin.Approvals.RetainDuration())
		if err == nil && a.currentConfig().ClientAuthorization.Enabled {
			err = a.store.MaintainClientGrants(ctx, a.currentConfig().Admin.Approvals.RetainDuration())
			if rt := a.currentRuntime(); rt != nil {
				rt.hub.PruneClientGrantViews(ctx)
			}
		}
		if a.currentConfig().Auth.SSO != nil {
			if rt := a.currentRuntime(); rt != nil {
				rt.hub.PruneIdentityViews(ctx)
			}
		}
		if a.credentials != nil {
			if cleanupErr := a.credentials.Maintain(ctx); cleanupErr != nil {
				a.logger.Warn("upstream credential cleanup will retry")
			}
		}
		cancel()
		if err != nil && a.ctx.Err() == nil {
			a.logger.Warn("approval maintenance failed")
		}
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
