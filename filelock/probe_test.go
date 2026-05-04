package filelock

import (
	"os"
	"runtime"
	"testing"
	"time"
)

func TestProbeOwnPIDIsAlive(t *testing.T) {
	got := probePID(os.Getpid(), time.Time{})
	if got != probeAlive {
		t.Fatalf("probe of own PID = %s, want alive", got)
	}
}

func TestProbeNonZeroPIDOutOfRange(t *testing.T) {
	// PID 0 → no such process by our contract.
	if got := probePID(0, time.Time{}); got != probeDead {
		t.Fatalf("probe(0) = %s, want dead", got)
	}
	if got := probePID(-1, time.Time{}); got != probeDead {
		t.Fatalf("probe(-1) = %s, want dead", got)
	}
}

func TestProbeImplausiblyHighPIDIsDead(t *testing.T) {
	// Most OSes cap PIDs well below this — pid 4_000_000 is essentially
	// guaranteed to not exist. The probe should report dead, not
	// inconclusive.
	got := probePID(4_000_000, time.Time{})
	if got != probeDead {
		t.Fatalf("probe(4000000) = %s, want dead", got)
	}
}

func TestProbeStartTimeMismatchIsDead(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("processStartTime only implemented on Linux")
	}
	// Probe own PID with a deliberately-wrong expected start. The real
	// start time will not match → result must be probeDead (PID-reuse
	// detection working).
	wrong := time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC)
	got := probePID(os.Getpid(), wrong)
	if got != probeDead {
		t.Fatalf("probe with wrong start time = %s, want dead", got)
	}
}

func TestProbeStartTimeMatchIsAlive(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("processStartTime only implemented on Linux")
	}
	actual := processStartTime(os.Getpid())
	if actual.IsZero() {
		t.Skip("could not read own start time; skipping")
	}
	got := probePID(os.Getpid(), actual)
	if got != probeAlive {
		t.Fatalf("probe with matching start time = %s, want alive", got)
	}
}

func TestProcessStartTimeOwnPIDOnLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("/proc only on Linux")
	}
	got := processStartTime(os.Getpid())
	if got.IsZero() {
		t.Fatal("expected non-zero start time for own PID on Linux")
	}
	// Sanity: own start time can't be in the future.
	if got.After(time.Now()) {
		t.Fatalf("start time %v is in the future", got)
	}
}

func TestProcessStartTimeNonLinux(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("Linux has a real implementation; this checks the stub")
	}
	got := processStartTime(os.Getpid())
	if !got.IsZero() {
		t.Fatalf("non-Linux start time = %v, want zero", got)
	}
}

func TestProbeResultString(t *testing.T) {
	cases := []struct {
		r    probeResult
		want string
	}{
		{probeAlive, "alive"},
		{probeDead, "dead"},
		{probeInconclusive, "inconclusive"},
		{probeResult(99), "unknown"},
	}
	for _, c := range cases {
		if got := c.r.String(); got != c.want {
			t.Errorf("probeResult(%d).String() = %q, want %q", c.r, got, c.want)
		}
	}
}
