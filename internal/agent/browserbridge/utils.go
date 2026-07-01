package browserbridge

import (
	"errors"
	"time"
)

func timeAfterSecs(n int) time.Time { return time.Now().Add(time.Duration(n) * time.Second) }

func errFromExtension(msg string) error { return errors.New(msg) }

func safePrefix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
