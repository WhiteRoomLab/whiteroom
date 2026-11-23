package main

import (
	"context"
	"fmt"

	"github.com/WhiteRoomLab/whiteroom/internal/dbserver"
)

const checkUsage = `Usage: whiteroom-db check [flags]

Look for what is wrong in the data directory, and change nothing: the check of a
copy that "whiteroom backup" has made, before "whiteroom-db serve" is started on
it, and of a data directory whose daemon has stopped. It reads the files of the
storage engine and says what the engine would repair or not use when it opens the
directory (a log that ends in a torn record, a head that does not name the end of
the log, a checkpoint that cannot be used), and what else is damaged in them (the
log, and every object against its hash). It reads every original in files/, whose
data must have the hash of its name.

It prints each problem on a line of the standard output, which starts with the part
that it is in, engine or original. The last line tells how many problems there are,
or that the command stopped after 1000 of them. The exit code is 0 if there are
none, 1 if there are some or the check could not be made (what could not be made is
on the standard error), and 2 for a usage error. The output has no key or value of a
row, and no data of an object or an original.

The command writes nothing in the data directory: not in the files of the engine,
not the mark <DATA>/stopped, and not in files/ (it leaves the temporary files that
a daemon left there). It checks a data directory that it cannot write in.

No daemon may have the data directory open: stop "whiteroom-db serve" on it first.
If one has, the command waits for about two seconds, and then fails without
changing anything.

After a sync has failed on the data directory (in the daemon, or in a command), the
command refuses the directory until the machine has restarted, and says so on the
standard error: what the sync was to write may be in the page cache only. The
refusal changes nothing. A mark <DATA>/stopped of a failure from before the last
restart is left where it is: the daemon removes it when it starts.

The command does not look at the rows. To check them, with the index, the audit
logs and the originals that the documents refer to, start a daemon on the data
directory (on a copy, with its own directory of sockets), and run "whiteroom
doctor" on it.

With -config, the item data of the YAML file gives what -data leaves out; the file
may hold other items, which are checked and not used. Without it, no config file
is read.

Flags:
`

// maxCheckProblems is how many problems check prints before it stops.
const maxCheckProblems = 1000

// runCheck runs whiteroom-db check with args, those after check.
func runCheck(ctx context.Context, args []string, e env) int {
	const command = "whiteroom-db check"
	data, code, ok := setup(command, checkUsage, args, e)
	if !ok {
		return code
	}
	n := 0
	err := dbserver.Inspect(ctx, data, maintainOptions(e, command), func(p dbserver.Problem) bool {
		printf(e.stdout, "%s\n", p)
		n++
		return n < maxCheckProblems
	})
	if err != nil {
		return failed(ctx, e, command, err)
	}
	switch {
	case n >= maxCheckProblems:
		printf(e.stdout, "stopped after %s in %s\n", problems(n), data)
	case n == 0:
		printf(e.stdout, "no problems found in %s\n", data)
		return 0
	default:
		printf(e.stdout, "%s found in %s\n", problems(n), data)
	}
	return 1
}

func problems(n int) string {
	if n == 1 {
		return "1 problem"
	}
	return fmt.Sprintf("%d problems", n)
}
