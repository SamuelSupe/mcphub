package client

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestCredentialStoreRejectsSharedWindowsPermissions(t *testing.T) {
	for _, target := range []string{"directory", "profile", "lock"} {
		t.Run(target, func(t *testing.T) {
			store := &Store{Dir: filepath.Join(t.TempDir(), "credentials")}
			ctx := context.Background()
			if err := store.locked(ctx, "work", func() error {
				return store.save("work", &profile{Version: 1, Session: "test", ClientID: "cli", ServerURL: "https://hub.example/mcp", Issuer: "https://idp.example", TokenURL: "https://idp.example/token"})
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Status(ctx, "work"); err != nil {
				t.Fatalf("new credential store is unusable: %v", err)
			}
			path := store.Dir
			if target == "profile" {
				path = filepath.Join(path, "work.json")
			} else if target == "lock" {
				path = filepath.Join(path, "work.lock")
			}
			user, err := windows.GetCurrentProcessToken().GetTokenUser()
			if err != nil {
				t.Fatal(err)
			}
			sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + user.User.Sid.String() + ")(A;OICI;GR;;;WD)")
			if err != nil {
				t.Fatal(err)
			}
			dacl, _, err := sd.DACL()
			if err != nil {
				t.Fatal(err)
			}
			if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Status(ctx, "work"); err == nil {
				t.Fatal("credential store accepted access granted to Everyone")
			}
		})
	}
}

func TestCredentialStoreRejectsWindowsSymlink(t *testing.T) {
	store := &Store{Dir: filepath.Join(t.TempDir(), "credentials")}
	ctx := context.Background()
	if _, err := store.Status(ctx, "work"); err != nil {
		t.Fatal(err)
	}
	outside := &Store{Dir: filepath.Join(t.TempDir(), "outside")}
	if err := outside.locked(ctx, "work", func() error {
		return outside.save("work", &profile{Version: 1, Session: "test", ClientID: "cli", ServerURL: "https://hub.example/mcp", Issuer: "https://idp.example", TokenURL: "https://idp.example/token"})
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside.Dir, "work.json"), filepath.Join(store.Dir, "work.json")); err != nil {
		t.Skipf("creating a symlink needs Windows Developer Mode or symlink privilege: %v", err)
	}
	if _, err := store.Status(ctx, "work"); err == nil {
		t.Fatal("credential store followed a symbolic link")
	}
}
