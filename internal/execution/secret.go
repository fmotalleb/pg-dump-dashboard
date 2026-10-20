package execution

import (
	"encoding/json"
	"strings"
)

// Redacted replaces every secret rendered by fmt. It keeps passwords out of
// logs, error messages and JSON responses even if a value is printed by
// accident.
const Redacted = "[redacted]"

// Secret is a string that refuses to reveal itself through fmt, JSON or string
// conversion. Call Reveal only at the point where the value is handed to a
// subprocess environment.
type Secret string

// String implements fmt.Stringer.
func (Secret) String() string { return Redacted }

// GoString implements fmt.GoStringer, covering %#v.
func (Secret) GoString() string { return Redacted }

// Reveal returns the underlying value. Use it only for subprocess environments.
func (s Secret) Reveal() string { return string(s) }

// IsZero reports whether the secret is empty.
func (s Secret) IsZero() bool { return s == "" }

// MarshalJSON prevents accidental serialisation of the secret.
func (Secret) MarshalJSON() ([]byte, error) { return json.Marshal(Redacted) }

// UnmarshalJSON accepts a plain JSON string for request decoding.
func (s *Secret) UnmarshalJSON(data []byte) error {
	var v string
	if err := json.Unmarshal(data, &v); err != nil {
		return err //nolint:wrapcheck // straight re-export of the decoder error.
	}
	*s = Secret(v)
	return nil
}

// RedactSecrets replaces every occurrence of the given secrets in msg. It is a
// defence-in-depth measure for text produced by external tools.
func RedactSecrets(msg string, secrets ...Secret) string {
	for _, s := range secrets {
		value := s.Reveal()
		if len(value) < 3 {
			continue
		}
		msg = strings.ReplaceAll(msg, value, Redacted)
	}
	return msg
}
