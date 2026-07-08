package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	mcppkg "github.com/revyl/cli/internal/mcp"
	"github.com/revyl/cli/internal/ui"
)

// ensureSessionAPI is the subset of DeviceSessionManager needed by ensure,
// narrowed for testability.
type ensureSessionAPI interface {
	ListSessions() []*mcppkg.DeviceSession
	CheckSessionAlive(ctx context.Context, session *mcppkg.DeviceSession) (bool, string)
	StopSession(ctx context.Context, index int) error
	StartSession(ctx context.Context, opts mcppkg.StartSessionOptions) (int, *mcppkg.DeviceSession, error)
}

type ensureResult struct {
	Index     int    `json:"index"`
	Platform  string `json:"platform"`
	Label     string `json:"label,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	ViewerURL string `json:"viewer_url,omitempty"`
	Reused    bool   `json:"reused"`
}

// ensureDeviceSession returns a live session matching the given label and/or
// platform, reusing one when possible and provisioning otherwise. A matching
// but dead session is stopped (best effort) and replaced. Idempotent: safe to
// call whether or not a session exists.
func ensureDeviceSession(ctx context.Context, api ensureSessionAPI, platform, label string, startOpts mcppkg.StartSessionOptions) (ensureResult, error) {
	platform = strings.ToLower(strings.TrimSpace(platform))
	label = strings.TrimSpace(label)

	var candidate *mcppkg.DeviceSession
	for _, s := range api.ListSessions() {
		if label != "" {
			if strings.EqualFold(s.Label, label) {
				if platform != "" && s.Platform != platform {
					return ensureResult{}, fmt.Errorf("session labeled %q is %s, not %s; use a different label or platform", label, s.Platform, platform)
				}
				candidate = s
			}
			if candidate != nil {
				break
			}
			continue
		}
		if platform == "" || s.Platform == platform {
			candidate = s
			break
		}
	}

	if candidate != nil {
		if alive, _ := api.CheckSessionAlive(ctx, candidate); alive {
			return ensureResult{
				Index:     candidate.Index,
				Platform:  candidate.Platform,
				Label:     candidate.Label,
				SessionID: candidate.SessionID,
				ViewerURL: candidate.ViewerURL,
				Reused:    true,
			}, nil
		}
		// Dead but still in local state: clear it out and provision fresh.
		_ = api.StopSession(ctx, candidate.Index)
	}

	if platform == "" {
		platform = "ios"
	}
	startOpts.Platform = platform
	startOpts.Label = label
	_, session, err := api.StartSession(ctx, startOpts)
	if err != nil {
		return ensureResult{}, err
	}
	return ensureResult{
		Index:     session.Index,
		Platform:  session.Platform,
		Label:     session.Label,
		SessionID: session.SessionID,
		ViewerURL: session.ViewerURL,
		Reused:    false,
	}, nil
}

var deviceEnsureCmd = &cobra.Command{
	Use:   "ensure",
	Short: "Get a live session matching a label/platform, reusing or starting as needed",
	Long: `Idempotent session acquisition: returns a live session matching the given
--label and/or --platform. Reuses a matching healthy session when one exists;
a matching but dead session is replaced; otherwise a new session is started.

Designed for agents: instead of check-then-start (which races with idle
timeouts), call ensure and act on the result.`,
	Example: `  revyl device ensure --platform ios --label checkout
  revyl device ensure --platform android --json
  revyl device ensure --label smoke --timeout 600 --json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr, err := getDeviceSessionMgr(cmd)
		if err != nil {
			return err
		}
		platform, _ := cmd.Flags().GetString("platform")
		label, _ := cmd.Flags().GetString("label")
		timeout, _ := cmd.Flags().GetInt("timeout")
		jsonOutput, _ := cmd.Flags().GetBool("json")

		if label != "" {
			if err := mcppkg.ValidateSessionLabel(label); err != nil {
				return err
			}
		}

		startOpts := mcppkg.StartSessionOptions{
			IdleTimeout: time.Duration(timeout) * time.Second,
		}
		result, err := ensureDeviceSession(cmd.Context(), mgr, platform, label, startOpts)
		if err != nil {
			return err
		}

		if jsonOutput {
			enc := json.NewEncoder(cmd.OutOrStdout())
			return enc.Encode(result)
		}
		if result.Reused {
			ui.PrintSuccess("Reusing session %d (%s%s)", result.Index, result.Platform, labelSuffix(result.Label))
		} else {
			ui.PrintSuccess("Started session %d (%s%s)", result.Index, result.Platform, labelSuffix(result.Label))
		}
		if result.ViewerURL != "" {
			ui.PrintLink("Live View", result.ViewerURL)
		}
		return nil
	},
}

func labelSuffix(label string) string {
	if label == "" {
		return ""
	}
	return fmt.Sprintf(" %q", label)
}

func init() {
	deviceEnsureCmd.Flags().String("platform", "", "Platform to match/start: ios or android (default ios when starting)")
	deviceEnsureCmd.Flags().String("label", "", "Label to match; assigned to the session if one is started")
	deviceEnsureCmd.Flags().Int("timeout", 300, "Idle timeout in seconds when starting a new session")
	deviceEnsureCmd.Flags().Bool("json", false, "Output as JSON")
	deviceCmd.AddCommand(deviceEnsureCmd)
}
