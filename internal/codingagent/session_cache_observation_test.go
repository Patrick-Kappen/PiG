package codingagent

import (
	"fmt"
	"math"
	"testing"
	"testing/synctest"
	"time"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/MichaelKinsy/PiG/coding/extension/host/inproc"
	"github.com/MichaelKinsy/PiG/coding/extension/host/subprocess"
)

func TestSessionCacheObservationOffAndEffectiveLifetime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		t.Setenv("PI_CACHE_RETENTION", "long")
		model := cacheWarmingTestModel(t, "anthropic/claude-opus-4-6", ai.ModelPromptCache{"short": 120, "long": 1800})
		f := newFakeWarmRuntime(t, withMode("off"))
		if got := f.warmer.SessionCacheObservation(); got != nil {
			t.Fatal(got)
		}
		for _, tc := range []struct {
			options ai.StreamOptions
			want    float64
		}{
			{ai.StreamOptions{}, 1800000},
			{ai.StreamOptions{Env: ai.ProviderEnv{"PI_CACHE_RETENTION": "short"}}, 120000},
			{ai.StreamOptions{CacheRetention: ai.CacheRetentionShort}, 120000},
			{ai.StreamOptions{CacheRetention: ai.CacheRetentionNone}, 0},
		} {
			at := time.Now().UnixMilli()
			f.warmer.Start(warmRequest(model, tc.options), alwaysCurrent)
			got := f.warmer.SessionCacheObservation()
			if tc.want == 0 {
				if got != nil {
					t.Fatal(got)
				}
				continue
			}
			if got == nil || got.StartedAt != at || got.TTLms != tc.want || got.Model != model.ID || got.Provider == "" {
				t.Fatalf("got %+v want lifetime %v at %v", got, tc.want, at)
			}
			advance(240 * time.Second)
			if later := f.warmer.SessionCacheObservation(); later == nil || *later != *got {
				t.Fatal("expired observation became unknown or moved", later)
			}
		}
		model.PromptCache = nil
		f.warmer.Start(warmRequest(model, ai.StreamOptions{}), alwaysCurrent)
		if got := f.warmer.SessionCacheObservation(); got != nil {
			t.Fatal(got)
		}
		if f.callCount() != 0 {
			t.Fatal("observation enabled warming")
		}
	})
}

func TestSessionCacheObservationDoesNotMoveWarmerClock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFakeWarmRuntime(t)
		f.warmer.getMode = func() CacheWarmingMode { time.Sleep(time.Second); return "idle" }
		model := cacheWarmingTestModel(t, "anthropic/claude-opus-4-6", ai.ModelPromptCache{"short": 300})
		before := time.Now().UnixMilli()
		f.warmer.Start(warmRequest(model, ai.StreamOptions{}), alwaysCurrent)
		f.warmer.mu.Lock()
		startedAt := f.warmer.run.startedAt
		f.warmer.mu.Unlock()
		if startedAt != before+1000 {
			t.Fatal("diagnostic moved Pi's warmer safety clock", startedAt, before)
		}
	})
}

func TestSessionCacheObservationRejectsUnusableMetadata(t *testing.T) {
	for _, ttl := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		f := newFakeWarmRuntime(t, withMode("off"))
		model := &ai.Model{ID: "custom-observation", PromptCache: ai.ModelPromptCache{"short": ttl}}
		f.warmer.Start(warmRequest(model, ai.StreamOptions{CacheRetention: ai.CacheRetentionShort}), alwaysCurrent)
		if got := f.warmer.SessionCacheObservation(); got != nil {
			t.Fatal("unusable metadata reported known", got)
		}
	}
	f := newFakeWarmRuntime(t, withMode("off"))
	f.warmer.Start(CacheWarmRequest{}, alwaysCurrent)
	if got := f.warmer.SessionCacheObservation(); got != nil {
		t.Fatal("nil model reported known", got)
	}
}

func TestSessionCacheObservationDelayedAndRetiredRefresh(t *testing.T) {
	for _, retire := range []bool{false, true} {
		t.Run(fmt.Sprint(retire), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				model := cacheWarmingTestModel(t, "anthropic/claude-opus-4-6", ai.ModelPromptCache{"short": 300})
				f := newFakeWarmRuntime(t, withResult(func(m *ai.Model) *ai.AssistantMessageEventStream {
					time.Sleep(30 * time.Second)
					return finishedStream(warmResponse(m, ai.StopReasonLength))
				}))
				f.warmer.Start(warmRequest(model, ai.StreamOptions{}), alwaysCurrent)
				first := f.warmer.SessionCacheObservation()
				advance(270 * time.Second)
				if got := f.warmer.SessionCacheObservation(); got.StartedAt != first.StartedAt {
					t.Fatal("in-flight refresh renewed cache")
				}
				expected := first.StartedAt + 270000
				if retire {
					advance(5 * time.Second)
					f.warmer.Start(warmRequest(model, ai.StreamOptions{}), alwaysCurrent)
					expected += 5000
					advance(25 * time.Second)
				} else {
					advance(30 * time.Second)
				}
				got := f.warmer.SessionCacheObservation()
				if got == nil || got.StartedAt != expected {
					t.Fatal("refresh used completion time or overwrote newer request", got)
				}
			})
		})
	}
}

func TestSessionCacheObservationFailedRefreshDoesNotRenew(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		model := cacheWarmingTestModel(t, "anthropic/claude-opus-4-6", ai.ModelPromptCache{"short": 300})
		f := newFakeWarmRuntime(t, withResult(func(m *ai.Model) *ai.AssistantMessageEventStream {
			return finishedStream(warmResponse(m, ai.StopReasonError))
		}))
		f.warmer.Start(warmRequest(model, ai.StreamOptions{}), alwaysCurrent)
		first := f.warmer.SessionCacheObservation()
		advance(270 * time.Second)
		got := f.warmer.SessionCacheObservation()
		if got == nil || *got != *first {
			t.Fatal("failed refresh renewed cache", got)
		}
		got.TTLms = 1
		if f.warmer.SessionCacheObservation().TTLms != first.TTLms {
			t.Fatal("caller mutated diagnostic state")
		}
	})
}

func BenchmarkSessionCacheObservation(b *testing.B) {
	model := &ai.Model{ID: "observation-bench", PromptCache: ai.ModelPromptCache{"short": 300}}
	w := NewCacheWarmer(nil, nil, func() CacheWarmingMode { return "off" }, nil)
	defer w.Close()
	w.Start(warmRequest(model, ai.StreamOptions{}), alwaysCurrent)
	b.ReportAllocs()
	for b.Loop() {
		if w.SessionCacheObservation() == nil {
			b.Fatal("missing observation")
		}
	}
}

func TestSessionCacheObservationRefreshUsesDispatchTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		model := cacheWarmingTestModel(t, "anthropic/claude-opus-4-6", ai.ModelPromptCache{"short": 300})
		f := newFakeWarmRuntime(t)
		f.warmer.Start(warmRequest(model, ai.StreamOptions{}), alwaysCurrent)
		first := f.warmer.SessionCacheObservation()
		advance(270 * time.Second)
		next := f.warmer.SessionCacheObservation()
		if first == nil || next == nil || next.StartedAt != first.StartedAt+270000 || next.TTLms != first.TTLms || f.callCount() != 1 {
			t.Fatalf("first=%+v next=%+v calls=%d", first, next, f.callCount())
		}
		f.warmer.OnAgentSettled()
		f.setMode("off")
		f.warmer.OnModeChanged()
		if got := f.warmer.SessionCacheObservation(); got == nil || *got != *next {
			t.Fatal("warming stop lost cache observation", got)
		}
		f.warmer.Close()
		if got := f.warmer.SessionCacheObservation(); got != nil {
			t.Fatal("closed warmer still reports cache", got)
		}
	})
}

// Interactive mode owns its Extension Host's bridge and rebinds the runner, so both routes read the Session it shows at the time of the call, as after a replacement.
func TestInteractiveModeReadsTheCurrentSessionsCacheObservation(t *testing.T) {
	bridge := &captureUIBridge{}
	m := newRunOnMainProbe(t)
	m.opts.SessionHandle, m.opts.SubprocessUIBridge = nil, bridge
	m.newRunner = inproc.NewRunner(nil, m.opts.CWD)
	defer m.wireSubprocessHostCallbacks()()
	m.wireInprocContextActions()
	wire, bound := bridge.actions[subprocess.CallGetSessionCacheObservation].(func() *extension.SessionCacheObservation)
	if !bound {
		t.Fatal("interactive mode leaves the wire diagnostic unbound")
	}
	direct := func() *extension.SessionCacheObservation {
		got, err := m.newRunner.CreateCommandContext().GetSessionCacheObservation()
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if wire() != nil || direct() != nil {
		t.Fatal("a mode without a Session reported an observation")
	}
	for _, want := range []*extension.SessionCacheObservation{{StartedAt: 1, TTLms: 300000, Provider: "p", Model: "first"}, nil, {StartedAt: 2, TTLms: 3600000, Provider: "p", Model: "replacement"}} {
		m.opts.SessionHandle = &recordingCompactHandle{cacheObservation: want}
		if got := wire(); got != want {
			t.Errorf("wire = %+v, want %+v", got, want)
		}
		if got := direct(); got != want {
			t.Errorf("direct = %+v, want %+v", got, want)
		}
	}
}
