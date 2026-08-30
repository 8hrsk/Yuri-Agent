// Package reflection contains the provider-neutral safety core for Yuri's
// background self-reflection.  It deliberately has no persistence, desktop,
// prompt-builder, or provider dependencies.  A run receives an immutable
// typed snapshot and returns a validated projection that an application
// service may persist atomically through its own adapter.
package reflection
