package plugins

import "errors"

// Sentinel errors returned by the plugin boundary. Callers should use
// errors.Is rather than matching the human-readable message.
var (
	ErrInvalidManifest   = errors.New("plugin: invalid manifest")
	ErrInvalidProtocol   = errors.New("plugin: invalid protocol message")
	ErrMessageTooLarge   = errors.New("plugin: message exceeds size limit")
	ErrPathEscape        = errors.New("plugin: executable escapes package directory")
	ErrExecutableInvalid = errors.New("plugin: executable is not a regular executable file")
	ErrPluginExited      = errors.New("plugin: process exited")
	ErrPluginNotReady    = errors.New("plugin: process is not ready")
	ErrPluginStopping    = errors.New("plugin: process is stopping")
	ErrPermissionDenied  = errors.New("plugin: permission denied")
	ErrRequestCancelled  = errors.New("plugin: request cancelled")
	ErrHandshakeFailed   = errors.New("plugin: handshake failed")
	ErrHealthFailed      = errors.New("plugin: health check failed")
)
