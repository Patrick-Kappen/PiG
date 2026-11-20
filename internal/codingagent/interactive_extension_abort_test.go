package codingagent

import (
	"context"
	"runtime"
	"sync"
	"testing"

	"github.com/MichaelKinsy/PiG/agent"
	"github.com/MichaelKinsy/PiG/internal/testbudget"
)

// Pi's ctx.abort() in interactive mode is `void this.session.abort()` (interactive-mode.ts:2399-2401, :4935-4937): it starts the abort and returns,
// and nothing awaits the end of the run. A handler the agent loop awaits can therefore call it. The host action waited for the run to settle, which
// waits for that handler: an extension that called ctx.abort() from a message_update handler hung the run and every later prompt.
//
// The handler here does not sleep. It makes round trips of the owner loop, which runs the abort as one of its tasks, until the call has returned:
// a call that waits for the run never returns, because the run is waiting for this handler, and the test's budget ends the wait.
//
// The round trips are not counted against a bound. The goroutine that calls abort starts on this handler's processor, and the handler and the owner
// loop hand that processor to each other without leaving it, so the call runs only once another processor takes it. With one processor to run on,
// 1,000 round trips passed before the call ran in 196 of 200 runs. The handler therefore yields after every round trip.
func TestExtensionAbortReturnsToAHandlerTheRunAwaits(t *testing.T) {
	m, ctx, release := newStreamingRoutingMode(t)
	bridge := &captureUIBridge{}
	m.opts.SubprocessUIBridge = bridge
	detach := m.wireSubprocessHostCallbacks()
	defer detach()
	abort := bridge.actions["abort"].(func())
	waitForIdle := bridge.actions["waitForIdle"].(func(context.Context) error)

	budget := testbudget.Context(t)
	returned := make(chan struct{})
	called := make(chan struct{})
	abortReturned := make(chan bool, 1)
	var once sync.Once
	// The agent awaits this listener as it awaits an extension's handler.
	unsubscribe := m.agent.Subscribe(func(context.Context, agent.AgentEvent) error {
		once.Do(func() {
			close(called)
			go func() {
				abort()
				close(returned)
			}()
			for {
				onLoop(m, ctx, func() {})
				select {
				case <-returned:
					abortReturned <- true
					return
				case <-budget.Done():
					abortReturned <- false
					return
				default:
					runtime.Gosched()
				}
			}
		})
		return nil
	})
	defer unsubscribe()
	release()
	select {
	case <-called:
	case <-budget.Done():
		t.Fatal("the run delivered no event to the handler")
	}
	if !<-abortReturned {
		t.Fatal("abort did not return to the handler the run awaits: it waits for the run, and the run waits for the handler")
	}
	if err := waitForIdle(ctx); err != nil {
		t.Fatalf("waitForIdle after abort = %v, want nil", err)
	}
	if m.turnActive.Load() {
		t.Fatal("the run was still active after it settled")
	}
}
