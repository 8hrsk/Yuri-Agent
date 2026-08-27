// Package keyring stores provider credentials in the operating system keyring.
// Only opaque references are persisted in Yuri configuration or SQLite.
package keyring

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	oskeyring "github.com/zalando/go-keyring"
)

const DefaultService = "ai.ordo.yuri"

var referencePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

var ErrNotFound = errors.New("credential not found")

type Backend interface {
	Set(service, account, secret string) error
	Get(service, account string) (string, error)
	Delete(service, account string) error
}

type systemBackend struct{}

func (systemBackend) Set(service, account, secret string) error {
	return oskeyring.Set(service, account, secret)
}

func (systemBackend) Get(service, account string) (string, error) {
	return oskeyring.Get(service, account)
}

func (systemBackend) Delete(service, account string) error {
	return oskeyring.Delete(service, account)
}

type Store struct {
	service string
	backend Backend
}

func New() *Store {
	return &Store{service: DefaultService, backend: systemBackend{}}
}

func NewWithBackend(service string, backend Backend) (*Store, error) {
	if service == "" {
		return nil, errors.New("keyring service is required")
	}
	if backend == nil {
		return nil, errors.New("keyring backend is required")
	}
	return &Store{service: service, backend: backend}, nil
}

func (store *Store) Put(ctx context.Context, reference, secret string) error {
	if err := validate(ctx, reference); err != nil {
		return err
	}
	if secret == "" {
		return errors.New("credential value is empty")
	}
	if err := store.backend.Set(store.service, reference, secret); err != nil {
		return fmt.Errorf("store credential %q: %w", reference, normalizeError(err))
	}
	return nil
}

func (store *Store) Get(ctx context.Context, reference string) (string, error) {
	if err := validate(ctx, reference); err != nil {
		return "", err
	}
	secret, err := store.backend.Get(store.service, reference)
	if err != nil {
		return "", fmt.Errorf("read credential %q: %w", reference, normalizeError(err))
	}
	if secret == "" {
		return "", fmt.Errorf("read credential %q: %w", reference, ErrNotFound)
	}
	return secret, nil
}

func (store *Store) Delete(ctx context.Context, reference string) error {
	if err := validate(ctx, reference); err != nil {
		return err
	}
	if err := store.backend.Delete(store.service, reference); err != nil {
		return fmt.Errorf("delete credential %q: %w", reference, normalizeError(err))
	}
	return nil
}

func (store *Store) Credential(reference string) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) { return store.Get(ctx, reference) }
}

func validate(ctx context.Context, reference string) error {
	if ctx == nil {
		return errors.New("credential operation: nil context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !referencePattern.MatchString(reference) {
		return errors.New("credential reference is invalid")
	}
	return nil
}

func normalizeError(err error) error {
	if errors.Is(err, oskeyring.ErrNotFound) || errors.Is(err, ErrNotFound) {
		return ErrNotFound
	}
	// Backend error strings can contain command output. Do not propagate them
	// because a platform backend may accidentally echo credential material.
	return errors.New("system keyring operation failed")
}
