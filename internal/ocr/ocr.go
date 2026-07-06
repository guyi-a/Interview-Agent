// Package ocr wraps a local tesseract CLI so upstream callers (document
// extractors, HTTP handlers) can turn image bytes into text without dealing
// with subprocess plumbing themselves.
package ocr

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// LangDefault covers the mixed CN+EN content typical of resumes / slides.
	LangDefault = "chi_sim+eng"

	// MaxImageBytes is the per-image size cap. Anything larger is skipped as
	// a soft failure so a single huge screenshot cannot stall a whole doc.
	MaxImageBytes = 5 * 1024 * 1024

	// DefaultPerImageTimeout bounds each tesseract invocation. Callers that
	// need a different budget override via Options.Timeout.
	DefaultPerImageTimeout = 10 * time.Second
)

// Sentinel warning strings — exported so callers can build stable markers
// and tests can assert on them without brittle substring matches.
const (
	WarnNotInstalled    = "tesseract not installed"
	WarnImageTooLarge   = "image too large"
	WarnEmptyImage      = "empty image"
	WarnTimeout         = "OCR timeout"
	WarnNoText          = "no text detected"
	WarnSubprocessError = "OCR failed"
)

// Result captures the outcome of a single OCR attempt.
//
//   - Text non-empty            → success
//   - Text empty, Warning set   → soft failure; caller can inline the reason
//   - Text empty, Warning empty → treat as no text detected (defensive)
type Result struct {
	Text    string
	Warning string
}

// Options tune a single Run call. Nil means all defaults.
type Options struct {
	Lang    string
	Timeout time.Duration
}

var (
	availableOnce   sync.Once
	availableResult bool
)

// Available reports whether the local `tesseract` binary is on PATH.
// Cached for the process lifetime.
func Available() bool {
	availableOnce.Do(func() {
		_, err := exec.LookPath("tesseract")
		availableResult = err == nil
	})
	return availableResult
}

// Run performs OCR on the given image bytes. `name` is a human-readable
// label (usually the source filename) used to pick a tempfile extension so
// tesseract's format sniff is reliable; it does not need to match a real
// file on disk.
//
// Return contract:
//
//   - err != nil            → hard failure (fs / io / cancelled ctx). Caller
//     should abort the whole extraction if the parent ctx is cancelled, or
//     surface the raw error otherwise.
//   - err == nil, Warning   → soft failure; Text is empty. Caller emits an
//     inline marker with the reason and moves on to the next image.
//   - err == nil, Text      → success.
func Run(ctx context.Context, image []byte, name string) (Result, error) {
	return RunWithOptions(ctx, image, name, nil)
}

// RunWithOptions is Run with explicit language / timeout overrides.
func RunWithOptions(ctx context.Context, image []byte, name string, opts *Options) (Result, error) {
	if opts == nil {
		opts = &Options{}
	}
	lang := opts.Lang
	if lang == "" {
		lang = LangDefault
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultPerImageTimeout
	}

	if !Available() {
		return Result{Warning: WarnNotInstalled}, nil
	}
	if len(image) == 0 {
		return Result{Warning: WarnEmptyImage}, nil
	}
	if len(image) > MaxImageBytes {
		return Result{
			Warning: fmt.Sprintf("%s (%d KiB > %d KiB cap)",
				WarnImageTooLarge, len(image)/1024, MaxImageBytes/1024),
		}, nil
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	ext := filepath.Ext(name)
	if ext == "" || len(ext) > 8 {
		ext = ".img"
	}
	tmp, err := os.CreateTemp("", "ocr-*"+ext)
	if err != nil {
		return Result{}, fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(image); err != nil {
		tmp.Close()
		return Result{}, fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return Result{}, fmt.Errorf("close temp file: %w", err)
	}

	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(tctx, "tesseract", tmpPath, "-", "-l", lang)
	out, err := cmd.Output()
	if err != nil {
		// Parent ctx cancelled — propagate as hard error so callers can
		// abort the whole extraction.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Result{}, ctxErr
		}
		// Our per-image budget fired — soft skip, keep going.
		if errors.Is(tctx.Err(), context.DeadlineExceeded) {
			return Result{
				Warning: fmt.Sprintf("%s after %s", WarnTimeout, timeout),
			}, nil
		}
		// Subprocess non-zero: usually a bad image format or unsupported
		// codec. Bubble up as a soft warning with the tesseract stderr's
		// first line for diagnosis.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			stderr := firstLine(strings.TrimSpace(string(exitErr.Stderr)))
			if stderr != "" {
				return Result{
					Warning: fmt.Sprintf("%s: %s", WarnSubprocessError, stderr),
				}, nil
			}
		}
		return Result{Warning: fmt.Sprintf("%s: %v", WarnSubprocessError, err)}, nil
	}

	text := strings.TrimSpace(string(out))
	if text == "" {
		return Result{Warning: WarnNoText}, nil
	}
	return Result{Text: text}, nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
