package host

import (
	"time"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/rpc"
)

// callFailure keeps RPC codes and sentinel identity while rendering the hub-owned explanation in a request's language.
type callFailure struct {
	message error
	cause   error
}

func (e *callFailure) Error() string { return e.message.Error() }
func (e *callFailure) Text(l i18n.Lang) string {
	return i18n.ExtensionStatusText(l, i18n.Localize(e.message, l))
}
func (e *callFailure) Unwrap() []error { return []error{e.message, e.cause} }

func newCallTimeout(name, method string, duration time.Duration) *callFailure {
	msg := i18n.ErrEnglish("extension-status.call_timeout", i18n.Args{"name": name, "method": method, "duration": duration.String()})
	return &callFailure{message: msg, cause: &rpc.Error{Code: extension.CodeTimeout, Message: msg.Error()}}
}
