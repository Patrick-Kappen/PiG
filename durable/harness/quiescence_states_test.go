package harness

import (
	"slices"
	"strings"
	"testing"
)

// The dump awaitQuiescent took when it returned in TestIncrementalIdleStateEqualsAFullRecomputeAfterEveryStepOfRandomNestedWork
// with a task's handler still running: goroutine 558 allocated a map while a garbage collection started and waited for the
// collector's semaphore; every other goroutine waited for a channel. The barrier counted 558 as parked and returned; the
// collector resumed it, and its commit was published with the next step's.
const dumpWithAGoroutineTheRuntimeParked = `goroutine 21 [running]:
github.com/MichaelKinsy/PiG/durable/harness.activeGoroutines(0x15, {0x1d3d00c8a000, 0x10000, 0x10000})
	/home/runner/work/PiG/PiG/durable/harness/quiescence_support_test.go:35 +0x5b

goroutine 1 [chan receive]:
testing.(*T).Run(0x1d3d003e2c08, {0x2760c7d?, 0x1d3d0015fb30?}, 0x2977cc8)
	/usr/local/go/src/testing/testing.go:2130 +0x47c

goroutine 558 [semacquire]:
internal/runtime/maps.newarray(0x2637bd8?, 0x2894140?)
	/usr/local/go/src/runtime/malloc.go:2215 +0x13
internal/runtime/maps.newGroups(...)
	/usr/local/go/src/internal/runtime/maps/group.go:330
github.com/MichaelKinsy/PiG/durable/internal/ordered.restoreStruct({0x2977c10?, 0x1d3d00e2b4a0?, 0x1d3d00fcc7f0?}, 0x1d3d00eca420)
	/home/runner/work/PiG/PiG/durable/internal/ordered/ordered.go:167 +0x1a6

goroutine 594 [select]:
github.com/MichaelKinsy/PiG/durable/harness.(*deferredGate).wait(0x1d3d00eb7590, {0x29c3398, 0x1d3d00eb7320})
	/home/runner/work/PiG/PiG/durable/harness/task_support_test.go:51 +0x89
`

func TestQuiescenceLeavesOutAGoroutineTheRuntimeParked(t *testing.T) {
	if got := activeStates([]byte(dumpWithAGoroutineTheRuntimeParked), 21); !slices.Equal(got, []string{"semacquire"}) {
		t.Fatalf("active states = %q, want the goroutine that waits for the collector's semaphore", got)
	}
	// The caller is left out by its id, and without it the dump's running goroutine counts.
	if got := activeStates([]byte(dumpWithAGoroutineTheRuntimeParked), 0); !slices.Equal(got, []string{"running", "semacquire"}) {
		t.Fatalf("active states = %q, want the running goroutine too", got)
	}
}

func TestQuiescenceCountsEachGoroutineStateOnce(t *testing.T) {
	goroutine := func(state, frame string) string {
		return "goroutine 7 [" + state + "]:\n" + frame + "\n\t/src/file.go:1 +0x1\n\n"
	}
	const own = "github.com/MichaelKinsy/PiG/durable/harness.work(0x1)"
	for _, test := range []struct {
		state, frame string
		active       bool
	}{
		// Running, about to run, or in the kernel.
		{"running", own, true},
		{"runnable", own, true},
		{"syscall", own, true},
		{"idle", own, true},
		// Parked by the runtime, which resumes it by itself.
		{"semacquire", own, true},
		{"semacquire", "internal/runtime/maps.newarray(0x1, 0x2)", true},
		{"GC assist wait", own, true},
		{"GC assist marking", own, true},
		{"GC sweep wait", own, true},
		{"wait for GC cycle", own, true},
		{"GC mark termination", own, true},
		{"stopping the world", own, true},
		{"garbage collection", own, true},
		{"preempted", own, true},
		{"copystack", own, true},
		{"waiting", own, true},
		{"debug call", own, true},
		// A state this Go does not have yet.
		{"a wait reason of a later Go", own, true},
		// Parked until the program or something outside it acts.
		{"chan receive", own, false},
		{"chan send", own, false},
		{"select", own, false},
		{"select (no cases)", own, false},
		{"chan receive (nil chan)", own, false},
		{"sync.Mutex.Lock", "sync.runtime_SemacquireMutex(0x1, 0x0, 0x1)", false},
		{"sync.RWMutex.RLock", "sync.runtime_SemacquireRWMutexR(0x1, 0x0, 0x0)", false},
		{"sync.RWMutex.Lock", "sync.runtime_SemacquireRWMutex(0x1, 0x0, 0x0)", false},
		{"sync.Cond.Wait", "sync.runtime_notifyListWait(0x1, 0x0)", false},
		{"sync.WaitGroup.Wait", "sync.runtime_SemacquireWaitGroup(0x1, 0x0)", false},
		{"semacquire", "internal/poll.runtime_Semacquire(0x1)", false},
		{"semacquire", "sync.runtime_Semacquire(0x1)", false},
		{"IO wait", "internal/poll.runtime_pollWait(0x1, 0x72)", false},
		{"sleep", "time.Sleep(0x1)", false},
		{"coroutine", own, false},
		{"finalizer wait", "runtime.runFinalizers()", false},
		// What follows the state in the brackets is not part of it.
		{"select, 5 minutes", own, false},
		{"chan receive, locked to thread", own, false},
		{"runnable, locked to thread", own, true},
		{"semacquire, 2 minutes", own, true},
	} {
		got := activeStates([]byte(goroutine(test.state, test.frame)), 1)
		if (len(got) == 1) != test.active || len(got) > 1 {
			t.Errorf("[%s] with first frame %s: active states = %q, want active = %v", test.state, test.frame, got, test.active)
		}
		if state, _, _ := strings.Cut(test.state, ","); test.active && len(got) == 1 && got[0] != state {
			t.Errorf("[%s]: reported as %q, want %q", test.state, got[0], state)
		}
	}
	// Several goroutines, and lines that are not a goroutine's header.
	dump := goroutine("chan receive", own) + goroutine("GC assist wait", own) + "goroutine profile: total 3\n" + goroutine("runnable", own)
	if got := activeStates([]byte(dump), 1); !slices.Equal(got, []string{"GC assist wait", "runnable"}) {
		t.Errorf("active states = %q, want the two that are not parked for good", got)
	}
	// Every goroutine in a dump has id 7 here: the caller's own id leaves them all out.
	if got := activeStates([]byte(dump), 7); len(got) != 0 {
		t.Errorf("active states = %q, want none for the caller's own goroutine", got)
	}
}
