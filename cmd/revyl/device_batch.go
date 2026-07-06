package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	mcppkg "github.com/revyl/cli/internal/mcp"
)

// batchSessionAPI is the subset of DeviceSessionManager needed to execute
// batch steps. Narrowed to an interface so tests can fake the worker side.
type batchSessionAPI interface {
	ResolveSessionRef(ref string) (*mcppkg.DeviceSession, error)
	ResolveTargetForSession(ctx context.Context, index int, target string) (*mcppkg.ResolvedTarget, error)
	WorkerRequestForSession(ctx context.Context, index int, path string, body interface{}) ([]byte, error)
	ScreenshotForSession(ctx context.Context, index int) ([]byte, error)
}

// batchSessionRef is a step's "s" field: a session index (JSON number) or a
// label / "active" (JSON string). set distinguishes absent from provided.
type batchSessionRef struct {
	set bool
	ref string
}

func (r *batchSessionRef) UnmarshalJSON(data []byte) error {
	var asString string
	if err := json.Unmarshal(data, &asString); err == nil {
		r.set = true
		r.ref = strings.TrimSpace(asString)
		return nil
	}
	var asInt int
	if err := json.Unmarshal(data, &asInt); err == nil {
		r.set = true
		r.ref = strconv.Itoa(asInt)
		return nil
	}
	return fmt.Errorf(`"s" must be a session index (number) or label (string)`)
}

// batchStep is one action in a batch. Pointer fields distinguish "not
// provided" from zero values (coordinates and drag endpoints can be 0).
type batchStep struct {
	Action     string          `json:"action"`
	Session    batchSessionRef `json:"s,omitempty"`
	Target     string   `json:"target,omitempty"`
	X          *int     `json:"x,omitempty"`
	Y          *int     `json:"y,omitempty"`
	Text       string   `json:"text,omitempty"`
	ClearFirst bool     `json:"clear_first,omitempty"`
	Direction  string   `json:"direction,omitempty"`
	DurationMs int      `json:"duration_ms,omitempty"`
	Scale      *float64 `json:"scale,omitempty"`
	StartX     *int     `json:"start_x,omitempty"`
	StartY     *int     `json:"start_y,omitempty"`
	EndX       *int     `json:"end_x,omitempty"`
	EndY       *int     `json:"end_y,omitempty"`
	Key        string   `json:"key,omitempty"`
	BundleID   string   `json:"bundle_id,omitempty"`
	App        string   `json:"app,omitempty"`
	Out        string   `json:"out,omitempty"`
}

// batchStepResult is the compact per-step output line (JSONL).
type batchStepResult struct {
	Index     int         `json:"i"`
	Action    string      `json:"action"`
	Session   int         `json:"s"`
	Label     string      `json:"label,omitempty"`
	OK        bool        `json:"ok"`
	X         *int        `json:"x,omitempty"`
	Y         *int        `json:"y,omitempty"`
	LatencyMs json.Number `json:"latency_ms,omitempty"`
	Path      string      `json:"path,omitempty"`
	Bytes     int         `json:"bytes,omitempty"`
	Error     string      `json:"error,omitempty"`
}

type batchSummary struct {
	Summary bool `json:"summary"`
	Total   int  `json:"total"`
	OK      int  `json:"ok"`
	Failed  int  `json:"failed"`
	Skipped int  `json:"skipped,omitempty"`
}

// batchCoordActions require --target or x/y (mirrors resolveTargetOrCoords).
var batchCoordActions = map[string]bool{
	"tap": true, "double_tap": true, "long_press": true, "type": true,
	"clear_text": true, "swipe": true, "pinch": true,
}

var batchBareActions = map[string]string{
	"back":     "/back",
	"home":     "/go_home",
	"shake":    "/shake",
	"kill_app": "/kill_app",
}

// normalizeBatchAction accepts CLI-style spellings (double-tap, long-press,
// clear-text, open-app, kill-app) alongside the canonical snake_case names.
func normalizeBatchAction(action string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(action)), "-", "_")
}

// parseBatchSteps accepts either a JSON array of steps or JSON Lines
// (one step object per line, blank lines ignored).
func parseBatchSteps(data []byte) ([]batchStep, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil, fmt.Errorf("no steps provided")
	}
	if strings.HasPrefix(trimmed, "[") {
		var steps []batchStep
		if err := json.Unmarshal([]byte(trimmed), &steps); err != nil {
			return nil, fmt.Errorf("invalid steps JSON array: %w", err)
		}
		return steps, nil
	}
	var steps []batchStep
	for lineNo, line := range strings.Split(trimmed, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var step batchStep
		if err := json.Unmarshal([]byte(line), &step); err != nil {
			return nil, fmt.Errorf("invalid step on line %d: %w", lineNo+1, err)
		}
		steps = append(steps, step)
	}
	if len(steps) == 0 {
		return nil, fmt.Errorf("no steps provided")
	}
	return steps, nil
}

// validateBatchStep enforces the same argument rules as the individual
// device subcommands so failures surface before any worker call.
func validateBatchStep(step batchStep) error {
	action := normalizeBatchAction(step.Action)
	hasCoords := step.X != nil && step.Y != nil
	if (step.X != nil) != (step.Y != nil) {
		return fmt.Errorf("both x and y are required when using coordinates")
	}
	if batchCoordActions[action] {
		if step.Target != "" && hasCoords {
			return fmt.Errorf("provide target OR x/y, not both")
		}
		if step.Target == "" && !hasCoords {
			return fmt.Errorf("provide target (element description) or x/y (coordinates)")
		}
	}
	switch action {
	case "tap", "double_tap", "long_press", "clear_text":
	case "type":
		if step.Text == "" {
			return fmt.Errorf("text is required")
		}
	case "swipe":
		if step.Direction == "" {
			return fmt.Errorf("direction is required (up|down|left|right)")
		}
	case "pinch":
		if step.Scale == nil {
			return fmt.Errorf("scale is required")
		}
	case "drag":
		if step.StartX == nil || step.StartY == nil || step.EndX == nil || step.EndY == nil {
			return fmt.Errorf("start_x, start_y, end_x, end_y are all required")
		}
	case "key":
		if _, err := normalizeBatchKey(step.Key); err != nil {
			return err
		}
	case "wait":
		if step.DurationMs < 0 {
			return fmt.Errorf("duration_ms must be >= 0")
		}
	case "launch":
		if step.BundleID == "" {
			return fmt.Errorf("bundle_id is required")
		}
	case "open_app":
		if step.App == "" {
			return fmt.Errorf("app is required")
		}
	case "back", "home", "shake", "kill_app", "screenshot", "hierarchy":
	default:
		return fmt.Errorf("unknown action %q (supported: tap, double_tap, long_press, type, clear_text, swipe, drag, pinch, key, wait, back, home, shake, kill_app, launch, open_app, screenshot, hierarchy)", step.Action)
	}
	return nil
}

func normalizeBatchKey(rawKey string) (string, error) {
	normalized := strings.ToUpper(strings.TrimSpace(rawKey))
	switch normalized {
	case "RETURN":
		normalized = "ENTER"
	case "DELETE":
		normalized = "BACKSPACE"
	}
	if normalized != "ENTER" && normalized != "BACKSPACE" {
		return "", fmt.Errorf("key must be ENTER or BACKSPACE")
	}
	return normalized, nil
}

// executeBatchStep runs a single step against its session and returns the
// compact result. Never panics; all failures land in result.Error.
func executeBatchStep(ctx context.Context, api batchSessionAPI, stepIndex int, step batchStep, defaultRef string) batchStepResult {
	action := normalizeBatchAction(step.Action)
	result := batchStepResult{Index: stepIndex, Action: action, Session: -1}

	fail := func(err error) batchStepResult {
		result.OK = false
		result.Error = err.Error()
		return result
	}

	if err := validateBatchStep(step); err != nil {
		return fail(err)
	}

	sessionRef := defaultRef
	if step.Session.set {
		sessionRef = step.Session.ref
	}
	session, err := api.ResolveSessionRef(sessionRef)
	if err != nil {
		return fail(err)
	}
	result.Session = session.Index
	result.Label = session.Label

	// tap with a target goes through /tap_target (server-side ground + tap
	// in one round trip), matching the standalone tap command.
	if action == "tap" && step.Target != "" {
		respBody, err := api.WorkerRequestForSession(ctx, session.Index, "/tap_target", map[string]interface{}{
			"target":     step.Target,
			"session_id": session.SessionID,
		})
		if err != nil {
			return fail(err)
		}
		ar := buildActionResult("tap", 0, 0, step.Target, respBody)
		return applyActionResult(result, ar)
	}

	// Resolve target to coordinates for the remaining coordinate actions.
	var x, y int
	if batchCoordActions[action] {
		if step.Target != "" {
			resolved, err := api.ResolveTargetForSession(ctx, session.Index, step.Target)
			if err != nil {
				return fail(err)
			}
			x, y = resolved.X, resolved.Y
		} else {
			x, y = *step.X, *step.Y
		}
	}

	switch action {
	case "tap":
		return workerActionStep(ctx, api, result, session.Index, "/tap", map[string]interface{}{"x": x, "y": y}, action, x, y, step.Target)
	case "double_tap":
		return workerActionStep(ctx, api, result, session.Index, "/double_tap", withTarget(map[string]interface{}{"x": x, "y": y}, step.Target), action, x, y, step.Target)
	case "long_press":
		dur := step.DurationMs
		if dur == 0 {
			dur = 1500
		}
		return workerActionStep(ctx, api, result, session.Index, "/longpress", withTarget(map[string]interface{}{"x": x, "y": y, "duration_ms": dur}, step.Target), action, x, y, step.Target)
	case "type":
		return workerActionStep(ctx, api, result, session.Index, "/input", withTarget(map[string]interface{}{"x": x, "y": y, "text": step.Text, "clear_first": step.ClearFirst}, step.Target), action, x, y, step.Target)
	case "clear_text":
		return workerActionStep(ctx, api, result, session.Index, "/clear_text", map[string]interface{}{"x": x, "y": y}, action, x, y, "")
	case "swipe":
		dur := step.DurationMs
		if dur == 0 {
			dur = 500
		}
		return workerActionStep(ctx, api, result, session.Index, "/swipe", withTarget(map[string]interface{}{"x": x, "y": y, "direction": step.Direction, "duration_ms": dur}, step.Target), action, x, y, step.Target)
	case "pinch":
		dur := step.DurationMs
		if dur <= 0 {
			dur = 300
		}
		return workerActionStep(ctx, api, result, session.Index, "/pinch", map[string]interface{}{"x": x, "y": y, "scale": *step.Scale, "duration_ms": dur}, action, x, y, "")
	case "drag":
		return workerActionStep(ctx, api, result, session.Index, "/drag", map[string]interface{}{
			"start_x": *step.StartX, "start_y": *step.StartY, "end_x": *step.EndX, "end_y": *step.EndY,
		}, action, *step.StartX, *step.StartY, "")
	case "key":
		key, _ := normalizeBatchKey(step.Key)
		return workerActionStep(ctx, api, result, session.Index, "/key", map[string]string{"key": key}, action, 0, 0, "")
	case "wait":
		if _, err := api.WorkerRequestForSession(ctx, session.Index, "/wait", map[string]int{"duration_ms": step.DurationMs}); err != nil {
			return fail(err)
		}
		result.OK = true
		return result
	case "back", "home", "shake", "kill_app":
		if _, err := api.WorkerRequestForSession(ctx, session.Index, batchBareActions[action], nil); err != nil {
			return fail(err)
		}
		result.OK = true
		return result
	case "launch":
		if _, err := api.WorkerRequestForSession(ctx, session.Index, "/launch", map[string]string{"bundle_id": step.BundleID}); err != nil {
			return fail(err)
		}
		result.OK = true
		return result
	case "open_app":
		bundleID := mcppkg.ResolveSystemApp(session.Platform, step.App)
		if _, err := api.WorkerRequestForSession(ctx, session.Index, "/launch", map[string]string{"bundle_id": bundleID}); err != nil {
			return fail(err)
		}
		result.OK = true
		return result
	case "screenshot":
		imgBytes, err := api.ScreenshotForSession(ctx, session.Index)
		if err != nil {
			return fail(err)
		}
		out := step.Out
		if out == "" {
			out = fmt.Sprintf("revyl-batch-%d-s%d.png", stepIndex, session.Index)
		}
		if err := os.WriteFile(out, imgBytes, 0o644); err != nil {
			return fail(err)
		}
		result.OK = true
		result.Path = out
		result.Bytes = len(imgBytes)
		return result
	case "hierarchy":
		respBytes, err := api.WorkerRequestForSession(ctx, session.Index, "/hierarchy", nil)
		if err != nil {
			return fail(err)
		}
		out := step.Out
		if out == "" {
			out = fmt.Sprintf("revyl-batch-%d-s%d-hierarchy.txt", stepIndex, session.Index)
		}
		if err := os.WriteFile(out, respBytes, 0o644); err != nil {
			return fail(err)
		}
		result.OK = true
		result.Path = out
		result.Bytes = len(respBytes)
		return result
	}
	return fail(fmt.Errorf("unknown action %q", step.Action))
}

func withTarget(body map[string]interface{}, target string) map[string]interface{} {
	if target != "" {
		body["target"] = target
	}
	return body
}

// workerActionStep posts an action to the worker and folds the response into
// the compact batch result via buildActionResult.
func workerActionStep(ctx context.Context, api batchSessionAPI, result batchStepResult, sessionIdx int, path string, body interface{}, action string, x, y int, target string) batchStepResult {
	respBody, err := api.WorkerRequestForSession(ctx, sessionIdx, path, body)
	if err != nil {
		result.OK = false
		result.Error = err.Error()
		return result
	}
	return applyActionResult(result, buildActionResult(action, x, y, target, respBody))
}

func applyActionResult(result batchStepResult, ar ActionResult) batchStepResult {
	result.OK = ar.Success
	result.Error = ar.Error
	if !ar.Success && result.Error == "" {
		result.Error = fmt.Sprintf("%s failed", ar.Action)
	}
	x, y := ar.X, ar.Y
	result.X = &x
	result.Y = &y
	result.LatencyMs = ar.LatencyMs
	return result
}

// checkBatchSessionAmbiguity refuses implicit active-session routing when
// several sessions are live: with no -s default, every step must carry its
// own "s". Failing upfront means no step mutates device state before the
// mistake surfaces.
func checkBatchSessionAmbiguity(steps []batchStep, defaultRef string, sessions []*mcppkg.DeviceSession) error {
	if len(sessions) <= 1 || strings.TrimSpace(defaultRef) != "" {
		return nil
	}
	var implicit []int
	for i, step := range steps {
		if !step.Session.set {
			implicit = append(implicit, i)
		}
	}
	if len(implicit) == 0 {
		return nil
	}
	return fmt.Errorf(
		"%d sessions active (%s) and step(s) %v have no \"s\" — add \"s\": <index|label> to each step or pass -s <index|label> as the default",
		len(sessions), sessionRoster(sessions), implicit,
	)
}

// runDeviceBatch executes steps sequentially, streaming one compact JSON
// line per step to w, and returns the summary.
func runDeviceBatch(ctx context.Context, api batchSessionAPI, steps []batchStep, defaultRef string, continueOnError bool, w io.Writer) batchSummary {
	summary := batchSummary{Summary: true, Total: len(steps)}
	enc := json.NewEncoder(w)
	for i, step := range steps {
		result := executeBatchStep(ctx, api, i, step, defaultRef)
		_ = enc.Encode(result)
		if result.OK {
			summary.OK++
			continue
		}
		summary.Failed++
		if !continueOnError {
			summary.Skipped = len(steps) - i - 1
			break
		}
	}
	_ = enc.Encode(summary)
	return summary
}

var deviceBatchCmd = &cobra.Command{
	Use:   "batch",
	Short: "Run multiple device actions in one invocation (JSON steps, compact JSONL output)",
	Long: `Run a sequence of device actions in a single CLI call.

Steps are JSON objects with an "action" field, provided as a JSON array or
JSON Lines via --steps, --file, or stdin. Each step may set "s" to a session
index or label, so one batch can drive multiple sessions. When more than one
session is active, every step must address its session explicitly (per-step
"s" or the -s default) — implicit active-session routing is refused.

Supported actions: tap, double_tap, long_press, type, clear_text, swipe,
drag, pinch, key, wait, back, home, shake, kill_app, launch, open_app,
screenshot, hierarchy.

Output is one compact JSON line per step plus a final summary line, designed
to keep coding-agent token usage low.`,
	Example: `  revyl device batch --steps '[{"action":"tap","target":"Sign In"},{"action":"type","target":"email","text":"a@b.co"},{"action":"screenshot","out":"after.png"}]'
  echo '{"action":"tap","x":200,"y":400}
{"action":"key","key":"ENTER"}' | revyl device batch
  revyl device batch --file steps.json --continue-on-error
  revyl device batch --steps '[{"action":"screenshot","s":"checkout-ios"},{"action":"screenshot","s":1}]'`,
	RunE: func(cmd *cobra.Command, args []string) error {
		inline, _ := cmd.Flags().GetString("steps")
		file, _ := cmd.Flags().GetString("file")
		if inline != "" && file != "" {
			return fmt.Errorf("provide --steps or --file, not both")
		}

		var data []byte
		var err error
		switch {
		case inline != "":
			data = []byte(inline)
		case file != "" && file != "-":
			data, err = os.ReadFile(file)
			if err != nil {
				return err
			}
		default:
			data, err = io.ReadAll(cmd.InOrStdin())
			if err != nil {
				return err
			}
		}

		steps, err := parseBatchSteps(data)
		if err != nil {
			return err
		}

		mgr, err := getDeviceSessionMgr(cmd)
		if err != nil {
			return err
		}
		defaultRef, _ := cmd.Flags().GetString("s")
		continueOnError, _ := cmd.Flags().GetBool("continue-on-error")

		if err := checkBatchSessionAmbiguity(steps, defaultRef, mgr.ListSessions()); err != nil {
			return err
		}

		summary := runDeviceBatch(cmd.Context(), mgr, steps, defaultRef, continueOnError, cmd.OutOrStdout())
		if summary.Failed > 0 {
			return fmt.Errorf("%d/%d steps failed", summary.Failed, summary.Total)
		}
		return nil
	},
}

func init() {
	deviceBatchCmd.Flags().String("steps", "", "Inline steps as a JSON array or JSON Lines")
	deviceBatchCmd.Flags().String("file", "", "Read steps from a file ('-' for stdin)")
	deviceBatchCmd.Flags().Bool("continue-on-error", false, "Continue executing remaining steps after a failure")
	deviceBatchCmd.Flags().StringP("s", "s", "", "Default session (index or label) for steps without an explicit \"s\"")
	deviceCmd.AddCommand(deviceBatchCmd)
}
