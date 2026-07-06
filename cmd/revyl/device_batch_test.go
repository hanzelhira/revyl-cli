package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mcppkg "github.com/revyl/cli/internal/mcp"
)

// fakeBatchAPI records worker calls and returns canned responses per path.
type fakeBatchAPI struct {
	sessions   map[int]*mcppkg.DeviceSession
	active     int
	calls      []fakeBatchCall
	responses  map[string][]byte
	failPaths  map[string]error
	resolveXY  *mcppkg.ResolvedTarget
	resolveErr error
	screenshot []byte
}

type fakeBatchCall struct {
	Session int
	Path    string
	Body    interface{}
}

func (f *fakeBatchAPI) ResolveSession(index int) (*mcppkg.DeviceSession, error) {
	if index == -1 {
		index = f.active
	}
	s, ok := f.sessions[index]
	if !ok {
		return nil, fmt.Errorf("no session at index %d", index)
	}
	return s, nil
}

func (f *fakeBatchAPI) ResolveTargetForSession(ctx context.Context, index int, target string) (*mcppkg.ResolvedTarget, error) {
	if f.resolveErr != nil {
		return nil, f.resolveErr
	}
	if f.resolveXY != nil {
		return f.resolveXY, nil
	}
	return &mcppkg.ResolvedTarget{X: 10, Y: 20}, nil
}

func (f *fakeBatchAPI) WorkerRequestForSession(ctx context.Context, index int, path string, body interface{}) ([]byte, error) {
	f.calls = append(f.calls, fakeBatchCall{Session: index, Path: path, Body: body})
	if err, ok := f.failPaths[path]; ok {
		return nil, err
	}
	if resp, ok := f.responses[path]; ok {
		return resp, nil
	}
	return []byte(`{"success": true, "latency_ms": 42}`), nil
}

func (f *fakeBatchAPI) ScreenshotForSession(ctx context.Context, index int) ([]byte, error) {
	f.calls = append(f.calls, fakeBatchCall{Session: index, Path: "/screenshot"})
	return f.screenshot, nil
}

func newFakeBatchAPI() *fakeBatchAPI {
	return &fakeBatchAPI{
		sessions: map[int]*mcppkg.DeviceSession{
			0: {Index: 0, SessionID: "sess-0", Platform: "ios"},
			1: {Index: 1, SessionID: "sess-1", Platform: "android"},
		},
		active:     0,
		responses:  map[string][]byte{},
		failPaths:  map[string]error{},
		screenshot: []byte("png-bytes"),
	}
}

func decodeBatchLines(t *testing.T, out string) ([]batchStepResult, batchSummary) {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 1 {
		t.Fatalf("no output lines")
	}
	var results []batchStepResult
	for _, line := range lines[:len(lines)-1] {
		var r batchStepResult
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("bad result line %q: %v", line, err)
		}
		results = append(results, r)
	}
	var summary batchSummary
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &summary); err != nil {
		t.Fatalf("bad summary line %q: %v", lines[len(lines)-1], err)
	}
	return results, summary
}

func TestParseBatchSteps_ArrayAndJSONL(t *testing.T) {
	array := `[{"action":"tap","target":"Sign In"},{"action":"key","key":"ENTER"}]`
	steps, err := parseBatchSteps([]byte(array))
	if err != nil {
		t.Fatalf("array parse: %v", err)
	}
	if len(steps) != 2 || steps[0].Action != "tap" || steps[1].Key != "ENTER" {
		t.Errorf("unexpected array steps: %+v", steps)
	}

	jsonl := "{\"action\":\"back\"}\n\n{\"action\":\"home\"}\n"
	steps, err = parseBatchSteps([]byte(jsonl))
	if err != nil {
		t.Fatalf("jsonl parse: %v", err)
	}
	if len(steps) != 2 || steps[0].Action != "back" || steps[1].Action != "home" {
		t.Errorf("unexpected jsonl steps: %+v", steps)
	}

	if _, err := parseBatchSteps([]byte("  \n ")); err == nil {
		t.Error("expected error for empty input")
	}
	if _, err := parseBatchSteps([]byte(`{"action":"tap" BROKEN`)); err == nil {
		t.Error("expected error for malformed line")
	}
}

func TestValidateBatchStep(t *testing.T) {
	intp := func(v int) *int { return &v }
	cases := []struct {
		name    string
		step    batchStep
		wantErr string
	}{
		{"tap target ok", batchStep{Action: "tap", Target: "Sign In"}, ""},
		{"tap coords ok", batchStep{Action: "tap", X: intp(0), Y: intp(0)}, ""},
		{"tap both", batchStep{Action: "tap", Target: "x", X: intp(1), Y: intp(2)}, "not both"},
		{"tap neither", batchStep{Action: "tap"}, "provide target"},
		{"tap x only", batchStep{Action: "tap", X: intp(1)}, "both x and y"},
		{"type no text", batchStep{Action: "type", Target: "field"}, "text is required"},
		{"swipe no direction", batchStep{Action: "swipe", Target: "list"}, "direction is required"},
		{"pinch no scale", batchStep{Action: "pinch", X: intp(1), Y: intp(2)}, "scale is required"},
		{"drag missing corner", batchStep{Action: "drag", StartX: intp(1), StartY: intp(2), EndX: intp(3)}, "all required"},
		{"key bad", batchStep{Action: "key", Key: "TAB"}, "ENTER or BACKSPACE"},
		{"key alias", batchStep{Action: "key", Key: "return"}, ""},
		{"launch no bundle", batchStep{Action: "launch"}, "bundle_id is required"},
		{"open_app no app", batchStep{Action: "open_app"}, "app is required"},
		{"dashed alias", batchStep{Action: "double-tap", X: intp(1), Y: intp(2)}, ""},
		{"unknown", batchStep{Action: "teleport"}, "unknown action"},
		{"bare ok", batchStep{Action: "home"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateBatchStep(tc.step)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestRunDeviceBatch_HappyPath(t *testing.T) {
	api := newFakeBatchAPI()
	tmp := t.TempDir()
	shot := filepath.Join(tmp, "after.png")

	steps := []batchStep{
		{Action: "tap", Target: "Sign In"},
		{Action: "type", Target: "email field", Text: "a@b.co"},
		{Action: "key", Key: "ENTER"},
		{Action: "screenshot", Out: shot},
	}

	var out strings.Builder
	summary := runDeviceBatch(context.Background(), api, steps, -1, false, &out)

	if summary.Failed != 0 || summary.OK != 4 || summary.Total != 4 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	results, _ := decodeBatchLines(t, out.String())
	if len(results) != 4 {
		t.Fatalf("expected 4 result lines, got %d", len(results))
	}
	for _, r := range results {
		if !r.OK {
			t.Errorf("step %d failed: %s", r.Index, r.Error)
		}
		if r.Session != 0 {
			t.Errorf("step %d ran on session %d, want 0", r.Index, r.Session)
		}
	}

	// tap with target must use the one-round-trip /tap_target endpoint.
	if api.calls[0].Path != "/tap_target" {
		t.Errorf("first call path = %s, want /tap_target", api.calls[0].Path)
	}
	// type with target resolves coordinates then posts /input.
	if api.calls[1].Path != "/input" {
		t.Errorf("second call path = %s, want /input", api.calls[1].Path)
	}
	body, ok := api.calls[1].Body.(map[string]interface{})
	if !ok || body["x"] != 10 || body["y"] != 20 || body["text"] != "a@b.co" {
		t.Errorf("unexpected /input body: %#v", api.calls[1].Body)
	}

	data, err := os.ReadFile(shot)
	if err != nil || string(data) != "png-bytes" {
		t.Errorf("screenshot not written: %v", err)
	}
}

func TestRunDeviceBatch_StopsOnFirstFailure(t *testing.T) {
	api := newFakeBatchAPI()
	api.failPaths["/tap"] = fmt.Errorf("worker down")

	steps := []batchStep{
		{Action: "back"},
		{Action: "tap", X: batchIntPtr(1), Y: batchIntPtr(2)},
		{Action: "home"},
	}
	var out strings.Builder
	summary := runDeviceBatch(context.Background(), api, steps, -1, false, &out)

	if summary.OK != 1 || summary.Failed != 1 || summary.Skipped != 1 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	results, _ := decodeBatchLines(t, out.String())
	if len(results) != 2 {
		t.Fatalf("expected 2 result lines (stopped early), got %d", len(results))
	}
	if results[1].OK || !strings.Contains(results[1].Error, "worker down") {
		t.Errorf("expected step 1 failure with worker error, got %+v", results[1])
	}
}

func TestRunDeviceBatch_ContinueOnError(t *testing.T) {
	api := newFakeBatchAPI()
	api.failPaths["/tap"] = fmt.Errorf("worker down")

	steps := []batchStep{
		{Action: "tap", X: batchIntPtr(1), Y: batchIntPtr(2)},
		{Action: "home"},
	}
	var out strings.Builder
	summary := runDeviceBatch(context.Background(), api, steps, -1, true, &out)

	if summary.OK != 1 || summary.Failed != 1 || summary.Skipped != 0 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
}

func TestRunDeviceBatch_PerStepSessionOverride(t *testing.T) {
	api := newFakeBatchAPI()
	s1 := 1
	steps := []batchStep{
		{Action: "back"},
		{Action: "back", Session: &s1},
	}
	var out strings.Builder
	runDeviceBatch(context.Background(), api, steps, -1, false, &out)

	results, summary := decodeBatchLines(t, out.String())
	if summary.Failed != 0 {
		t.Fatalf("unexpected failures: %+v", summary)
	}
	if results[0].Session != 0 || results[1].Session != 1 {
		t.Errorf("session routing wrong: %+v", results)
	}
	if api.calls[0].Session != 0 || api.calls[1].Session != 1 {
		t.Errorf("worker calls hit wrong sessions: %+v", api.calls)
	}
}

func TestRunDeviceBatch_WorkerReportsFailure(t *testing.T) {
	api := newFakeBatchAPI()
	api.responses["/tap_target"] = []byte(`{"success": false, "error": "element not found"}`)

	steps := []batchStep{{Action: "tap", Target: "Ghost Button"}}
	var out strings.Builder
	summary := runDeviceBatch(context.Background(), api, steps, -1, false, &out)

	if summary.Failed != 1 {
		t.Fatalf("expected failure, got %+v", summary)
	}
	results, _ := decodeBatchLines(t, out.String())
	if results[0].OK || results[0].Error != "element not found" {
		t.Errorf("unexpected result: %+v", results[0])
	}
}

func TestRunDeviceBatch_UnknownSession(t *testing.T) {
	api := newFakeBatchAPI()
	s9 := 9
	steps := []batchStep{{Action: "back", Session: &s9}}
	var out strings.Builder
	summary := runDeviceBatch(context.Background(), api, steps, -1, false, &out)
	if summary.Failed != 1 {
		t.Fatalf("expected failure for unknown session, got %+v", summary)
	}
}

func batchIntPtr(v int) *int { return &v }

func TestExecuteBatchStep_OpenAppResolvesSystemBundle(t *testing.T) {
	api := newFakeBatchAPI()
	result := executeBatchStep(context.Background(), api, 0, batchStep{Action: "open_app", App: "settings"}, -1)
	if !result.OK {
		t.Fatalf("open_app failed: %s", result.Error)
	}
	if api.calls[0].Path != "/launch" {
		t.Fatalf("expected /launch call, got %s", api.calls[0].Path)
	}
	body := api.calls[0].Body.(map[string]string)
	if body["bundle_id"] == "" || body["bundle_id"] == "settings" {
		t.Errorf("expected resolved system bundle id, got %q", body["bundle_id"])
	}
}

