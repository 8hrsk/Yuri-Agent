// Package slowmode provides provider-neutral admission control for inference
// requests that share a remote quota scope.
//
// A Coordinator accounts for rolling request and input-token windows, a
// persistent Pacific-date request ledger, active concurrency, queue priority,
// and provider rate-limit feedback. Provider adapters remain responsible for
// estimating or counting request tokens and for classifying provider errors.
//
// A zero RPM, TPM, or RPD limit disables that dimension. Integrations must not
// use zero to mean an unknown remote limit: they should resolve unknown limits
// to an explicitly conservative local profile before constructing a
// Coordinator.
package slowmode
