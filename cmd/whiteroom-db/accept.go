package main

import (
	"context"
	"flag"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbserver"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
)

const acceptUsage = `Usage: whiteroom-db accept-break [flags] -log LOG -record N

Accept the break at record N of the audit log LOG (login or activity), once you
have looked into it. A break is a record whose number is that of the one before it
plus one and whose prev is not the hash of that record: "whiteroom doctor" reports
it as a record that does not follow the one before it. Take LOG and N from its
report:

  audit log "login": record 7 (event 31): prev is not the hash of record 6

The command changes no record and drops none. It leaves three traces: a row of the
table chains that ties the acceptance to the hash of record N and to the time of the
command, an event of the daemon in the log, and the line on the standard output
below. From then on "whiteroom doctor" checks the log again from record N, and tells
the break in a note, not as a problem. A later change to record N undoes the
acceptance, and a break at another record is still reported.

The command refuses, and changes nothing, a record that is not in the store (also
one before the start of the chain, which the deletion of old login records made),
the first record that the log is checked from (record 1, or the first that remains
after such a deletion), a record whose number is not that of the one before it plus
one (a gap), a record that follows the one before it, a break that is accepted
already, a chain whose rows cannot be read, and a data directory of an older table
schema than this program's: the first writer that connects through
"whiteroom-db serve" (such as "whiteroom serve admin") upgrades it.

It accepts a break at a record that can be read, and no more. An event that is no
record, which damage to the engine leaves, has no record number to name it by, and a
record that was lost is not brought back: the gaps that unreadable or lost events
leave wait for the repair of the engine, and "whiteroom doctor" goes on reporting a
gap and an event that is no record.

On the standard output the command prints "accepted the break at record N of the
audit log LOG in DATA; whiteroom doctor checks the log again from it". A refusal is
a line on the standard error that starts with "whiteroom-db accept-break:", and the
exit code 1.

No daemon may have the data directory open: stop "whiteroom-db serve" on it first.
If one has, the command waits for about two seconds, and then fails without
changing anything. The daemon has no command for this, so that an admin server that
has been broken into cannot accept the changes that it made to the log.

After a sync has failed on the data directory (in the daemon, or in this
command), the command refuses the directory until the machine has restarted, and
says so on the standard error: what the sync was to write may be in the page
cache only. The refusal changes nothing. After the restart the command removes
the mark <DATA>/stopped, which the failure left, and says so on the standard
error. It also refuses a data directory that it cannot write in, where it could
not leave the mark.

With -config, the item data of the YAML file gives what -data leaves out; the file
may hold other items, which are checked and not used. Without it, no config file
is read.

Flags:
`

// runAcceptBreak runs whiteroom-db accept-break with args, those after accept-break.
func runAcceptBreak(ctx context.Context, args []string, e env) int {
	const command = "whiteroom-db accept-break"
	var log string
	var record uint64
	flags, data, code, ok := setupFlags(command, acceptUsage, args, e, func(flags *flag.FlagSet) {
		flags.StringVar(&log, "log", "", "accept the break in the audit log `LOG`, login or activity")
		flags.Uint64Var(&record, "record", 0, "accept the break at the record with the number `N`")
	})
	if !ok {
		return code
	}
	switch {
	case !flagGiven(flags, "log"):
		return usageError(flags, "no audit log: give -log login or -log activity")
	case log != dbwire.LogLogin && log != dbwire.LogActivity:
		return usageError(flags, "the audit log %q is neither login nor activity", log)
	case !flagGiven(flags, "record"):
		return usageError(flags, "no record: give -record N, the number that whiteroom doctor reports")
	case record < 1:
		return usageError(flags, "the records are numbered from 1, not %d", record)
	}
	err := dbserver.Maintain(ctx, data, maintainOptions(e, command), func(m *logdb.Maintenance) error {
		return dbserver.AcceptBreak(ctx, m, log, record, time.Now())
	})
	if err != nil {
		return failed(ctx, e, command, err)
	}
	printf(e.stdout, "accepted the break at record %d of the audit log %s in %s; whiteroom doctor checks the log again from it\n",
		record, log, data)
	return 0
}
