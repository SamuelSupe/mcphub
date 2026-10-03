package configstore

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
)

func TestBackupRestoreSecurityBoundary(t *testing.T) {
	source, key, _ := newTestStore(t)
	testBackupRestore(t, source, key, nil)
}

func testBackupRestore(t *testing.T, source *Store, key []byte, target *Store) {
	t.Helper()
	ctx := t.Context()
	backend, err := source.Create(ctx, Record{Enabled: true, Config: config.BackendConfig{ID: "backup-service", URL: "https://backend.example.com/mcp", Headers: map[string]string{"Authorization": "private-backend-secret"}, PublishedTools: []string{"echo"}}})
	if err != nil {
		t.Fatal(err)
	}
	user, err := source.CreateLocalAccount(ctx, "admin", "Backup administrator", "backup-long-password", true)
	if err != nil {
		t.Fatal(err)
	}
	uid, policy, err := source.ClientEndpointPolicy(ctx, backend.Config.ID)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := source.EffectiveIdentity(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	grants, err := source.CreateNativeConnection(ctx, identity, []ClientGrant{{GrantBinding: GrantBinding{ClientID: "ci_backup_client_instance", EndpointUID: uid}, Issuer: "https://hub.example.com/sso", Subject: user.ID, Resource: "https://hub.example.com/mcp", ClientName: "Backup connection", EndpointID: backend.Config.ID, EndpointPolicy: policy, AllowedTools: []string{"echo"}, Capabilities: GrantCapabilities{Tools: true}}}, time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	session, refresh, err := source.CreateSSOSession(ctx, SSOSession{UserID: user.ID, CredentialVersion: user.CredentialVersion, ClientID: "desktop", Resource: "https://hub.example.com/mcp", ExpiresAt: time.Now().Add(time.Hour), Grants: grants}, true)
	if err != nil {
		t.Fatal(err)
	}
	approval, err := source.CreateApproval(ctx, ApprovalIntent{Issuer: "https://hub.example.com/sso", Subject: user.ID, Tool: "backup-service.write", Params: []byte(`{"arguments":{}}`)})
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(t.TempDir(), "snapshot")
	manifest, err := source.Backup(ctx, directory, "test", EncryptionKeyID(key), "deployment-hash")
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Rows < 10 || manifest.Bytes == 0 {
		t.Fatal("empty backup", manifest)
	}
	if _, err = VerifyBackup(directory, "wrong-key"); err == nil {
		t.Fatal("wrong key accepted")
	}
	if target == nil {
		target, err = Open(ctx, filepath.Join(t.TempDir(), "restored.db"), key)
		if err != nil {
			t.Fatal(err)
		}
		defer target.Close()
	}
	if _, err = target.RestoreBackup(ctx, directory, EncryptionKeyID(key)); err != nil {
		t.Fatal(err)
	}
	restored, err := target.Get(ctx, backend.Config.ID)
	if err != nil || restored.Config.Headers["Authorization"] != "private-backend-secret" || restored.Revision != backend.Revision {
		t.Fatal("configuration not restored", restored, err)
	}
	if _, _, err = target.AuthenticateLocal(ctx, "admin", "backup-long-password", "", false); err != nil {
		t.Fatal("account not restored", err)
	}
	if _, err = target.SSOSession(ctx, session.ID); err == nil {
		t.Fatal("restored access session accepted")
	}
	if _, err = target.SSORefreshSession(ctx, refresh); err == nil {
		t.Fatal("restored refresh token accepted")
	}
	if _, err = target.NativeGrant(ctx, grants[0], "https://hub.example.com/sso", user.ID, "https://hub.example.com/mcp"); err == nil {
		t.Fatal("restored grant accepted", err)
	}
	value, err := target.GetApproval(ctx, approval.ID)
	if err != nil || value.Status != "revoked" {
		t.Fatal("unfinished approval survived restore", value, err)
	}
	if _, err = source.SSOSession(ctx, session.ID); err != nil {
		t.Fatal("restore modified source", err)
	}
	if _, err = target.RestoreBackup(ctx, directory, EncryptionKeyID(key)); err == nil {
		t.Fatal("nonempty destination overwritten")
	}
	f, err := os.OpenFile(filepath.Join(directory, "database.jsonl"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("tampered")
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = VerifyBackup(directory, EncryptionKeyID(key)); err == nil {
		t.Fatal("tampered snapshot accepted")
	}
}
