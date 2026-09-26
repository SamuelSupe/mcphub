package app

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
)

func (a *App) runApprovalDelivery() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for {
		for _, archive := range []bool{true, false} {
			for range 50 {
				settings := a.currentConfig().Admin.Approvals
				if (archive && settings.AuditArchive.URL == "") || (!archive && settings.Notifications.URL == "") {
					break
				}
				value, err := a.store.NextApprovalDelivery(a.ctx, archive)
				if errors.Is(err, configstore.ErrNotFound) {
					break
				}
				if err != nil {
					a.logger.Warn("approval delivery queue unavailable")
					break
				}
				err = a.deliverApproval(a.ctx, client, value, archive)
				if err != nil {
					a.logger.Warn("approval delivery failed; durable retry scheduled", "archive", archive, "event_id", value.EventID, "attempt", value.Attempts+1)
				}
				if saveErr := a.store.CompleteApprovalDelivery(a.ctx, value, archive, err == nil); saveErr != nil {
					a.logger.Warn("approval delivery acknowledgement could not be saved")
					break
				}
				if err != nil {
					break
				}
			}
		}
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *App) deliverApproval(ctx context.Context, client *http.Client, value configstore.ApprovalDelivery, archive bool) error {
	settings := a.currentConfig().Admin.Approvals
	target := settings.Notifications.URL
	if archive {
		target = settings.AuditArchive.URL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(value.Payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-MCPHub-Event-ID", value.EventID)
	if !archive {
		stamp := strconv.FormatInt(time.Now().Unix(), 10)
		mac := hmac.New(sha256.New, []byte(settings.Notifications.Secret))
		mac.Write([]byte(stamp + "."))
		mac.Write(value.Payload)
		req.Header.Set("X-MCPHub-Timestamp", stamp)
		req.Header.Set("X-MCPHub-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return errors.New("delivery rejected")
	}
	if archive {
		var envelope configstore.AuditEnvelope
		var entry configstore.AuditEntry
		var ack struct {
			Sequence int64  `json:"sequence"`
			Hash     string `json:"hash"`
		}
		if json.Unmarshal(value.Payload, &envelope) != nil || json.Unmarshal(envelope.Entry, &entry) != nil || json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&ack) != nil || ack.Sequence != entry.Sequence || ack.Hash != envelope.Hash {
			return errors.New("archive acknowledgement does not match the signed record")
		}
	}
	return nil
}
