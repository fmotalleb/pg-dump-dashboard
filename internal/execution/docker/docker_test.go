package docker_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/fmotalleb/pg-dump-dashboard/internal/execution"
	"github.com/fmotalleb/pg-dump-dashboard/internal/execution/docker"
	"github.com/fmotalleb/pg-dump-dashboard/internal/execution/fake"
)

func TestTranslateBuildsDockerExecArguments(t *testing.T) {
	t.Parallel()

	translator := docker.Translator{Config: docker.Config{
		Container:  "pg-primary",
		DockerPath: "docker",
	}}

	cmd, err := translator.Translate(execution.Command{
		Path:  "psql",
		Args:  []string{"--tuples-only", "--file", "-"},
		Env:   []string{"PGHOST=db", "PGPASSWORD=secret-value"},
		Stdin: "SELECT 1;\n",
	})
	if err != nil {
		t.Fatalf("translate: %v", err)
	}

	if cmd.Path != "docker" {
		t.Fatalf("path = %q, want docker", cmd.Path)
	}
	want := []string{
		"exec", "--interactive",
		"--env", "PGHOST",
		"--env", "PGPASSWORD",
		"pg-primary", "psql", "--tuples-only", "--file", "-",
	}
	if strings.Join(cmd.Args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args = %v, want %v", cmd.Args, want)
	}
	if cmd.Stdin != "SELECT 1;\n" {
		t.Errorf("stdin was not forwarded: %q", cmd.Stdin)
	}
	// Values are carried by the docker CLI environment, never by argv.
	if strings.Contains(strings.Join(cmd.Args, " "), "secret-value") {
		t.Fatalf("a credential leaked into the argument vector: %v", cmd.Args)
	}
	joinedEnv := strings.Join(cmd.Env, " ")
	for _, wantEnv := range []string{"PGHOST=db", "PGPASSWORD=secret-value"} {
		if !strings.Contains(joinedEnv, wantEnv) {
			t.Errorf("docker CLI environment is missing %q: %v", wantEnv, cmd.Env)
		}
	}
	if strings.Contains(cmd.Describe(), "secret-value") {
		t.Fatalf("Describe leaked the credential: %s", cmd.Describe())
	}
}

func TestTranslateOmitsInteractiveWithoutStdin(t *testing.T) {
	t.Parallel()

	translator := docker.Translator{Config: docker.Config{Container: "pg", DockerPath: "docker"}}
	cmd, err := translator.Translate(execution.Command{Path: "pg_dump", Args: []string{"appdb"}})
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	for _, arg := range cmd.Args {
		if arg == "--interactive" {
			t.Fatalf("--interactive must only be used when stdin is needed: %v", cmd.Args)
		}
	}
}

func TestTranslateRunsAsConfiguredUser(t *testing.T) {
	t.Parallel()

	translator := docker.Translator{Config: docker.Config{Container: "pg", DockerPath: "docker", PgUser: "postgres"}}
	cmd, err := translator.Translate(execution.Command{Path: "psql"})
	if err != nil {
		t.Fatalf("translate: %v", err)
	}

	joined := strings.Join(cmd.Args, " ")
	if !strings.Contains(joined, "--user postgres") {
		t.Fatalf("expected --user postgres in %q", joined)
	}
}

func TestTranslateValidatesContainer(t *testing.T) {
	t.Parallel()

	translator := docker.Translator{Config: docker.Config{DockerPath: "docker"}}
	if _, err := translator.Translate(execution.Command{Path: "psql"}); err == nil {
		t.Fatal("an empty container must be rejected: guessing the target is unsafe")
	}

	bad := docker.Translator{Config: docker.Config{Container: "pg; rm -rf /", DockerPath: "docker"}}
	if _, err := bad.Translate(execution.Command{Path: "psql"}); err == nil {
		t.Fatal("an invalid container name must be rejected")
	}
}

func TestRunnerDelegatesToBaseRunner(t *testing.T) {
	t.Parallel()

	base := fake.New(fake.Response{Stdout: "1\n"})
	runner, err := docker.NewRunner(base, docker.Config{Container: "pg-primary", DockerPath: "docker"})
	if err != nil {
		t.Fatalf("new runner: %v", err)
	}

	if runner.Name() != "docker" {
		t.Errorf("name = %q", runner.Name())
	}

	result, err := runner.Run(context.Background(), execution.Command{
		Path: "psql",
		Args: []string{"--tuples-only"},
		Env:  []string{"PGPASSWORD=hidden"},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if string(result.Stdout) != "1\n" {
		t.Errorf("stdout = %q", result.Stdout)
	}

	rewritten := base.LastCommand()
	if rewritten.Path != "docker" {
		t.Fatalf("base runner saw %q, want the docker CLI", rewritten.Path)
	}
	if rewritten.Args[0] != "exec" {
		t.Fatalf("expected docker exec, got %v", rewritten.Args)
	}
}

func TestRunnerStartPropagatesProcess(t *testing.T) {
	t.Parallel()

	base := fake.New(fake.Response{Stream: "dump-bytes"})
	runner, err := docker.NewRunner(base, docker.Config{Container: "pg-primary", DockerPath: "docker"})
	if err != nil {
		t.Fatalf("new runner: %v", err)
	}

	proc, err := runner.Start(context.Background(), execution.Command{Path: "pg_dump", Args: []string{"appdb"}})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := proc.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
}

func TestNewRunnerRejectsBadConfig(t *testing.T) {
	t.Parallel()

	if _, err := docker.NewRunner(fake.New(), docker.Config{DockerPath: "docker"}); err == nil {
		t.Fatal("expected validation failure for a missing container")
	}
	if _, err := docker.NewRunner(fake.New(), docker.Config{Container: "pg"}); err == nil {
		t.Fatal("expected validation failure for a missing docker path")
	}
}

func TestStartErrorIsWrapped(t *testing.T) {
	t.Parallel()

	base := fake.New()
	base.StartErr = errors.New("daemon unreachable")
	runner, err := docker.NewRunner(base, docker.Config{Container: "pg", DockerPath: "docker"})
	if err != nil {
		t.Fatalf("new runner: %v", err)
	}
	if _, err := runner.Start(context.Background(), execution.Command{Path: "pg_dump"}); err == nil {
		t.Fatal("expected the start error to be propagated")
	}
}
