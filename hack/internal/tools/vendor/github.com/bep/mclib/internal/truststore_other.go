//go:build dragonfly || freebsd || openbsd || netbsd || solaris

package internal

import (
	"fmt"
	"runtime"
)

var (
	FirefoxProfiles     = []string{}
	CertutilInstallHelp = ""
	NSSBrowsers         = ""
)

func (m *mkcert) installPlatform() bool {
	panic(fmt.Sprintf("installing root on %s is currently not supported", runtime.GOOS))
	return false
}

func (m *mkcert) uninstallPlatform() bool {
	panic(fmt.Sprintf("uninstalling root on %s is currently not supported", runtime.GOOS))
	return false
}
