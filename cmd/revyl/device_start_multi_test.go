package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mcppkg "github.com/revyl/cli/internal/mcp"
)

type fakeMultiStarter struct {
	mu       sync.Mutex
	nextIdx  int
	calls    []string
	failFor  map[string]error
	inFlight atomic.Int32
	maxSeen  atomic.Int32
	delay    time.Duration
}

func (f *fakeMultiStarter) StartSession(ctx context.Context, opts mcppkg.StartSessionOptions) (int, *mcppkg.DeviceSession, error) {
	cur := f.inFlight.Add(1)
	defer f.inFlight.Add(-1)
	for {
		prev := f.maxSeen.Load()
		if cur <= prev || f.maxSeen.CompareAndSwap(prev, cur) {
			break
		}
	}
	if f.delay > 0 {
		time.Sleep(f.delay)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, opts.Platform)
	if err, ok := f.failFor[opts.Platform]; ok {
		return -1, nil, err
	}
	idx := f.nextIdx
	f.nextIdx++
	return idx, &mcppkg.DeviceSession{
		Index:     idx,
		Platform:  opts.Platform,
		Label:     opts.Label,
		SessionID: fmt.Sprintf("sess-%d", idx),
		ViewerURL: fmt.Sprintf("https://app.revyl.ai/sessions/sess-%d", idx),
	}, nil
}

func TestRunMultiDeviceStart_ParallelAndJSON(t *testing.T) {
	starter := &fakeMultiStarter{delay: 50 * time.Millisecond}
	var out strings.Builder

	err := runMultiDeviceStart(context.Background(), starter, []string{"ios", "android"}, 2, nil, mcppkg.StartSessionOptions{}, true, &out)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var results []multiStartResult
	if err := json.Unmarshal([]byte(out.String()), &results); err != nil {
		t.Fatalf("output not a JSON array: %v\n%s", err, out.String())
	}
	if len(results) != 4 {
		t.Fatalf("expected 4 results, got %d", len(results))
	}
	platforms := map[string]int{}
	for _, r := range results {
		if r.Error != "" {
			t.Errorf("unexpected failure: %+v", r)
		}
		if r.SessionID == "" {
			t.Errorf("missing session id: %+v", r)
		}
		platforms[r.Platform]++
	}
	if platforms["ios"] != 2 || platforms["android"] != 2 {
		t.Errorf("wrong platform distribution: %v", platforms)
	}
	if starter.maxSeen.Load() < 2 {
		t.Errorf("expected concurrent starts, max in-flight was %d", starter.maxSeen.Load())
	}
}

func TestRunMultiDeviceStart_PartialFailure(t *testing.T) {
	starter := &fakeMultiStarter{failFor: map[string]error{"android": fmt.Errorf("no capacity")}}
	var out strings.Builder

	err := runMultiDeviceStart(context.Background(), starter, []string{"ios", "android"}, 1, nil, mcppkg.StartSessionOptions{}, true, &out)
	if err == nil || !strings.Contains(err.Error(), "1/2 sessions failed") {
		t.Fatalf("expected partial failure error, got %v", err)
	}

	var results []multiStartResult
	if err := json.Unmarshal([]byte(out.String()), &results); err != nil {
		t.Fatalf("output not a JSON array: %v", err)
	}
	var iosOK, androidErr bool
	for _, r := range results {
		if r.Platform == "ios" && r.Error == "" && r.SessionID != "" {
			iosOK = true
		}
		if r.Platform == "android" && strings.Contains(r.Error, "no capacity") && r.Index == -1 {
			androidErr = true
		}
	}
	if !iosOK || !androidErr {
		t.Errorf("unexpected results: %+v", results)
	}
}

func TestRunMultiDeviceStart_LabelsAssignedInOrder(t *testing.T) {
	starter := &fakeMultiStarter{}
	var out strings.Builder

	labels := []string{"ios-a", "ios-b", "droid-a", "droid-b"}
	err := runMultiDeviceStart(context.Background(), starter, []string{"ios", "android"}, 2, labels, mcppkg.StartSessionOptions{}, true, &out)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var results []multiStartResult
	if err := json.Unmarshal([]byte(out.String()), &results); err != nil {
		t.Fatalf("output not a JSON array: %v", err)
	}
	got := map[string]string{}
	for _, r := range results {
		got[r.Label] = r.Platform
	}
	if got["ios-a"] != "ios" || got["ios-b"] != "ios" || got["droid-a"] != "android" || got["droid-b"] != "android" {
		t.Errorf("labels not assigned in spec order: %+v", results)
	}
}

func TestParseDeviceStartLabels(t *testing.T) {
	existing := []*mcppkg.DeviceSession{{Index: 0, Platform: "ios", Label: "taken"}}

	cases := []struct {
		name    string
		flag    string
		total   int
		wantErr string
		want    []string
	}{
		{"empty means none", "", 3, "", nil},
		{"single ok", "checkout", 1, "", []string{"checkout"}},
		{"multi ok with spaces", "a-1, b-2", 2, "", []string{"a-1", "b-2"}},
		{"count mismatch", "only-one", 2, "1 label(s) but 2 session(s)", nil},
		{"numeric rejected", "42", 1, "must not be a number", nil},
		{"reserved rejected", "active", 1, "reserved", nil},
		{"bad charset", "has space", 1, "may only contain", nil},
		{"duplicate in list", "same,SAME", 2, "duplicate label", nil},
		{"collides with existing", "Taken", 1, "already used by session 0", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseDeviceStartLabels(tc.flag, tc.total, existing)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}
