package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// AuthorizationRequest is evaluated immediately before a plugin tool is
// invoked. A manifest declaration is not a grant; the host authorizer must
// intersect it with the owner's current permission grants.
type AuthorizationRequest struct {
	PluginID   string
	ToolID     string
	Capability string
	Scope      json.RawMessage
	Action     string
	Risk       domain.RiskLevel
}

type AuthorizationResult struct {
	Allowed bool
	Reason  string
}

// Authorizer is deliberately injected so the runtime remains independent of
// SQLite and the application's policy implementation. A nil authorizer is
// deny-by-default whenever a tool declares a capability.
type Authorizer interface {
	Authorize(context.Context, AuthorizationRequest) (AuthorizationResult, error)
}

type AllowAllAuthorizer struct{}

func (AllowAllAuthorizer) Authorize(context.Context, AuthorizationRequest) (AuthorizationResult, error) {
	return AuthorizationResult{Allowed: true}, nil
}

const (
	// DefaultCancelGrace is how long a plugin has to answer a health probe
	// after a cancelled request before its process group is killed.
	DefaultCancelGrace = 2 * time.Second
	// DefaultRestartWindow and DefaultMaxRestartsPerWindow form the crash-loop
	// circuit breaker.
	DefaultRestartWindow        = 60 * time.Second
	DefaultMaxRestartsPerWindow = 5
)

type LifecycleState string

const (
	StateStopped  LifecycleState = "stopped"
	StateStarting LifecycleState = "starting"
	StateRunning  LifecycleState = "running"
	StateStopping LifecycleState = "stopping"
	StateCrashed  LifecycleState = "crashed"
	StateFailed   LifecycleState = "failed"
)

// RestartPolicy controls optional automatic recovery after an unexpected
// process exit. The default zero value disables automatic restart; callers
// can still call Supervisor.Restart explicitly.
//
// MaxAttempts bounds one *consecutive* failure streak. It cannot catch a
// plugin that handshakes successfully and then crashes a second later, because
// every successful start resets the streak. MaxRestartsPerWindow is the global
// circuit breaker for that case: more than N crashes inside RestartWindow puts
// the plugin into StateFailed instead of restarting it forever.
type RestartPolicy struct {
	MaxAttempts    int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration

	RestartWindow        time.Duration
	MaxRestartsPerWindow int
}

type SupervisorConfig struct {
	Manifest          Manifest
	PackageDir        string
	CoreVersion       string
	Authorizer        Authorizer
	EffectiveGrants   []PermissionGrant
	Client            ClientConfig
	DevMode           bool
	SignatureVerified bool
	Restart           RestartPolicy
	// CancelGrace bounds how long a plugin has to prove it is still responsive
	// after a request.cancel before the host escalates to killing its process
	// group.
	CancelGrace time.Duration
}

type Supervisor struct {
	config SupervisorConfig

	mu           sync.Mutex
	state        LifecycleState
	lastErr      error
	client       *Client
	lifecycleCtx context.Context
	cancel       context.CancelFunc
	restarting   bool
	restartWG    sync.WaitGroup
	crashes      []time.Time
	stderrTail   string
	dropped      uint64
}

func NewSupervisor(config SupervisorConfig) (*Supervisor, error) {
	if err := config.Manifest.Validate(); err != nil {
		return nil, err
	}
	if !config.Manifest.CompatibleWithCore(config.CoreVersion) {
		return nil, fmt.Errorf("%w: plugin %s is incompatible with core %s", ErrInvalidManifest, config.Manifest.ID, config.CoreVersion)
	}
	if !config.DevMode && !config.SignatureVerified {
		return nil, fmt.Errorf("%w: package signature was not verified by the host trust store", ErrInvalidManifest)
	}
	if config.Restart.InitialBackoff <= 0 {
		config.Restart.InitialBackoff = 100 * time.Millisecond
	}
	if config.Restart.MaxBackoff <= 0 {
		config.Restart.MaxBackoff = 5 * time.Second
	}
	if config.Restart.RestartWindow <= 0 {
		config.Restart.RestartWindow = DefaultRestartWindow
	}
	if config.Restart.MaxRestartsPerWindow <= 0 {
		config.Restart.MaxRestartsPerWindow = DefaultMaxRestartsPerWindow
	}
	if config.CancelGrace <= 0 {
		config.CancelGrace = DefaultCancelGrace
	}
	if config.Client.MaxMessageBytes <= 0 {
		config.Client.MaxMessageBytes = DefaultMaxMessageBytes
	}
	if config.Client.CloseTimeout <= 0 {
		config.Client.CloseTimeout = DefaultCloseTimeout
	}
	return &Supervisor{config: config, state: StateStopped}, nil
}

// Start validates the package, launches the process, completes the handshake
// and performs a health check before exposing it as running.
func (s *Supervisor) Start(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: nil context", ErrPluginNotReady)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	if s.state == StateRunning || s.state == StateStarting {
		s.mu.Unlock()
		return fmt.Errorf("%w: plugin is already active", ErrPluginNotReady)
	}
	if s.state == StateStopping {
		s.mu.Unlock()
		return ErrPluginStopping
	}
	s.state = StateStarting
	s.lastErr = nil
	if s.lifecycleCtx == nil || contextDone(s.lifecycleCtx) {
		s.lifecycleCtx, s.cancel = context.WithCancel(context.Background())
	}
	lifecycleCtx := s.lifecycleCtx
	s.mu.Unlock()

	executable, err := s.config.Manifest.ResolveExecutable(s.config.PackageDir)
	if err == nil {
		err = s.config.Manifest.VerifyChecksum(s.config.PackageDir)
	}
	if err != nil {
		s.setFailure(err)
		return err
	}
	clientConfig := s.config.Client
	clientConfig.Executable = executable
	clientConfig.Dir = s.config.PackageDir
	clientConfig.Env = append([]string(nil), clientConfig.Env...)
	clientConfig.Env = append(clientConfig.Env,
		"YURI_PLUGIN_PROTOCOL="+ProtocolVersion,
		"YURI_PLUGIN_ID="+s.config.Manifest.ID,
	)
	client, err := NewClient(lifecycleCtx, clientConfig)
	if err != nil {
		s.setFailure(err)
		return err
	}

	handshakeCtx, cancelHandshake := context.WithCancel(ctx)
	defer cancelHandshake()
	_, err = client.Handshake(handshakeCtx, HandshakeParams{
		ProtocolVersion: ProtocolVersion,
		CoreVersion:     s.config.CoreVersion,
		PluginID:        s.config.Manifest.ID,
		Grants:          s.grantedCapabilities(),
	})
	if err == nil {
		_, err = client.Health(handshakeCtx, HealthParams{})
	}
	if err != nil {
		_ = client.Close()
		s.setFailure(err)
		return err
	}

	s.mu.Lock()
	s.client = client
	s.state = StateRunning
	s.lastErr = nil
	s.mu.Unlock()
	s.restartWG.Add(1)
	go s.watch(client)
	return nil
}

func (s *Supervisor) watch(client *Client) {
	defer s.restartWG.Done()
	// The watcher owns the supervisor's whole crash story: without it the
	// supervisor keeps reporting StateRunning for a dead session, Events()
	// keeps handing out a closed channel, and the desktop bridge above spins on
	// a plugin it believes is healthy. See failWatch for what "honest terminal
	// state" means here.
	defer recoverPluginGoroutine("plugin_supervisor_watch", func(err error) {
		s.failWatch(client, err)
	})
	<-client.Done()
	s.handleClientExit(client, client.Err())
}

// handleClientExit records one client's exit. The whole body runs under s.mu
// with a deferred unlock: the previous non-deferred unlock meant a panic
// anywhere inside would leave the supervisor mutex held forever, deadlocking
// every Supervisor method — including the recovery reporter.
func (s *Supervisor) handleClientExit(client *Client, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fireFaultHook(faultSupervisorWatch)
	if s.client != client {
		return
	}
	if s.state == StateStopping || s.state == StateStopped {
		return
	}
	s.stderrTail = client.StderrTail()
	s.dropped += client.DroppedEvents()
	// s.client is cleared only once the exiting client has been accounted for,
	// so "s.client is still this client" is an exact test for "this exit has
	// not been recorded yet" — which is what failWatch relies on.
	s.client = nil
	s.state = StateCrashed
	s.lastErr = withDiagnostics(err, s.stderrTail)
	if s.cancel != nil {
		s.cancel()
	}
	s.lifecycleCtx = nil
	policy := s.config.Restart
	tripped := s.recordCrashLocked(time.Now(), policy)
	shouldRestart := policy.MaxAttempts > 0 && !tripped
	if tripped {
		s.state = StateFailed
		s.lastErr = withDiagnostics(fmt.Errorf("%w: restarted more than %d times within %s; automatic restart disabled: %v",
			ErrPluginNotReady, policy.MaxRestartsPerWindow, policy.RestartWindow, err), s.stderrTail)
	}
	if shouldRestart && !s.restarting {
		s.restarting = true
		s.restartWG.Add(1)
		go s.restartAfterCrash(policy)
	}
}

// failWatch is the terminal state for a supervisor whose watcher died.
//
// The goroutine captured `client` when it started, and that snapshot cannot be
// written back unchecked: by the time the panic unwinds, Stop may have taken
// the plugin down or a restart may have installed a newer client with a healthy
// watcher of its own. The live supervisor is therefore re-read under the lock
// and the failure is recorded only while this goroutine's client is still the
// one the supervisor owns — the same discipline failPluginSupervision applies
// one layer up.
//
// The client is closed either way, before the lock is taken. A plugin left
// running after its watcher died is a third-party process holding the owner's
// granted capabilities with nobody supervising it, which is exactly the state
// this recovery exists to prevent.
//
// The state chosen is StateFailed, not StateCrashed, and no restart is
// scheduled. That is deliberate: a panic must not be able to drive the restart
// path at all, so it cannot reset or bypass the 5/60s crash-loop window
// (M-28). StateFailed is also terminal for a restartAfterCrash that is already
// in flight, whose every attempt re-checks for StateCrashed.
func (s *Supervisor) failWatch(client *Client, cause error) {
	if client != nil {
		closeCtx, cancel := context.WithTimeout(context.Background(), s.config.Client.CloseTimeout)
		_ = client.CloseContext(closeCtx)
		cancel()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client != nil && s.client != client {
		// A newer client already owns this supervisor; reporting it as failed
		// would present a healthy session as broken.
		return
	}
	if s.state == StateStopping || s.state == StateStopped {
		return
	}
	if s.client == client {
		// handleClientExit never got to its accounting, so do it here rather
		// than losing the diagnostics that explain the exit.
		if tail := client.StderrTail(); tail != "" {
			s.stderrTail = tail
		}
		s.dropped += client.DroppedEvents()
	}
	s.client = nil
	s.state = StateFailed
	s.lastErr = withDiagnostics(cause, s.stderrTail)
	if s.cancel != nil {
		s.cancel()
	}
	s.lifecycleCtx = nil
}

func (s *Supervisor) restartAfterCrash(policy RestartPolicy) {
	defer s.restartWG.Done()
	defer s.clearRestarting()
	// Deferred handlers run last-in-first-out, so the recovery reports first
	// and clearRestarting and restartWG.Done still run after it. Both take
	// s.mu, which is why every critical section below unlocks with defer: a
	// panic while holding the lock would otherwise deadlock this goroutine's
	// own cleanup and strand restartWG.Wait forever.
	defer recoverPluginGoroutine("plugin_supervisor_restart", func(err error) {
		s.failRestart(err)
	})
	backoff := policy.InitialBackoff
	for attempt := 0; attempt < policy.MaxAttempts; attempt++ {
		if !s.restartStillWanted() {
			return
		}
		if attempt > 0 {
			timer := time.NewTimer(backoff)
			select {
			case <-timer.C:
			case <-s.stopSignal():
				timer.Stop()
				return
			}
			backoff *= 2
			if backoff > policy.MaxBackoff {
				backoff = policy.MaxBackoff
			}
		}
		if err := s.Start(context.Background()); err == nil {
			return
		}
	}
	s.exhaustRestarts()
}

// restartStillWanted reports whether the supervisor is still in the crashed
// state this goroutine was spawned to recover from.
func (s *Supervisor) restartStillWanted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	fireFaultHook(faultSupervisorRestart)
	return s.state == StateCrashed
}

func (s *Supervisor) exhaustRestarts() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == StateCrashed {
		s.state = StateFailed
	}
}

func (s *Supervisor) clearRestarting() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.restarting = false
}

// failRestart is the terminal state for a restart goroutine that panicked.
//
// Nothing about the pre-panic snapshot is trusted: the supervisor is re-read
// under the lock, and a session that has since been stopped or has genuinely
// come back up keeps the state it earned. Only a supervisor still stuck in the
// crashed/starting limbo this goroutine was responsible for is failed.
//
// The lifecycle context is cancelled so a client that Start had already
// launched but not yet published — a live child process holding the owner's
// granted capabilities, reachable from no field — is torn down rather than
// orphaned.
func (s *Supervisor) failRestart(cause error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == StateStopping || s.state == StateStopped || s.state == StateRunning {
		return
	}
	s.state = StateFailed
	s.lastErr = withDiagnostics(cause, s.stderrTail)
	if s.cancel != nil {
		s.cancel()
	}
	s.lifecycleCtx = nil
}

// recordCrashLocked appends one crash to the sliding window and reports
// whether the circuit breaker has tripped. s.mu must be held.
func (s *Supervisor) recordCrashLocked(now time.Time, policy RestartPolicy) bool {
	window := policy.RestartWindow
	if window <= 0 {
		window = DefaultRestartWindow
	}
	limit := policy.MaxRestartsPerWindow
	if limit <= 0 {
		limit = DefaultMaxRestartsPerWindow
	}
	cutoff := now.Add(-window)
	kept := s.crashes[:0]
	for _, at := range s.crashes {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	s.crashes = append(kept, now)
	return len(s.crashes) > limit
}

// withDiagnostics appends the redacted stderr tail so a crash-looping plugin
// reports why it died instead of only "process exited".
func withDiagnostics(err error, tail string) error {
	if err == nil || strings.TrimSpace(tail) == "" {
		return err
	}
	return fmt.Errorf("%w; plugin stderr: %s", err, truncateDiagnostics(tail, 2048))
}

func truncateDiagnostics(value string, max int) string {
	runes := []rune(value)
	if max <= 0 || len(runes) <= max {
		return value
	}
	return "…" + string(runes[len(runes)-max:])
}

// DroppedEvents reports how many plugin events the host discarded because the
// consumer could not keep up, accumulated across restarts. Dropping is what
// keeps a legitimate burst from tearing the session down (L-23); this counter
// is what keeps that drop from being silent.
func (s *Supervisor) DroppedEvents() uint64 {
	s.mu.Lock()
	client := s.client
	dropped := s.dropped
	s.mu.Unlock()
	if client != nil {
		dropped += client.DroppedEvents()
	}
	return dropped
}

// StderrTail returns the last redacted diagnostics observed from the plugin
// process, including the output of a process that has already exited.
func (s *Supervisor) StderrTail() string {
	s.mu.Lock()
	client := s.client
	tail := s.stderrTail
	s.mu.Unlock()
	if client != nil {
		if live := client.StderrTail(); live != "" {
			return live
		}
	}
	return tail
}

func (s *Supervisor) stopSignal() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lifecycleCtx == nil {
		return nil
	}
	return s.lifecycleCtx.Done()
}

func contextDone(ctx context.Context) bool {
	if ctx == nil {
		return true
	}
	select {
	case <-ctx.Done():
		return true
	default:
		return false
	}
}

func (s *Supervisor) setFailure(err error) {
	s.mu.Lock()
	s.state = StateFailed
	s.lastErr = err
	s.mu.Unlock()
}

func (s *Supervisor) grantedCapabilities() []CapabilityGrant {
	grants := make([]CapabilityGrant, 0, len(s.config.EffectiveGrants))
	for _, grant := range s.config.EffectiveGrants {
		grants = append(grants, CapabilityGrant{
			Capability: grant.Capability,
			Scope:      append(json.RawMessage(nil), grant.Scope...),
			ExpiresAt:  grant.ExpiresAt,
		})
	}
	return grants
}

func (s *Supervisor) State() (LifecycleState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state, s.lastErr
}

func (s *Supervisor) Manifest() Manifest {
	s.mu.Lock()
	defer s.mu.Unlock()
	manifest := s.config.Manifest
	manifest.SupportedOS = append([]string(nil), manifest.SupportedOS...)
	manifest.SupportedArch = append([]string(nil), manifest.SupportedArch...)
	manifest.Tools = append([]ToolDeclaration(nil), manifest.Tools...)
	manifest.EventSources = append([]EventSource(nil), manifest.EventSources...)
	manifest.Permissions = append([]PermissionDeclaration(nil), manifest.Permissions...)
	return manifest
}

func (s *Supervisor) Events() (<-chan Envelope, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client == nil || s.state != StateRunning {
		return nil, ErrPluginNotReady
	}
	return s.client.Events(), nil
}

// InvokeTool performs manifest lookup and capability authorization on every
// invocation, even if a previous invocation was authorized.
func (s *Supervisor) InvokeTool(ctx context.Context, params ToolInvokeParams) (ToolInvokeResult, error) {
	if ctx == nil {
		return ToolInvokeResult{}, fmt.Errorf("%w: nil context", ErrPluginNotReady)
	}
	if err := ctx.Err(); err != nil {
		return ToolInvokeResult{}, err
	}
	tool, ok := s.findTool(params.ToolID)
	if !ok {
		return ToolInvokeResult{}, fmt.Errorf("%w: unknown tool %q", ErrInvalidManifest, params.ToolID)
	}
	if err := authorizeTool(ctx, s.config.Manifest.ID, tool, s.config.Manifest.Permissions, s.config.Authorizer); err != nil {
		return ToolInvokeResult{}, err
	}
	s.mu.Lock()
	client := s.client
	state := s.state
	s.mu.Unlock()
	if client == nil || state != StateRunning {
		return ToolInvokeResult{}, ErrPluginNotReady
	}
	result, err := client.InvokeTool(ctx, params)
	if err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		// The client has already sent request.cancel for this invocation. A
		// handler is untrusted and may ignore it, so the plugin gets one
		// bounded grace period to prove it is still serving the protocol.
		// Killing the process group is the escalation, not the first response:
		// a user cancelling a turn must not take a healthy plugin down.
		if !s.respondsAfterCancel(client) {
			stopCtx, cancel := context.WithTimeout(context.Background(), s.config.Client.CloseTimeout)
			_ = s.Stop(stopCtx)
			cancel()
		}
	}
	return result, err
}

// respondsAfterCancel probes the plugin within the cancel grace period. A
// plugin that answers is left running with its state untouched.
func (s *Supervisor) respondsAfterCancel(client *Client) bool {
	grace := s.config.CancelGrace
	if grace <= 0 {
		grace = DefaultCancelGrace
	}
	ctx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	if _, err := client.Health(ctx, HealthParams{}); err != nil {
		return false
	}
	return true
}

func (s *Supervisor) findTool(id string) (ToolDeclaration, bool) {
	for _, tool := range s.config.Manifest.Tools {
		if tool.ID == id {
			return tool, true
		}
	}
	return ToolDeclaration{}, false
}

func authorizeTool(ctx context.Context, pluginID string, tool ToolDeclaration, permissions []PermissionDeclaration, authorizer Authorizer) error {
	permissionByCapability := make(map[string]PermissionDeclaration, len(permissions))
	for _, permission := range permissions {
		permissionByCapability[string(domain.NormalizeCapabilityName(permission.Capability))] = permission
	}
	for _, rawCapability := range tool.Permissions {
		capability := domain.NormalizeCapabilityName(rawCapability)
		permission, declared := permissionByCapability[string(capability)]
		if !declared {
			return fmt.Errorf("%w: plugin did not declare permission %q", ErrPermissionDenied, capability)
		}
		if authorizer == nil {
			return fmt.Errorf("%w: no authorizer configured for %q", ErrPermissionDenied, capability)
		}
		decision, err := authorizer.Authorize(ctx, AuthorizationRequest{
			PluginID: pluginID, ToolID: tool.ID, Capability: string(capability), Scope: permission.Scope,
			Action: "plugin.tool.invoke:" + tool.ID, Risk: tool.Risk,
		})
		if err != nil {
			return fmt.Errorf("%w: authorize %q: %v", ErrPermissionDenied, capability, err)
		}
		if !decision.Allowed {
			reason := strings.TrimSpace(decision.Reason)
			if reason == "" {
				reason = "authorizer denied the capability"
			}
			return fmt.Errorf("%w: %s", ErrPermissionDenied, reason)
		}
	}
	return nil
}

// Health asks the process to prove it is still responsive.
func (s *Supervisor) Health(ctx context.Context) (HealthResult, error) {
	s.mu.Lock()
	client := s.client
	state := s.state
	s.mu.Unlock()
	if client == nil || state != StateRunning {
		return HealthResult{}, ErrPluginNotReady
	}
	return client.Health(ctx, HealthParams{})
}

// Stop attempts the protocol shutdown first and always closes/kills the
// process afterwards. A plugin cannot keep the desktop application alive.
func (s *Supervisor) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	if s.state == StateStopped {
		s.mu.Unlock()
		return nil
	}
	s.state = StateStopping
	cancel := s.cancel
	client := s.client
	s.mu.Unlock()
	var shutdownErr error
	if client != nil && !isClosed(client.Done()) {
		shutdownErr = client.Shutdown(ctx)
	}
	if client != nil {
		closeCtx, cancel := context.WithTimeout(ctx, s.config.Client.CloseTimeout)
		_ = client.CloseContext(closeCtx)
		cancel()
	}
	if cancel != nil {
		cancel()
	}
	s.mu.Lock()
	s.client = nil
	s.lifecycleCtx = nil
	s.state = StateStopped
	if shutdownErr != nil && !isIgnorableStopError(shutdownErr) {
		s.lastErr = shutdownErr
	} else {
		s.lastErr = nil
	}
	s.mu.Unlock()
	return shutdownErr
}

// isIgnorableStopError reports whether a shutdown failure is one of the two
// benign outcomes of stopping a plugin that had already gone away.
//
// Matched with errors.Is, not by message substring: every producer of these
// two sentinels wraps them with %w, and substring matching is wrong in both
// directions.  It misses a sentinel whose message the wrapper rephrased, and —
// worse — it matches any unrelated error that merely quotes the sentinel's
// text, which is exactly what happens when a genuine failure is reported as
// `... after "plugin: process exited"`.  A real error must not be silently
// dropped from Supervisor.lastErr because of the words it happens to contain.
func isIgnorableStopError(err error) bool {
	return err == nil || errors.Is(err, ErrPluginExited) || errors.Is(err, ErrPluginNotReady)
}

func (s *Supervisor) Restart(ctx context.Context) error {
	if err := s.Stop(ctx); err != nil && ctx != nil && ctx.Err() != nil {
		return err
	}
	return s.Start(ctx)
}

// ValidateToolArguments checks the only property the host can enforce without
// shipping a full JSON Schema interpreter: arguments must be valid JSON. The
// authoritative schema remains in the manifest and can be validated by a
// future schema adapter.
func ValidateToolArguments(args json.RawMessage) error {
	if len(args) == 0 {
		return nil
	}
	if !json.Valid(args) {
		return fmt.Errorf("%w: invalid JSON arguments", ErrInvalidProtocol)
	}
	return nil
}
