package config

import "log/slog"

const redacted = "***"

// Secret is a string that prints as "***" in fmt, slog and JSON.
type Secret string

// Reveal returns the raw value.
func (s Secret) Reveal() string { return string(s) }

func (Secret) String() string { return redacted }

// GoString redacts %#v.
func (Secret) GoString() string { return redacted }

// LogValue implements [slog.LogValuer].
func (Secret) LogValue() slog.Value { return slog.StringValue(redacted) }

// MarshalJSON implements [encoding/json.Marshaler].
func (Secret) MarshalJSON() ([]byte, error) { return []byte(`"` + redacted + `"`), nil }
