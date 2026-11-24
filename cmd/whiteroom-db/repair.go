package main

import (
	"context"
	"flag"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbserver"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
)

const repairUsage = `Usage: whiteroom-db repair [flags]
       whiteroom-db repair -apply -keep DIR [-truncate] [-drop-unreadable-records] [flags]

Repair a data directory whose files of the storage engine are damaged, without
restoring a copy of it. Without -apply it only shows what a repair would drop and
lose, and changes nothing: not a file of the data directory, not the mark
<DATA>/stopped, and it works on a data directory that it cannot write in.

A repair keeps the state of the last commit that comes before the first damage that
cannot be read, or the state of the checkpoint, which covers the damage before it.
It drops what cannot be read: the records of the log that are damaged, the objects
whose data cannot be read or does not have their hash, the segments of the log of
other generations and the temporary files; the rest is written as a new generation
of the log, as "whiteroom-db compact" does. A row that names a dropped object stays,
and so does the original of a medium that is missing: "whiteroom doctor" reports
them, and the content model must fix them. Nothing in files/ is changed.

The commits that come after the first damage are correct, and a repair drops them
only with -truncate: it refuses, and changes nothing, if it must drop them and
-truncate is not given. A repair never drops the commits that come before the
damage, and refuses a store that it would leave without a commit: restore that
store from a copy.

The audit logs have a number for each record and the hash of the record before it. A
repair that drops events that were audit records leaves a hole in a log: the records
A to B are lost. It records each hole, in the same generation, in the table chains
and with an event of the daemon, so that "whiteroom doctor" checks the log again
from the record after the hole, and tells the hole in a note. A store of the table
schema 8 gets the schema 9 for that; one of an older schema is refused if a hole is to
be recorded in it. A gap that
is no hole of this repair stays what it was, and "whiteroom doctor" goes on
reporting it. The heads of the logs that were kept outside the store are no part of
it: one that names a record that a repair dropped is a problem still.

An event that is neither an event of the daemon nor an audit record that this
program can read is kept, and its number is told, unless -drop-unreadable-records
is given. A writer that sends what the store never sends leaves such events, and
"whiteroom doctor" stops its check of the audit log at the first of them.

-apply -keep DIR first copies the files of logdb/, as they are, to DIR/logdb/, and
makes them durable, and then repairs. DIR must be missing, in a directory that
exists, or empty, and must not be the data directory or in it. It must not be a
symbolic link itself (the directories that hold it may be). The copy and the
files/ of the data directory are a data directory that "whiteroom-db check" shows
the damage of again. The copy holds all that logdb/ holds, the hashes of the
passwords and the audit logs included: keep it as private as the data directory.
-keep and -apply need each other. -truncate and -drop-unreadable-records without
-apply change what is shown: it is what -apply with them would do.

The standard output has a line for each thing, which has no key or value of a row,
and no data of an object or of an original:

  drop: PROBLEM (sequence A to B, events C to D)   what cannot be read, and the
                                                   commits and events that it takes
  drop: object HASH: PLACE: REASON                 an object that is dropped
  lose: the commits A to B after PLACE: REASON     the commits that only -truncate
                                                   drops ("; only with -truncate")
  recover: PROBLEM                                 what Open would change or not
                                                   use, which a repair writes anew
  remove: FILE                                     a file that a repair removes
  unreadable: event N                              an event that is no record ("; dropped")
  hole: audit log "LOG": records A to B            records that are lost ("; recorded
                                                   in the table chains")
  schema: ...                                      the table schema that is raised
  gap: audit log "LOG": records A to B, ...        a gap that this repair does not make

and then "nothing to repair in DATA", or the number of things to repair and the
command that repairs them. With -apply, the first line is "kept the files of
DATA/logdb in DIR/logdb", and the last "repaired DATA: generation G, sequence S".
The exit code is 0 if it showed or repaired, 1 if it failed or refused (the reason is
on the standard error, after "whiteroom-db repair:"), and 2 for a usage error.

No daemon may have the data directory open: stop "whiteroom-db serve" on it first.
If one has, the command waits for about two seconds, and then fails without
changing anything.

After a sync has failed on the data directory (in the daemon, or in a command), the
command refuses the directory until the machine has restarted, and says so on the
standard error: what the sync was to write may be in the page cache only. The
refusal changes nothing. With -apply, after the restart, it removes the mark
<DATA>/stopped, which the failure left, and says so on the standard error; without
-apply the mark is left where it is. With -apply it also refuses a data directory
that it cannot write in, where it could not leave the mark. A sync that fails in the
repair itself makes it leave the mark; one that fails in the copy does not, which
writes nothing in the data directory, and the copy is removed.

With -config, the item data of the YAML file gives what -data leaves out; the file
may hold other items, which are checked and not used. Without it, no config file
is read.

Flags:
`

// runRepair runs whiteroom-db repair with args, those after repair.
func runRepair(ctx context.Context, args []string, e env) int {
	const command = "whiteroom-db repair"
	var apply, truncate, dropUnreadable bool
	var keep string
	flags, data, code, ok := setupFlags(command, repairUsage, args, e, func(flags *flag.FlagSet) {
		flags.BoolVar(&apply, "apply", false, "repair the data directory; without it, only show what a repair would drop")
		flags.StringVar(&keep, "keep", "", "copy the files of logdb/ to the directory `DIR` before the repair (required with -apply)")
		flags.BoolVar(&truncate, "truncate", false, "drop the commits that can be read but come after the first damage")
		flags.BoolVar(&dropUnreadable, "drop-unreadable-records", false, "drop the events that are no audit record or event of the daemon")
	})
	if !ok {
		return code
	}
	switch {
	case apply && keep == "":
		return usageError(flags, "no copy of logdb/: give -keep DIR, the directory for it, with -apply")
	case !apply && flagGiven(flags, "keep"):
		return usageError(flags, "-keep is for -apply: give -apply too, or leave out -keep")
	}
	rep, err := dbserver.Repair(ctx, data, maintainOptions(e, command), dbserver.RepairOptions{
		Apply: apply, Truncate: truncate, DropUnreadable: dropUnreadable, Keep: keep, Now: time.Now()})
	printRepair(e, data, rep, truncate, dropUnreadable)
	if err != nil {
		return failed(ctx, e, command, err)
	}
	switch things := rep.Things(); {
	case rep.Applied:
		printf(e.stdout, "repaired %s: generation %d, sequence %d\n", data, rep.Result.Generation, rep.Result.Seq)
	case things == 0:
		printf(e.stdout, "nothing to repair in %s\n", data)
	default:
		again := "whiteroom-db repair -apply -keep DIR"
		if rep.Plan.Truncation != nil {
			again += " -truncate"
		}
		if dropUnreadable {
			again += " -drop-unreadable-records"
		}
		noun, pronoun := "things", "them"
		if things == 1 {
			noun, pronoun = "thing", "it"
		}
		printf(e.stdout, "%d %s to repair in %s; %s repairs %s\n", things, noun, data, again, pronoun)
	}
	return 0
}

// printRepair prints the lines of the report rep of the repair of data on the standard output.
func printRepair(e env, data string, rep dbserver.RepairReport, truncate, dropUnreadable bool) {
	if rep.Kept != "" {
		printf(e.stdout, "kept the files of %s in %s\n", filepath.Join(data, "logdb"), rep.Kept)
	}
	plan := rep.Plan
	for _, s := range plan.Spans {
		line := "drop: " + dbserver.EngineProblem(s.Problem).String()
		var what []string
		if s.FirstSeq != 0 {
			what = append(what, "sequence "+numbers(s.FirstSeq, s.LastSeq))
		}
		if s.FirstEvent != 0 {
			what = append(what, "events "+numbers(s.FirstEvent, s.LastEvent))
		}
		if len(what) > 0 {
			line += " (" + strings.Join(what, ", ") + ")"
		}
		printf(e.stdout, "%s\n", line)
	}
	for _, o := range plan.Objects {
		p := dbserver.EngineProblem(o.Problem)
		p.Detail = strings.TrimPrefix(p.Detail, fmt.Sprintf("object %x: ", o.Hash))
		p.Area = fmt.Sprintf("object %x", o.Hash)
		printf(e.stdout, "drop: %s\n", p)
	}
	if t := plan.Truncation; t != nil {
		commits := "the commits " + numbers(t.FirstSeq, t.LastSeq)
		if t.FirstSeq == t.LastSeq {
			commits = "the commit " + numbers(t.FirstSeq, t.LastSeq)
		}
		p := dbserver.EngineProblem(t.Damage)
		line := fmt.Sprintf("lose: %s after %s", commits, strings.TrimPrefix(p.String(), "engine: "))
		if !truncate {
			line += "; only with -truncate"
		}
		printf(e.stdout, "%s\n", line)
	}
	for _, p := range plan.Recovery {
		printf(e.stdout, "recover: %s\n", dbserver.EngineProblem(p))
	}
	for _, name := range plan.Remove {
		printf(e.stdout, "remove: %s\n", name)
	}
	for _, n := range rep.Unreadable {
		printf(e.stdout, "unreadable: event %d%s\n", n, dropped(rep.Dropped))
	}
	if more := rep.UnreadableTotal - len(rep.Unreadable); more > 0 {
		printf(e.stdout, "unreadable: %d more events%s\n", more, dropped(rep.Dropped))
	}
	for _, h := range rep.Holes {
		line := fmt.Sprintf("hole: audit log %q: %s", h.Log, records(h.First, h.Last))
		if h.Row {
			line += "; recorded in the table chains"
		} else {
			line += "; not recorded: the record after it has no hash of the record before it that can be read"
		}
		printf(e.stdout, "%s\n", line)
	}
	if rep.SchemaFrom != 0 {
		printf(e.stdout, "schema: the table schema %d is raised to %d, which has the rows of the holes\n", rep.SchemaFrom, dbwire.TableVersion)
	}
	for _, g := range rep.Gaps {
		printf(e.stdout, "gap: audit log %q: %s, which this repair does not make: whiteroom doctor goes on reporting it\n", g.Log,
			records(g.First, g.Last))
	}
}

func dropped(yes bool) string {
	if yes {
		return "; dropped"
	}
	return ""
}

// numbers returns "A to B", or "A" if they are one.
func numbers(first, last uint64) string {
	if first == last {
		return fmt.Sprint(first)
	}
	return fmt.Sprintf("%d to %d", first, last)
}

func records(first, last uint64) string {
	if first == last {
		return fmt.Sprintf("record %d", first)
	}
	return fmt.Sprintf("records %d to %d", first, last)
}
