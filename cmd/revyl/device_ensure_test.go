package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	mcppkg "github.com/revyl/cli/internal/mcp"
)

type fakeEnsureAPI struct {
	sessions []*mcppkg.DeviceSession
	dead     map[int]bool
	stopped  []int
	started  []mcppkg.StartSessionOptions
	startErr error
	nextIdx  int
}

func (f *fakeEnsureAPI) ListSessions() []*mcppkg.DeviceSession { return f.sessions }

func (f *fakeEnsureAPI) CheckSessionAlive(ctx context.Context, s *mcppkg.DeviceSession) (bool, string) {
	if f.dead[s.Index] {
		return false, "worker unreachable"
	}
	return true, ""
}

func (f *fakeEnsureAPI) StopSession(ctx context.Context, index int) error {
	f.stopped = append(f.stopped, index)
	return nil
}

func (f *fakeEnsureAPI) StartSession(ctx context.Context, opts mcppkg.StartSessionOptions) (int, *mcppkg.DeviceSession, error) {
	f.started = append(f.started, opts)
	if f.startErr != nil {
		return -1, nil, f.startErr
	}
	idx := f.nextIdx
	f.nextIdx++
	return idx, &mcppkg.DeviceSession{
		Index: idx, Platform: opts.Platform, Label: opts.Label,
		SessionID: fmt.Sprintf("new-%d", idx), ViewerURL: "https://viewer/new",
	}, nil
}

func TestEnsureDeviceSession_ReusesHealthyLabelMatch(t *testing.T) {
	api := &fakeEnsureAPI{
		sessions: []*mcppkg.DeviceSession{
			{Index: 0, Platform: "ios", Label: "other", SessionID: "s0"},
			{Index: 1, Platform: "android", Label: "checkout", SessionID: "s1"},
		},
		dead:    map[int]bool{},
		nextIdx: 2,
	}
	res, err := ensureDeviceSession(context.Background(), api, "", "checkout", mcppkg.StartSessionOptions{})
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if !res.Reused || res.Index != 1 || res.SessionID != "s1" {
		t.Errorf("expected reuse of session 1, got %+v", res)
	}
	if len(api.started) != 0 {
		t.Errorf("should not have started a session: %+v", api.started)
	}
}

func TestEnsureDeviceSession_ReplacesDeadMatch(t *testing.T) {
	api := &fakeEnsureAPI{
		sessions: []*mcppkg.DeviceSession{
			{Index: 0, Platform: "ios", Label: "checkout", SessionID: "s0"},
		},
		dead:    map[int]bool{0: true},
		nextIdx: 1,
	}
	res, err := ensureDeviceSession(context.Background(), api, "ios", "checkout", mcppkg.StartSessionOptions{})
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if res.Reused {
		t.Errorf("dead session must not be reused: %+v", res)
	}
	if len(api.stopped) != 1 || api.stopped[0] != 0 {
		t.Errorf("dead session should be stopped, got %v", api.stopped)
	}
	if len(api.started) != 1 || api.started[0].Label != "checkout" || api.started[0].Platform != "ios" {
		t.Errorf("replacement start wrong: %+v", api.started)
	}
}

func TestEnsureDeviceSession_StartsWhenNoneExists(t *testing.T) {
	api := &fakeEnsureAPI{dead: map[int]bool{}}
	res, err := ensureDeviceSession(context.Background(), api, "", "smoke", mcppkg.StartSessionOptions{})
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if res.Reused {
		t.Errorf("nothing to reuse, got %+v", res)
	}
	// Empty platform defaults to ios when starting.
	if len(api.started) != 1 || api.started[0].Platform != "ios" || api.started[0].Label != "smoke" {
		t.Errorf("unexpected start opts: %+v", api.started)
	}
}

func TestEnsureDeviceSession_PlatformFilterAndMismatch(t *testing.T) {
	api := &fakeEnsureAPI{
		sessions: []*mcppkg.DeviceSession{
			{Index: 0, Platform: "ios", SessionID: "s0"},
			{Index: 1, Platform: "android", SessionID: "s1"},
		},
		dead:    map[int]bool{},
		nextIdx: 2,
	}
	// No label: first session matching the platform is reused.
	res, err := ensureDeviceSession(context.Background(), api, "android", "", mcppkg.StartSessionOptions{})
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if !res.Reused || res.Index != 1 {
		t.Errorf("expected android session 1, got %+v", res)
	}

	// Label matching a session on a different platform is an error, not a
	// silent wrong-platform reuse or a duplicate-label start.
	api.sessions[0].Label = "checkout"
	_, err = ensureDeviceSession(context.Background(), api, "android", "checkout", mcppkg.StartSessionOptions{})
	if err == nil || !strings.Contains(err.Error(), "is ios, not android") {
		t.Errorf("expected platform mismatch error, got %v", err)
	}
}

func TestEnsureDeviceSession_StartFailurePropagates(t *testing.T) {
	api := &fakeEnsureAPI{dead: map[int]bool{}, startErr: fmt.Errorf("no capacity")}
	_, err := ensureDeviceSession(context.Background(), api, "ios", "", mcppkg.StartSessionOptions{})
	if err == nil || !strings.Contains(err.Error(), "no capacity") {
		t.Errorf("expected start error, got %v", err)
	}
}
