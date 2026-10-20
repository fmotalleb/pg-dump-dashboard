package execution

import (
	"fmt"
	"regexp"
)

// containerPattern matches the container names accepted by the Docker CLI.
var containerPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// ValidContainerName reports whether name is an explicit, unambiguous Docker
// container name or id.
func ValidContainerName(name string) error {
	if name == "" {
		return fmt.Errorf("docker container must be configured explicitly (--docker-container)")
	}
	if !containerPattern.MatchString(name) {
		return fmt.Errorf("invalid docker container name %q", name)
	}
	return nil
}

// Translator rewrites a command that is meaningful in the application's own
// environment into one that runs in a different execution target.
//
// It is the only place where backend-specific argument arrangement lives, so
// business logic never needs to branch on the execution context.
type Translator interface {
	// Translate returns the command that should actually be spawned.
	Translate(cmd Command) (Command, error)
}
