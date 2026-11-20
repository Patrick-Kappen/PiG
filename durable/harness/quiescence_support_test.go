package harness

// awaitQuiescent is the barrier behind `await flush()` of upstream's tests (a macrotask turn: every ready microtask has run).
//
// Upstream runs on one thread, so when a macrotask starts, everything that was ready has run up to its next await. The Go
// equivalent is a moment when no goroutine is running, runnable, or in a system call: each one has run up to a wait and
// nothing it waits for has happened yet. It reads the state of every goroutine from a goroutine dump, which stops the world,
// so one snapshot with only parked goroutines is stable until something outside the process (a timer, the test) acts. It
// waits for that and never for a duration.
//
// Parked is not enough: the runtime parks a goroutine too, in the middle of whatever it was doing, and resumes it without
// anything outside acting. A goroutine that allocates while a garbage collection starts waits for the collector's
// semaphore (`[semacquire]`, with the allocation as its first frame), and others wait to assist, to sweep or for the world
// to stop. Such a goroutine has not run up to a wait, so only the waits of parkedForGood count as quiescent
// (TestQuiescenceLeavesOutAGoroutineTheRuntimeParked has the dump that showed it).

import (
	"bytes"
	"runtime"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/internal/goroutineid"
)

// quiescenceTimeout bounds the wait for quiescence: a goroutine that never parks fails the test instead of hanging it.
const quiescenceTimeout = 30 * time.Second

// parkedForGood is the goroutine states (Go 1.27's wait reasons and statuses) in which a goroutine stays until the program
// or something outside it acts: a channel, a lock or a wait group of the program, a timer, the network poller. Every other
// state is active: running, runnable or in a system call, or parked by the runtime for a garbage collection, a stop of the
// world, a preemption or a stack copy, from which the runtime resumes it by itself. A state this table does not name is
// active, so a new wait reason of a later Go makes awaitQuiescent wait and then name the state, and never return early.
var parkedForGood = map[string]bool{
	"chan receive": true, "chan send": true, "select": true,
	"chan receive (nil chan)": true, "chan send (nil chan)": true, "select (no cases)": true,
	"chan receive (durable)": true, "chan send (durable)": true, "select (durable)": true,
	"sync.Mutex.Lock": true, "sync.RWMutex.RLock": true, "sync.RWMutex.Lock": true, "sync.Cond.Wait": true,
	"sync.WaitGroup.Wait": true, "sync.WaitGroup.Wait (durable)": true,
	"IO wait": true, "sleep": true, "coroutine": true, "synctest.Run": true, "synctest.Wait": true,
	// The runtime's own goroutines when they have nothing to do; a dump shows them only with GOTRACEBACK=system.
	"finalizer wait": true, "cleanup wait": true, "force gc (idle)": true, "GOMAXPROCS updater (idle)": true,
	"GC worker (idle)": true, "GC scavenge wait": true, "trace reader (blocked)": true,
	"dead": true, "leaked": true, "waiting for cgo callback": true,
}

// programSemaphores are the first frames of a goroutine in `[semacquire]` that waits for a semaphore of the program (a file
// descriptor's lock, a sync primitive). The runtime's own semaphores have the same state and whatever the goroutine was
// doing as the first frame: a dump leaves the runtime's frames out.
var programSemaphores = []string{"internal/poll.runtime_Semacquire(", "sync.runtime_Semacquire("}

// activeStates lists, for a goroutine dump, the state of each goroutine other than self that is not parked for good.
func activeStates(dump []byte, self uint64) []string {
	var active []string
	header := []byte("goroutine ")
	rest := dump
	for len(rest) > 0 {
		line := rest
		if end := bytes.IndexByte(rest, '\n'); end >= 0 {
			line, rest = rest[:end], rest[end+1:]
		} else {
			rest = nil
		}
		if !bytes.HasPrefix(line, header) {
			continue
		}
		line = line[len(header):]
		space := bytes.IndexByte(line, ' ')
		if space < 0 {
			continue
		}
		id, err := strconv.ParseUint(string(line[:space]), 10, 64)
		if err != nil || id == self {
			continue
		}
		state := line[space+1:]
		if len(state) == 0 || state[0] != '[' {
			continue
		}
		state = state[1:]
		if end := bytes.IndexAny(state, ",]"); end >= 0 {
			state = state[:end]
		}
		// The line after the header is the goroutine's first frame.
		frame := rest
		if end := bytes.IndexByte(frame, '\n'); end >= 0 {
			frame = frame[:end]
		}
		switch {
		case parkedForGood[string(state)]:
		case string(state) == "semacquire" && slices.ContainsFunc(programSemaphores, func(prefix string) bool { return bytes.HasPrefix(frame, []byte(prefix)) }):
		default:
			active = append(active, string(state))
		}
	}
	return active
}

// activeGoroutines takes a dump of every goroutine and returns the states of those other than self that are not parked for
// good; buffer is reused as the dump buffer.
func activeGoroutines(self uint64, buffer []byte) ([]string, []byte) {
	if cap(buffer) < 64<<10 {
		buffer = make([]byte, 64<<10)
	}
	buffer = buffer[:cap(buffer)]
	for {
		n := runtime.Stack(buffer, true)
		if n < len(buffer) {
			buffer = buffer[:n]
			break
		}
		buffer = make([]byte, 2*len(buffer))
	}
	return activeStates(buffer, self), buffer
}

// awaitQuiescent returns once every goroutine but the caller is parked in a wait.
func awaitQuiescent(t testing.TB) {
	t.Helper()
	self := goroutineid.Current()
	var buffer []byte
	deadline := time.Now().Add(quiescenceTimeout)
	for attempt := 0; ; attempt++ {
		var active []string
		if active, buffer = activeGoroutines(self, buffer); len(active) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d goroutines still ran after %s, in the states %q", len(active), quiescenceTimeout, active)
		}
		if attempt < 100 {
			runtime.Gosched()
		} else {
			time.Sleep(100 * time.Microsecond)
		}
	}
}
