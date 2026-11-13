// Package directcore attaches MyPiglets' real core and gateway to the native host API.
package directcore

import (
	"context"
	"errors"
	"fmt"

	"github.com/Patrick-Kappen/MyPiglets/extensions/core/api"
	"github.com/Patrick-Kappen/MyPiglets/extensions/core/run"
	"github.com/Patrick-Kappen/MyPiglets/modules/gateway"

	"github.com/MichaelKinsy/PiG/coding/extension"
)

// Extension is the native factory for the core with the gateway selected by piglets/pig/piglet.yaml.
func Extension(host extension.API) error { return Attach(host, gateway.Module()) }

// Attach supplies the native subscriptions used by the current core/gateway composition. Unsupported event bindings fail at load rather than silently losing a module's hook.
func Attach(host extension.API, modules ...api.Module) error {
	registry, err := run.NewRegistry(modules...)
	if err != nil {
		return err
	}
	for _, event := range registry.Events() {
		switch event {
		case api.EventBeforeProviderHeaders, api.EventSessionStart, api.EventSessionShutdown:
		default:
			return fmt.Errorf("core native attachment does not support event %q", event)
		}
	}
	return run.Attach("core", modules, func(event string, handler run.Handler) {
		switch api.Event(event) {
		case api.EventBeforeProviderHeaders:
			host.OnBeforeProviderHeaders(func(ctx context.Context, event extension.BeforeProviderHeadersEvent) error {
				data := make(map[string]any, len(event.Headers))
				for key, value := range event.Headers {
					data[key] = nil
					if value != nil {
						data[key] = *value
					}
				}
				result, err := handler(nativeContext{ctx}, map[string]any{"type": event.Type, "headers": data})
				if err != nil || result == nil {
					return err
				}
				values, ok := result.(map[string]any)
				if !ok {
					return fmt.Errorf("core native headers result is %T, want map[string]any", result)
				}
				replacement := make(extension.ProviderHeaders, len(values))
				for key, value := range values {
					if value == nil {
						replacement[key] = nil
						continue
					}
					text, ok := value.(string)
					if !ok {
						return fmt.Errorf("core native header %q is %T, want string or nil", key, value)
					}
					replacement[key] = &text
				}
				clear(event.Headers)
				for key, value := range replacement {
					event.Headers[key] = value
				}
				return nil
			})
		case api.EventSessionStart:
			host.OnSessionStart(func(ctx context.Context, event extension.SessionStartEvent) error {
				data := map[string]any{"type": event.Type, "reason": event.Reason}
				if event.PreviousSessionFile != "" {
					data["previousSessionFile"] = event.PreviousSessionFile
				}
				_, err := handler(nativeContext{ctx}, data)
				return err
			})
		case api.EventSessionShutdown:
			host.OnSessionShutdown(func(ctx context.Context, event extension.SessionShutdownEvent) error {
				data := map[string]any{"type": event.Type, "reason": event.Reason}
				if event.TargetSessionFile != "" {
					data["targetSessionFile"] = event.TargetSessionFile
				}
				_, err := handler(nativeContext{ctx}, data)
				return err
			})
		}
	})
}

type nativeContext struct{ context.Context }

func (c nativeContext) GetSessionID() (string, error) {
	manager, err := extension.FromContext(c.Context).SessionManager()
	if err != nil {
		return "", err
	}
	if manager == nil {
		return "", errors.New("core native attachment has no SessionManager")
	}
	return manager.GetSessionId(), nil
}
