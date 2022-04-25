package version

import (
	"fmt"
	"log"
	"strings"

	"github.com/hashicorp/go-version"
)

// consulVersionConstraint is the compatible version constraint
// between ESM with Consul.
const consulVersionConstraint = ">= 1.4.1"

var (
	Name string = "consul-esm"

	// The git commit that was compiled. These will be filled in by the
	// compiler.
	GitCommit   string
	GitDescribe string

	// Version is the main version number that is being run at the moment.
	// Note: our current release process does a pattern match on this variable.
	Version = "0.6.0"

	// VersionPrerelease is a pre-release marker for the version. If this is ""
	// (empty string) then it means that it is a final release. Otherwise, this
	// is a pre-release such as "dev" (in development), "beta", "rc1", etc.
	VersionPrerelease = ""

	consulConstraints version.Constraints
)

func init() {
	vc, err := version.NewConstraint(consulVersionConstraint)
	if err != nil {
		log.Fatalf("invalid Consul version constraint %q: %s",
			consulVersionConstraint, err)
	}
	consulConstraints = vc
}

// GetHumanVersion composes the parts of the version in a way that's suitable
// for displaying to humans.
func GetHumanVersion() string {
	version := fmt.Sprintf("%s v%s", Name, Version)

	if VersionPrerelease != "" {
		version += fmt.Sprintf("-%s", VersionPrerelease)
	}
	if GitCommit != "" {
		version += fmt.Sprintf(" (%s)", GitCommit)
	}

	// Strip off any single quotes added by the git information.
	return strings.Replace(version, "'", "", -1)
}

// GetConsulVersionConstraint returns the version constraint for Consul
// that ESM is compatible with.
func GetConsulVersionConstraint() string {
	return consulVersionConstraint
}

// CheckConsulVersions checks for the compatibility of the Consul versions.
// Valid SemVer is expected, and will consider any invalid versions to be
// incompatible.
func CheckConsulVersions(versions []string) error {
	if len(versions) == 0 {
		return fmt.Errorf("no Consul versions")
	}

	var unsupported []string
	for _, s := range versions {
		// Any prelease information is trimmed to simplify constraint comparision
		// that will automatically return false for versions with prereleases.
		// This is a spec for go-version constraints checking.
		trimmed := strings.SplitN(s, "-", 2)[0]
		v, err := version.NewSemver(trimmed)
		if err != nil {
			unsupported = append(unsupported, s)
		}

		if ok := consulConstraints.Check(v); !ok {
			unsupported = append(unsupported, s)
		}
	}

	if len(unsupported) != 0 {
		return NewConsulVersionError(unsupported)
	}

	return nil
}

// NewConsulVersionError returns an error detailing the version compatibility
// issue between ESM and Consul servers.
func NewConsulVersionError(consulVersions []string) error {
	versions := strings.Join(consulVersions, ", ")
	return fmt.Errorf(versionErrorTmpl, Version, versions)
}

const versionErrorTmpl = `
Consul ESM version %s is incompatible with the running versions of the local
Consul agent or Consul servers (%s), please refer to the documentation
to safely upgrade Consul or change to a version of ESM that is compatible.

https://www.consul.io/docs/upgrading.html
`
