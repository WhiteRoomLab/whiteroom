package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbserver"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
)

const acceptUsage = `Usage: whiteroom-db accept-break [flags] -log LOG -record N

Accept the break at record N of the audit log LOG (login or activity), or the gap
before it, once you have looked into it. A break is a record whose number is that
of the one before it plus one and whose prev is not the hash of that record. The
first record that is read is a break too, if it is the one that the log starts with
and no record before it is in the store: record 1 with a prev (the first record of a
log has none), or the first that remains after the deletion of old login records,
whose prev is not the hash that the start of the chain has. A gap is records that
are missing: record N has a number above that of the one before it plus one, or the
head of the log names records that the log does not have. "whiteroom doctor" reports
both. Take LOG and N from its report:

  audit log "login": record 7 (event 31): prev is not the hash of record 6
  audit log "login": record 9 (event 33): the number does not follow that of record 6
  audit log "activity": the head in the table is record 12, but the last record is record 10 (event 44)

N is 7 for the break, 9 for the gap in the middle of the log (the record after it),
and 12 for the gap at the end (the number of the head). A gap that "whiteroom-db
repair" tells in a "gap:" line is accepted the same way.

The command changes no record and drops none. It leaves three traces: a row of the
table chains (for a break, it ties the acceptance to the hash of record N and to
the time of the command; for a gap, it says which records are missing, which hash
the record after them has as its prev, and the time of the command), an event of the
daemon in the log, and the line on the standard output below. From then on
"whiteroom doctor" checks the log again from record N (after a gap at the end, it
checks that the log ends with the record before the gap), and tells the break or the
gap in a note, not as a problem. A later change to record N undoes the acceptance of
a break, and a later change to its prev that of a gap; a break or a gap at another
record is still reported.

The command refuses, and changes nothing, a record that is not in the store (also
one before the start of the chain, which the deletion of old login records made, and
one that a gap leaves out, for which it names the record to accept the gap with),
a record that follows a hole that "whiteroom-db repair" recorded or a gap that is
accepted already, a record whose number is below that of a record before it, the gap
before a record whose prev is not the hash of a record, record 1 with no prev, the first
that remains after such a deletion with the hash that the start of the chain has (no
break is there), the first that remains after such a deletion while records before it
that the deletion left are still in the store ("whiteroom-db compact" drops them if
they are what the deletion left: accept the break then), a record that follows the one
before it, a break that is accepted already, a chain whose rows cannot be read, and a
data directory of an older table schema than this program's: the first writer that
connects through "whiteroom-db serve" (such as "whiteroom serve admin") upgrades it.

It accepts a break or a gap at a record that can be read, and no more. An event that
is no record, which damage to the engine leaves, has no record number to name it by,
and a record that was lost is not brought back. "whiteroom-db repair" records as a
hole a gap that it makes itself, by dropping what it cannot read, and tells a gap
that it did not make; "whiteroom doctor" goes on reporting such a gap until it is
accepted here, and an event that is no record. An activity log always has a record,
so after a gap at the end of an activity log that has none, "whiteroom doctor" goes
on reporting the empty log until the writer has added the next record.

On the standard output the command prints, for a break, "accepted the break at
record N of the audit log LOG in DATA; whiteroom doctor checks the log again from
it". For a gap in the middle of the log it prints "accepted the gap of the records A
to B before record N of the audit log LOG in DATA; whiteroom doctor checks the log
again from record N" ("the gap of record A" if it is one record), and for a gap at
the end "accepted the gap of the records A to B at the end of the audit log LOG in
DATA; whiteroom doctor checks that the log ends with record A-1" (or "that the log
has no record"). A refusal is a line on the standard error that starts with
"whiteroom-db accept-break:", and the exit code 1.

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
		flags.StringVar(&log, "log", "", "accept the break or the gap in the audit log `LOG`, login or activity")
		flags.Uint64Var(&record, "record", 0, "accept the break at the record with the number `N`, or the gap before it (at the end of "+
			"the log: the number of its head)")
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
	var acc dbserver.Acceptance
	err := dbserver.Maintain(ctx, data, maintainOptions(e, command), func(m *logdb.Maintenance) (err error) {
		acc, err = dbserver.AcceptBreak(ctx, m, log, record, time.Now())
		return err
	})
	if err != nil {
		return failed(ctx, e, command, err)
	}
	gap := "the gap of record " + fmt.Sprint(acc.First)
	if acc.First != acc.Last {
		gap = fmt.Sprintf("the gap of the records %d to %d", acc.First, acc.Last)
	}
	switch {
	case !acc.Gap:
		printf(e.stdout, "accepted the break at record %d of the audit log %s in %s; whiteroom doctor checks the log again from it\n",
			record, log, data)
	case acc.End && acc.NoRecord:
		printf(e.stdout, "accepted %s at the end of the audit log %s in %s; whiteroom doctor checks that the log has no record\n",
			gap, log, data)
	case acc.End:
		printf(e.stdout, "accepted %s at the end of the audit log %s in %s; whiteroom doctor checks that the log ends with record %d\n",
			gap, log, data, acc.First-1)
	default:
		printf(e.stdout, "accepted %s before record %d of the audit log %s in %s; whiteroom doctor checks the log again from record %d\n",
			gap, record, log, data, record)
	}
	return 0
}
