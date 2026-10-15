package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/h0ek/gotify-vps-agent/internal/config"
	"github.com/h0ek/gotify-vps-agent/internal/engine"
	"github.com/h0ek/gotify-vps-agent/internal/gotify"
	"github.com/h0ek/gotify-vps-agent/internal/model"
	"github.com/h0ek/gotify-vps-agent/internal/queue"
	"github.com/h0ek/gotify-vps-agent/internal/state"
)

func emergencyConfig(t *testing.T, url string) config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.Agent.Hostname = "test-host"
	cfg.Gotify.URL = url
	cfg.Gotify.Timeout = "1s"
	cfg.Gotify.TokenFile = filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(cfg.Gotify.TokenFile, []byte("test-token"), 0600); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestPersistenceFailuresSendOneEmergencyWithoutAcknowledgingQueue(t *testing.T) {
	for _, cause := range []error{syscall.ENOSPC, syscall.EROFS} {
		for failedStage := 0; failedStage < 3; failedStage++ {
			t.Run(fmt.Sprintf("%v-stage-%d", cause, failedStage), func(t *testing.T) {
				requests := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests++
					var body struct {
						Title    string
						Message  string
						Priority int
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if !strings.Contains(body.Message, "Read-only filesystems") || !strings.Contains(body.Message, "not recorded") || body.Priority != 10 || len(body.Message) > gotify.MaxMessageBytes {
						t.Errorf("incomplete emergency notification: %#v", body)
					}
					_, _ = io.WriteString(w, `{"id":1}`)
				}))
				defer server.Close()
				cfg := emergencyConfig(t, server.URL)
				dir := t.TempDir()
				pending := queue.New()
				pending.Add([]model.Event{{CheckID: "filesystem.readonly", Status: model.StatusCritical, Kind: model.EventProblem, Fingerprint: "disk-ro", Message: "/srv is read-only"}}, time.Now())
				queuePath := filepath.Join(dir, "queue.json")
				if err := queue.Save(queuePath, pending); err != nil {
					t.Fatal(err)
				}
				calls := 0
				step := func() error {
					index := calls
					calls++
					if index == failedStage {
						return &os.PathError{Op: "write", Path: "runtime-file", Err: cause}
					}
					return nil
				}
				failure := persistCheck(state.New(), pending, "s=cursor", persistence{
					Queue: func(value queue.Queue) error {
						if err := step(); err != nil {
							return err
						}
						return queue.Save(queuePath, value)
					},
					State: func(value state.State) error {
						if err := step(); err != nil {
							return err
						}
						return state.Save(filepath.Join(dir, "state.json"), value)
					},
					Cursor: func(string) error { return step() },
				})
				var writeFailure *persistenceError
				if !errors.As(failure, &writeFailure) || !errors.Is(failure, cause) {
					t.Fatalf("storage failure was lost: %v", failure)
				}
				var output bytes.Buffer
				err := reportStorageFailure(context.Background(), cfg, []model.Result{{ID: "filesystem.readonly", Title: "Read-only filesystems", Status: model.StatusCritical, Message: "/srv"}}, writeFailure, &output)
				if requests != 1 || !errors.Is(err, cause) || !strings.Contains(output.String(), "Emergency notification delivered") || calls != failedStage+1 {
					t.Fatalf("wrong emergency handling: requests=%d calls=%d err=%v output=%s", requests, calls, err, output.String())
				}
				retained, err := queue.Load(queuePath)
				if err != nil || len(retained.Events) != 1 || retained.Events[0].Fingerprint != "disk-ro" {
					t.Fatalf("emergency cleared durable queue: %#v %v", retained, err)
				}
			})
		}
	}
}

func TestEmergencyDeliveryFailureKeepsOriginalStorageError(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; w.WriteHeader(http.StatusServiceUnavailable) }))
	defer server.Close()
	err := reportStorageFailure(context.Background(), emergencyConfig(t, server.URL), nil, &persistenceError{Stage: "queue", Err: syscall.ENOSPC}, io.Discard)
	if requests != 1 || !errors.Is(err, syscall.ENOSPC) || !strings.Contains(err.Error(), "emergency notification failed") {
		t.Fatalf("wrong emergency failure: requests=%d err=%v", requests, err)
	}
}

func TestEmergencyDoesNotBypassFailedSOCKSProxy(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; _, _ = io.WriteString(w, `{"id":1}`) }))
	defer server.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	proxy := "socks5h://" + listener.Addr().String()
	_ = listener.Close()
	cfg := emergencyConfig(t, server.URL)
	cfg.Gotify.ProxyURL = proxy
	err = reportStorageFailure(context.Background(), cfg, nil, &persistenceError{Stage: "queue", Err: syscall.EROFS}, io.Discard)
	if requests != 0 || !errors.Is(err, syscall.EROFS) || !strings.Contains(err.Error(), "emergency notification failed") {
		t.Fatalf("emergency bypassed proxy: requests=%d err=%v", requests, err)
	}
}

func TestEmergencyHandlesRealNoSpaceError(t *testing.T) {
	file, err := os.OpenFile("/dev/full", os.O_WRONLY, 0)
	if err != nil {
		t.Skip(err)
	}
	defer file.Close()
	_, err = file.Write([]byte("test"))
	if !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("expected ENOSPC from /dev/full: %v", err)
	}
	calls := 0
	err = sendStorageFailure(context.Background(), "host", nil, &persistenceError{Stage: "queue", Err: err}, func(context.Context, string, string, int) error { calls++; return nil }, io.Discard)
	if calls != 1 || !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("real ENOSPC did not attempt emergency: %v", err)
	}
}

func TestExistingLockUsesReadOnlyDescriptor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	if err := os.WriteFile(path, nil, 0400); err != nil {
		t.Fatal(err)
	}
	lock, err := acquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, lock.file.Fd(), syscall.F_GETFL, 0)
	if errno != 0 || flags&syscall.O_ACCMODE != syscall.O_RDONLY {
		t.Fatalf("lock requires write access: flags=%d error=%v", flags, errno)
	}
}

func TestCommittedQueueRestoresUncommittedIncidentState(t *testing.T) {
	cfg := config.Default()
	now := time.Now().UTC()
	current := state.New()
	pending := queue.New()
	pending.Add([]model.Event{{CheckID: "service.nginx", Status: model.StatusCritical, Kind: model.EventProblem, Fingerprint: "committed-before-state-failure", Message: "nginx.service is inactive"}}, now)
	restorePendingState(&current, pending)
	healthy := model.Result{ID: "service.nginx", Status: model.StatusOK, Message: "nginx.service is active"}
	if events := engine.Evaluate(&current, []model.Result{healthy}, cfg, now.Add(5*time.Minute)); len(events) != 0 {
		t.Fatalf("recovery was not confirmed: %#v", events)
	}
	restorePendingState(&current, pending)
	events := engine.Evaluate(&current, []model.Result{healthy}, cfg, now.Add(10*time.Minute))
	if len(events) != 1 || events[0].Kind != model.EventRecovery {
		t.Fatalf("uncommitted state lost incident recovery: %#v", events)
	}
	pending.Add(events, now.Add(10*time.Minute))
	stale := state.New()
	stale.Checks["service.nginx"] = state.CheckState{Current: model.StatusCritical, LastObserved: model.StatusCritical, Notified: true}
	restorePendingState(&stale, pending)
	if events := engine.Evaluate(&stale, []model.Result{healthy}, cfg, now.Add(15*time.Minute)); len(events) != 0 {
		t.Fatalf("committed recovery was repeated: %#v", events)
	}
	if len(pending.Events) != 2 {
		t.Fatalf("recovery lost pending problem: %#v", pending.Events)
	}
}
