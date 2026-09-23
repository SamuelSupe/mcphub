package client

import (
	"runtime"
	"syscall"

	"golang.org/x/sys/windows"
)

func openBrowser(rawURL string) error {
	url, err := windows.UTF16PtrFromString(rawURL)
	if err != nil {
		return err
	}
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	// S_FALSE (1) is also success and needs a matching CoUninitialize.
	if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED|windows.COINIT_DISABLE_OLE1DDE); err != nil && err != syscall.Errno(1) {
		return err
	}
	defer windows.CoUninitialize()
	return windows.ShellExecute(0, verb, url, nil, nil, windows.SW_SHOWNORMAL)
}
