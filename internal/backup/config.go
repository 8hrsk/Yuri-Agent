package backup

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// SanitizeConfigMetadata removes keyring references and secret-shaped fields
// from JSON config metadata. Provider Binary fields are removed unless they
// pass the explicit ConfigMetadataOptions.AllowedBinaryRoots policy.
func SanitizeConfigMetadata(input []byte) ([]byte, error) {
	return SanitizeConfigMetadataWithOptions(input, ConfigMetadataOptions{}, DefaultMaxConfigBytes)
}

// SanitizeConfigMetadataWithOptions is the bounded form of
// SanitizeConfigMetadata used by Export.
func SanitizeConfigMetadataWithOptions(input []byte, options ConfigMetadataOptions, maxBytes int64) ([]byte, error) {
	if maxBytes < 1 || maxBytes > hardMaxConfigBytes || int64(len(input)) > maxBytes {
		return nil, fmt.Errorf("%w: config metadata", ErrSizeLimit)
	}
	if len(input) == 0 {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("%w: invalid config metadata: %v", ErrInvalidArchive, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("%w: config metadata has trailing JSON", ErrInvalidArchive)
		}
		return nil, fmt.Errorf("%w: invalid trailing config metadata: %v", ErrInvalidArchive, err)
	}
	cleaned, err := sanitizeJSON(value, options, maxBytes)
	if err != nil {
		return nil, err
	}
	output, err := json.Marshal(cleaned)
	if err != nil {
		return nil, fmt.Errorf("encode sanitized config metadata: %w", err)
	}
	if int64(len(output)) > maxBytes {
		return nil, fmt.Errorf("%w: sanitized config metadata", ErrSizeLimit)
	}
	return output, nil
}

// ValidateSanitizedConfigMetadata rejects archives that contain secret-shaped
// fields rather than silently making restore semantics depend on a cleanup.
func ValidateSanitizedConfigMetadata(input []byte, maxBytes int64) error {
	return ValidateSanitizedConfigMetadataWithOptions(input, ConfigMetadataOptions{}, maxBytes)
}

// ValidateSanitizedConfigMetadataWithOptions is the policy-aware form used by
// Restore when a caller intentionally allowed a verified provider binary.
func ValidateSanitizedConfigMetadataWithOptions(input []byte, options ConfigMetadataOptions, maxBytes int64) error {
	if maxBytes < 1 || maxBytes > hardMaxConfigBytes || int64(len(input)) > maxBytes {
		return fmt.Errorf("%w: config metadata", ErrSizeLimit)
	}
	if len(input) == 0 {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("%w: invalid config metadata: %v", ErrInvalidArchive, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("%w: config metadata has trailing JSON", ErrInvalidArchive)
		}
		return fmt.Errorf("%w: invalid trailing config metadata: %v", ErrInvalidArchive, err)
	}
	if err := rejectSensitiveJSON(value, options); err != nil {
		return err
	}
	return nil
}

func sanitizeJSON(value any, options ConfigMetadataOptions, maxBytes int64) (any, error) {
	switch value := value.(type) {
	case map[string]any:
		cleaned := make(map[string]any, len(value))
		for key, child := range value {
			normalized := normalizeFieldName(key)
			if sensitiveField(normalized) {
				continue
			}
			if normalized == "binary" {
				text, ok := child.(string)
				if !ok || !safeBinaryPath(text, options.AllowedBinaryRoots) {
					continue
				}
			}
			value, err := sanitizeJSON(child, options, maxBytes)
			if err != nil {
				return nil, err
			}
			cleaned[key] = value
		}
		return cleaned, nil
	case []any:
		cleaned := make([]any, 0, len(value))
		for _, child := range value {
			value, err := sanitizeJSON(child, options, maxBytes)
			if err != nil {
				return nil, err
			}
			cleaned = append(cleaned, value)
		}
		return cleaned, nil
	default:
		return value, nil
	}
}

func rejectSensitiveJSON(value any, options ConfigMetadataOptions) error {
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			normalized := normalizeFieldName(key)
			if sensitiveField(normalized) {
				return fmt.Errorf("%w: unsafe config field %q", ErrInvalidArchive, key)
			}
			if normalized == "binary" {
				text, ok := child.(string)
				if !ok || !safeBinaryPath(text, options.AllowedBinaryRoots) {
					return fmt.Errorf("%w: unsafe config field %q", ErrInvalidArchive, key)
				}
			}
			if err := rejectSensitiveJSON(child, options); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range value {
			if err := rejectSensitiveJSON(child, options); err != nil {
				return err
			}
		}
	}
	return nil
}

func normalizeFieldName(value string) string {
	value = strings.ToLower(value)
	var builder strings.Builder
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') {
			builder.WriteRune(character)
		}
	}
	return builder.String()
}

func sensitiveField(normalized string) bool {
	switch normalized {
	case "credentialref", "credential", "apikey", "apisecret", "secret", "password",
		"passphrase", "token", "accesstoken", "refreshtoken", "clientsecret",
		"authorization", "bearer", "privatekey", "secretkey":
		return true
	default:
		return false
	}
}

func safeBinaryPath(value string, roots []string) bool {
	if len(roots) == 0 || value == "" || len(value) > maxEntryNameBytes || strings.ContainsRune(value, '\x00') || !filepath.IsAbs(value) {
		return false
	}
	cleaned := filepath.Clean(value)
	info, err := os.Lstat(cleaned)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 || info.Mode()&0o111 == 0 {
		return false
	}
	resolved, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		return false
	}
	for _, root := range roots {
		if root == "" || !filepath.IsAbs(root) {
			continue
		}
		rootInfo, rootErr := os.Lstat(root)
		if rootErr != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
			continue
		}
		resolvedRoot, rootErr := filepath.EvalSymlinks(root)
		if rootErr != nil {
			continue
		}
		relative, relErr := filepath.Rel(filepath.Clean(resolvedRoot), filepath.Clean(resolved))
		if relErr == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative) {
			return true
		}
	}
	return false
}
