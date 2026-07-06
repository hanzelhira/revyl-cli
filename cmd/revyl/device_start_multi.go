package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	mcppkg "github.com/revyl/cli/internal/mcp"
	"github.com/revyl/cli/internal/ui"
)

// multiStartResult is the compact per-session outcome of a parallel start.
type multiStartResult struct {
	Index     int    `json:"index"`
	Platform  string `json:"platform"`
	SessionID string `json:"session_id,omitempty"`
	ViewerURL string `json:"viewer_url,omitempty"`
	Error     string `json:"error,omitempty"`
}

// multiSessionStarter is the subset of DeviceSessionManager needed by
// runMultiDeviceStart, narrowed for testability.
type multiSessionStarter interface {
	StartSession(ctx context.Context, opts mcppkg.StartSessionOptions) (int, *mcppkg.DeviceSession, error)
}

// runMultiDeviceStart provisions count sessions per platform concurrently
// and prints one compact result per session (JSON array with --json).
func runMultiDeviceStart(ctx context.Context, mgr multiSessionStarter, platforms []string, count int, base mcppkg.StartSessionOptions, jsonOutput bool, w io.Writer) error {
	var specs []string
	for _, p := range platforms {
		for i := 0; i < count; i++ {
			specs = append(specs, p)
		}
	}

	if !jsonOutput {
		ui.PrintInfo("Starting %d device sessions in parallel...", len(specs))
	}

	results := make([]multiStartResult, len(specs))
	var wg sync.WaitGroup
	for i, p := range specs {
		wg.Add(1)
		go func(i int, platform string) {
			defer wg.Done()
			opts := base
			opts.Platform = platform
			_, session, err := mgr.StartSession(ctx, opts)
			if err != nil {
				results[i] = multiStartResult{Index: -1, Platform: platform, Error: err.Error()}
				return
			}
			results[i] = multiStartResult{
				Index:     session.Index,
				Platform:  platform,
				SessionID: session.SessionID,
				ViewerURL: session.ViewerURL,
			}
		}(i, p)
	}
	wg.Wait()

	failed := 0
	if jsonOutput {
		enc := json.NewEncoder(w)
		if err := enc.Encode(results); err != nil {
			return err
		}
		for _, r := range results {
			if r.Error != "" {
				failed++
			}
		}
	} else {
		for _, r := range results {
			if r.Error != "" {
				failed++
				ui.PrintError("%s session failed: %s", r.Platform, r.Error)
				continue
			}
			ui.PrintSuccess("Session %d ready (%s) — %s", r.Index, r.Platform, r.SessionID)
			if r.ViewerURL != "" {
				ui.PrintLink("Live View", r.ViewerURL)
			}
		}
		if failed == 0 {
			ui.PrintInfo("Target a session with -s <index>, e.g. revyl device screenshot -s %d", results[0].Index)
		}
	}

	if failed > 0 {
		return fmt.Errorf("%d/%d sessions failed to start", failed, len(specs))
	}
	return nil
}
