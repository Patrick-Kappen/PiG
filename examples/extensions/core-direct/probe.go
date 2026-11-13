package directcore

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/MichaelKinsy/PiG/coding/extension"
)

// ObservedExtension is Extension with optional lifecycle observations for a no-turn CLI run. The shutdown marker is written only after the real core handler returns.
func ObservedExtension(host extension.API) error {
	path := os.Getenv("PIG_CORE_DIRECT_TRACE")
	if path == "" {
		return Extension(host)
	}
	host.OnSessionStart(func(context.Context, extension.SessionStartEvent) error {
		return appendTrace(path, "host session_start; real core/gateway has no session_start hook")
	})
	return Extension(observedAPI{API: host, path: path})
}

type observedAPI struct {
	extension.API
	path string
}

func (a observedAPI) OnSessionShutdown(handler extension.ExtensionHandlerNoResult[extension.SessionShutdownEvent]) func() {
	return a.API.OnSessionShutdown(func(ctx context.Context, event extension.SessionShutdownEvent) error {
		if err := handler(ctx, event); err != nil {
			return err
		}
		return appendTrace(a.path, "real core session_shutdown completed (dispatch, forget, drain)")
	})
}

func appendTrace(path, line string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(file, line)
	return errors.Join(err, file.Close())
}
