package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"slices"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/build"
)

const rollbackUsage = `Usage: whiteroom rollback OUT [RELEASE]

Publish the release RELEASE of the output directory OUT, a name that whiteroom
build or whiteroom serve admin has made, instead of the release that is
published: OUT/current points to it from then on, and a whiteroom serve public
that serves OUT serves it from its next request. A build keeps the release that
it replaces, so that a rollback undoes the build, and another rollback undoes the
rollback.

Without RELEASE, it is the one other release that OUT holds. The command does not
open a data directory, and reads no config file.
`

// rollbackWarning is what whiteroom rollback tells on stderr when it has changed the published release.
const rollbackWarning = "whiteroom rollback: warning: a running whiteroom serve admin replaces this release in its next build; " +
	"to keep it, roll back through the admin API, POST /api/releases/{name}/rollback, which stops the builds until they " +
	"are resumed\n"

// runRollback runs whiteroom rollback with args, those after rollback.
func runRollback(_ context.Context, args []string, e env) int {
	flags := flag.NewFlagSet("whiteroom rollback", flag.ContinueOnError)
	flags.SetOutput(e.stderr)
	flags.Usage = func() {
		printf(flags.Output(), "%s", rollbackUsage)
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	switch flags.NArg() {
	case 0:
		return usageError(flags, "no output directory: give OUT")
	case 1, 2:
	default:
		return usageError(flags, "too many arguments: give OUT and at most one RELEASE")
	}
	out := flags.Arg(0)
	fail := func(err error) int {
		printf(e.stderr, "%s: %v\n", flags.Name(), err)
		return 1
	}

	before, err := build.ListReleases(out)
	if err != nil {
		return fail(err)
	}
	release := flags.Arg(1)
	if flags.NArg() == 1 {
		if release, err = otherRelease(before); err != nil {
			return fail(err)
		}
	}
	changed, err := build.Rollback(out, release)
	if err != nil {
		return fail(err)
	}
	switch {
	case !changed:
		printf(e.stdout, "release %s in %s is already published\n", release, out)
		return 0
	case before.Current == "":
		printf(e.stdout, "rolled back to release %s in %s\n", release, out)
	default:
		printf(e.stdout, "rolled back to release %s in %s (was %s)\n", release, out, before.Current)
	}
	printf(e.stderr, "%s", rollbackWarning)
	return 0
}

// otherRelease returns the release to roll back to when the command does not name it: the one other release of rel, if
// there is one, and the published one is known. Else it is an error, which names the releases that there are.
func otherRelease(rel build.Releases) (string, error) {
	others := slices.DeleteFunc(slices.Clone(rel.Names), func(name string) bool { return name == rel.Current })
	slices.Sort(others)
	switch {
	case len(others) == 0:
		return "", errors.New("no release to roll back to: no other release is left")
	case rel.Current == "":
		return "", fmt.Errorf("no release is published, so there is no release to go back to: give RELEASE, one of %s",
			strings.Join(others, ", "))
	case len(others) > 1:
		return "", fmt.Errorf("%d releases are left besides the published one: give RELEASE, one of %s", len(others),
			strings.Join(others, ", "))
	}
	return others[0], nil
}
