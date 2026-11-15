package coding

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/MichaelKinsy/PiG/coding/extension/host/inproc"
	icodingagent "github.com/MichaelKinsy/PiG/internal/codingagent"
)

func TestSessionCacheObservationThroughTheSessionWithWarmingOff(t *testing.T) {
	sess, provider := newWarmingSession(t, "off", SessionOptions{NoSession: true})
	if got := sess.SessionCacheObservation(); got != nil {
		t.Fatal(got)
	}
	before := time.Now().UnixMilli()
	sendAndSettle(t, sess)
	got := sess.SessionCacheObservation()
	if got == nil || got.StartedAt < before || got.StartedAt > time.Now().UnixMilli() || got.TTLms != 300000 || got.Provider != "capture-provider" || got.Model != "capture-model" {
		t.Fatalf("%+v", got)
	}
	if len(provider.calls()) != 1 {
		t.Fatal("diagnostic sent a request")
	}
	changed := *sess.Model()
	changed.ID = "different-model"
	sess.Agent().SetModel(&changed)
	if got := sess.SessionCacheObservation(); got != nil {
		t.Fatal("model switch retained another model's observation", got)
	}
	sess.ReplaceInner(icodingagent.NewSession("replacement", t.TempDir()))
	if got := sess.SessionCacheObservation(); got != nil {
		t.Fatal("replacement retained observation", got)
	}
}

type failedCacheProvider struct {
	warmingProvider
	aborted bool
}

func (p *failedCacheProvider) Stream(context.Context, ai.TranscriptContext, ai.StreamOptions) (*ai.AssistantMessageEventStream, error) {
	if p.aborted {
		stream := ai.NewAssistantMessageEventStream()
		stream.End(&ai.AssistantMessage{StopReason: ai.StopReasonAborted})
		return stream, nil
	}
	return nil, errors.New("request failed before writing cache")
}

func TestSessionCacheObservationFailedRealRequestIsUnknown(t *testing.T) {
	for _, aborted := range []bool{false, true} {
		sess, _ := newWarmingSession(t, "off", SessionOptions{NoSession: true})
		sendAndSettle(t, sess)
		if sess.SessionCacheObservation() == nil {
			t.Fatal("missing initial observation")
		}
		sess.Agent().SetModel(warmingModel(&failedCacheProvider{aborted: aborted}))
		_, _ = sess.Send(t.Context(), "fail")
		if got := sess.SessionCacheObservation(); got != nil {
			t.Fatalf("failed request reported known cache: %+v", got)
		}
	}
}

func TestSessionCacheObservationNativeContextAndStaleGuard(t *testing.T) {
	sess, _ := newWarmingSession(t, "off", SessionOptions{NoSession: true})
	sendAndSettle(t, sess)
	active := true
	ctx := extension.NewContext(t.TempDir(), nil, func() error {
		if !active {
			return ErrSessionNotReady
		}
		return nil
	}, extension.ContextActions{GetSessionCacheObservation: sess.SessionCacheObservation})
	got, err := ctx.GetSessionCacheObservation()
	if err != nil || got == nil {
		t.Fatal(got, err)
	}
	active = false
	if _, err := ctx.GetSessionCacheObservation(); err == nil {
		t.Fatal("stale diagnostic succeeded")
	}
}

// A Session without a CLI mode (createAgentSession) and a mode that rebinds the runner without the diagnostic (interactive) both read the Session's own binding.
func TestSessionCacheObservationThroughTheSessionBoundRunner(t *testing.T) {
	runner := inproc.NewRunner(nil, t.TempDir())
	sess, _ := newWarmingSession(t, "off", SessionOptions{NoSession: true, Runner: runner})
	sendAndSettle(t, sess)
	want := sess.SessionCacheObservation()
	if want == nil {
		t.Fatal("missing Session observation")
	}
	for _, binding := range []string{"session", "mode"} {
		got, err := runner.CreateCommandContext().GetSessionCacheObservation()
		if err != nil || got == nil || *got != *want {
			t.Fatalf("%s binding: got %+v, %v; want %+v", binding, got, err, want)
		}
		runner.BindCore(extension.ExtensionActions{}, extension.ContextActions{}, nil)
	}
}
