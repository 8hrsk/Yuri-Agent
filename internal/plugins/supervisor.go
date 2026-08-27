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
type RestartPolicy struct {
	MaxAttempts    int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
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
	<-client.Done()
	err := client.Err()
	s.mu.Lock()
	if s.client != client {
		s.mu.Unlock()
		return
	}
	if s.state == StateStopping || s.state == StateStopped {
		s.mu.Unlock()
		return
	}
	s.client = nil
	s.state = StateCrashed
	s.lastErr = err
	if s.cancel != nil {
		s.cancel()
	}
	s.lifecycleCtx = nil
	policy := s.config.Restart
	shouldRestart := policy.MaxAttempts > 0
	if shouldRestart && !s.restarting {
		s.restarting = true
		s.restartWG.Add(1)
		go s.restartAfterCrash(policy)
	}
	s.mu.Unlock()
}

func (s *Supervisor) restartAfterCrash(policy RestartPolicy) {
	defer s.restartWG.Done()
	defer func() {
		s.mu.Lock()
		s.restarting = false
		s.mu.Unlock()
	}()
	backoff := policy.InitialBackoff
	for attempt := 0; attempt < policy.MaxAttempts; attempt++ {
		s.mu.Lock()
		if s.state != StateCrashed {
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
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
	s.mu.Lock()
	if s.state == StateCrashed {
		s.state = StateFailed
	}
	s.mu.Unlock()
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
		// A handler is untrusted and may ignore cancellation. Terminate its
		// process group so one timed-out call cannot wedge the plugin runtime.
		stopCtx, cancel := context.WithTimeout(context.Background(), s.config.Client.CloseTimeout)
		_ = s.Stop(stopCtx)
		cancel()
	}
	return result, err
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

func isIgnorableStopError(err error) bool {
	return err == nil || strings.Contains(err.Error(), ErrPluginExited.Error()) || strings.Contains(err.Error(), ErrPluginNotReady.Error())
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
