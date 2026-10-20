package execution_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/fmotalleb/pg-dump-dashboard/internal/execution"
)

func TestCommandDescribeNeverContainsEnvironment(t *testing.T) {
	t.Parallel()

	cmd := execution.Command{
		Path: "psql",
		Args: []string{"--no-password", "--file", "-"},
		Env:  []string{"PGPASSWORD=super-secret-value", "PGHOST=db"},
	}

	described := cmd.Describe()
	if strings.Contains(described, "super-secret-value") {
		t.Fatalf("Describe leaked the password: %s", described)
	}
	if !strings.HasPrefix(described, "psql --no-password") {
		t.Fatalf("unexpected description: %s", described)
	}
}

func TestSecretIsNeverFormatted(t *testing.T) {
	t.Parallel()

	secret := execution.Secret("hunter2-not-visible")

	var verb strings.Builder
	fmt.Fprintf(&verb, "%s|%v", secret, secret)

	renderings := []string{
		fmt.Sprint(secret),
		verb.String(),
		fmt.Sprintf("%#v", secret),
		fmt.Sprintf("%+v", struct{ P execution.Secret }{secret}),
	}
	for _, rendering := range renderings {
		if strings.Contains(rendering, "hunter2-not-visible") {
			t.Fatalf("secret leaked through formatting: %q", rendering)
		}
	}

	encoded, err := json.Marshal(struct{ P execution.Secret }{secret})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), "hunter2-not-visible") {
		t.Fatalf("secret leaked through JSON: %s", encoded)
	}

	var decoded struct{ P execution.Secret }
	if err := json.Unmarshal([]byte(`{"P":"plain"}`), &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.P.Reveal() != "plain" {
		t.Fatalf("unmarshal produced %q", decoded.P.Reveal())
	}
}

func TestRedactEnvRemovesSecrets(t *testing.T) {
	t.Parallel()

	env := []string{
		"PGPASSWORD=topsecret",
		"PGHOST=db.example.com",
		"MY_TOKEN=abc123token",
	}
	msg := "psql: error: authentication failed for topsecret with abc123token"

	redacted := execution.RedactEnv(msg, env)
	if strings.Contains(redacted, "topsecret") || strings.Contains(redacted, "abc123token") {
		t.Fatalf("secrets survived redaction: %s", redacted)
	}
	if !strings.Contains(redacted, execution.Redacted) {
		t.Fatalf("expected redaction marker in %q", redacted)
	}
	if !strings.Contains(redacted, "authentication failed") {
		t.Fatalf("redaction destroyed the diagnostic: %s", redacted)
	}
}

func TestSalientLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "prefers the error line",
			input: "pg_dump: last built-in OID is 16383\npg_dump: reading schemas\npg_dump: error: connection lost\npg_dump: cleaning up",
			want:  "pg_dump: error: connection lost",
		},
		{name: "falls back to the last line", input: "one\ntwo\n\n", want: "two"},
		{name: "single line", input: "boom\n", want: "boom"},
		{name: "empty", input: "  \n\n", want: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := execution.SalientLine(tc.input); got != tc.want {
				t.Fatalf("SalientLine() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCommandErrorClassifiesFailures(t *testing.T) {
	t.Parallel()

	cmd := execution.Command{Path: "psql", Env: []string{"PGPASSWORD=hunter2secret"}}

	exitErr := &exec.ExitError{}
	typed := execution.CommandError(context.Background(), cmd, "FATAL: password authentication failed for hunter2secret", exitErr)
	if !errors.As(typed, new(*execution.ExitError)) {
		t.Fatalf("expected an *ExitError, got %T", typed)
	}
	if strings.Contains(typed.Error(), "hunter2secret") {
		t.Fatalf("error message leaked the password: %v", typed)
	}

	missing := execution.CommandError(context.Background(), cmd, "", exec.ErrNotFound)
	if !errors.Is(missing, execution.ErrBinaryNotFound) {
		t.Fatalf("expected ErrBinaryNotFound, got %v", missing)
	}
}

func TestValidContainerName(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"postgres", "pg-1", "my_db.main", "0abc"} {
		if err := execution.ValidContainerName(name); err != nil {
			t.Errorf("%q should be accepted: %v", name, err)
		}
	}
	for _, name := range []string{"", "-flag", "with space", "quote'", "a/b", strings.Repeat("a", 129)} {
		if err := execution.ValidContainerName(name); err == nil {
			t.Errorf("%q should be rejected", name)
		}
	}
}
