package tui

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// clipboardTimeout caps how long the platform clipboard subprocess is
// allowed to run before /copy reports a failure to the user. Real
// pbcopy / xclip invocations finish in milliseconds; the timeout is
// belt-and-suspenders against a hung child blocking the TUI.
const clipboardTimeout = 2 * time.Second

// copyToClipboard writes text to the OS clipboard via the platform's
// native command-line tool. The pure-Go alternatives (golang.design/x/
// clipboard, atotto/clipboard's older versions) require cgo for native
// frameworks on macOS; subprocess invocation keeps Lernen cgo-free and
// preserves the cross-compile invariant.
//
// macOS: pbcopy is shipped in /usr/bin/ and always available.
// Linux: tries wl-copy (Wayland), then xclip, then xsel (X11). The
// first one found on $PATH wins.
// Windows: clip.exe is shipped in C:\Windows\System32\.
//
// On unsupported platforms or when no clipboard tool is found, the
// function returns an error the caller surfaces via a system turn —
// /copy fails gracefully without crashing the session.
func copyToClipboard(ctx context.Context, text string) error {
	ctx, cancel := context.WithTimeout(ctx, clipboardTimeout)
	defer cancel()

	cmd, err := clipboardCommand(ctx)
	if err != nil {
		return err
	}
	cmd.Stdin = strings.NewReader(text)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("clipboard tool %q failed: %w", cmd.Path, err)
	}
	return nil
}

// clipboardCommand resolves the platform's clipboard write tool to an
// exec.Cmd ready to receive text on stdin.
func clipboardCommand(ctx context.Context) (*exec.Cmd, error) {
	switch runtime.GOOS {
	case "darwin":
		return exec.CommandContext(ctx, "pbcopy"), nil
	case "windows":
		return exec.CommandContext(ctx, "clip"), nil
	case "linux":
		// Prefer Wayland-native tool, fall back through X11 options.
		for _, name := range []string{"wl-copy", "xclip", "xsel"} {
			if path, err := exec.LookPath(name); err == nil {
				switch name {
				case "xclip":
					return exec.CommandContext(ctx, path, "-selection", "clipboard"), nil
				case "xsel":
					return exec.CommandContext(ctx, path, "--clipboard", "--input"), nil
				default:
					return exec.CommandContext(ctx, path), nil
				}
			}
		}
		return nil, errors.New("no clipboard tool found (install wl-copy, xclip, or xsel)")
	default:
		return nil, fmt.Errorf("clipboard not supported on %s", runtime.GOOS)
	}
}
