package history

import (
	"errors"
	"testing"
	"time"
)

func TestRetryInitRetriesUntilSuccess(t *testing.T) {
	statusMu.Lock()
	status = Status{}
	statusMu.Unlock()

	calls := 0
	var sleeps []time.Duration
	retryInit(func() error {
		calls++
		if calls < 4 {
			return errors.New("mongo not ready")
		}
		return nil
	}, func(d time.Duration) { sleeps = append(sleeps, d) })

	if calls != 4 {
		t.Fatalf("connect called %d times, want 4", calls)
	}
	want := []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second}
	if len(sleeps) != len(want) {
		t.Fatalf("slept %v, want %v", sleeps, want)
	}
	for i := range want {
		if sleeps[i] != want[i] {
			t.Fatalf("sleeps = %v, want %v", sleeps, want)
		}
	}
	st := GetStatus()
	if st.Attempts != 4 || st.LastError != "" {
		t.Fatalf("status = %+v, want 4 attempts and no error", st)
	}
}

func TestRetryInitBackoffCaps(t *testing.T) {
	calls := 0
	var last time.Duration
	retryInit(func() error {
		calls++
		if calls < 10 {
			return errors.New("down")
		}
		return nil
	}, func(d time.Duration) { last = d })

	if last != initRetryMax {
		t.Fatalf("final backoff = %s, want cap %s", last, initRetryMax)
	}
}

func TestRetryInitRecordsLastError(t *testing.T) {
	calls := 0
	retryInit(func() error {
		calls++
		if calls == 1 {
			return errors.New("connection refused")
		}
		return nil
	}, func(time.Duration) {
		if got := GetStatus().LastError; got != "connection refused" {
			t.Fatalf("LastError during retry = %q, want %q", got, "connection refused")
		}
	})
}
