package browseruse

import (
	"bytes"
	"fmt"

	"github.com/playwright-community/playwright-go"
)

// InstallStatus reports what the local machine has vs. what's still needed
// before a Session can boot. Two flags because the two artifacts (driver
// and browser binary) install through different pipelines.
type InstallStatus struct {
	DriverReady   bool   `json:"driver_ready"`
	BrowsersReady bool   `json:"browsers_ready"`
	Message       string `json:"message"`
}

// CheckInstall reports whether the playwright driver + chromium binary are
// both installed. Cheap: just tries a dry-run install.
func CheckInstall() InstallStatus {
	var buf bytes.Buffer
	err := playwright.Install(&playwright.RunOptions{
		DryRun:   true,
		Browsers: []string{"chromium"},
		Stdout:   &buf,
		Stderr:   &buf,
		Verbose:  false,
	})
	if err != nil {
		return InstallStatus{
			Message: fmt.Sprintf("尚未安装：%v。下一步：调 browser_use_install(step='install')。", err),
		}
	}
	return InstallStatus{
		DriverReady:   true,
		BrowsersReady: true,
		Message:       "playwright driver 和 chromium 已就绪，可以直接调 browser_use。",
	}
}

// DoInstall downloads the playwright driver and the chromium browser to
// the OS user cache. Blocks until finished; large first-time download
// (~150MB). Progress goes to stderr of the current process.
func DoInstall() error {
	return playwright.Install(&playwright.RunOptions{
		Browsers: []string{"chromium"},
		Verbose:  true,
	})
}
