package keyring

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type memoryBackend struct {
	values map[string]string
	err    error
}

func (backend *memoryBackend) Set(service, account, secret string) error {
	if backend.err != nil {
		return backend.err
	}
	backend.values[service+":"+account] = secret
	return nil
}

func (backend *memoryBackend) Get(service, account string) (string, error) {
	if backend.err != nil {
		return "", backend.err
	}
	value, found := backend.values[service+":"+account]
	if !found {
		return "", ErrNotFound
	}
	return value, nil
}

func (backend *memoryBackend) Delete(service, account string) error {
	if backend.err != nil {
		return backend.err
	}
	delete(backend.values, service+":"+account)
	return nil
}

func TestRoundTripAndCredentialClosure(t *testing.T) {
	backend := &memoryBackend{values: make(map[string]string)}
	store, err := NewWithBackend("test.yuri", backend)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.Put(ctx, "provider.main", "secret"); err != nil {
		t.Fatal(err)
	}
	got, err := store.Credential("provider.main")(ctx)
	if err != nil || got != "secret" {
		t.Fatalf("Get() = %q, %v", got, err)
	}
	if err := store.Delete(ctx, "provider.main"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, "provider.main"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestRejectsInvalidReferenceAndRedactsBackendError(t *testing.T) {
	store, err := NewWithBackend("test.yuri", &memoryBackend{values: make(map[string]string), err: errors.New("failed with secret-value")})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(context.Background(), "../bad", "value"); err == nil {
		t.Fatal("expected invalid reference")
	}
	_, err = store.Get(context.Background(), "provider.main")
	if err == nil || strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("backend error was not redacted: %v", err)
	}
}
