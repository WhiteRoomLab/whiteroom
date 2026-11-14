#!/bin/sh
# Records the workload of internal/store (TestDMLogWorkload) on a file system through the dm-log-writes target of
# Linux, for TestDMLogCheck (docs 3.5). It needs root; vm.nix runs it in a virtual machine, which needs none.
#
#	usage: record.sh <output dir> <test binary of internal/store> [ext4|xfs|btrfs [<mount options>]]
#
# Make the test binary in the dev shell with: CGO_ENABLED=0 go test -c -o <file> ./internal/store
# The output dir, missing or empty, gets log.img (the log), data.img (the device at the end), data-size (its size in
# bytes), workload.txt (the output of the workload) and env.txt (the environment). Check a recording of ext4 as any
# user, in the dev shell: WHITEROOM_DMLOG=<output dir> go test -count=1 -v -run '^TestDMLogCheck$' ./internal/store
# The environment goes to the workload: with WHITEROOM_DMLOG_LOOSE=files or dirs, its store drops those syncs, and
# the check must find problems.
set -eu
LC_ALL=C
export LC_ALL

name=whiteroom-dmlog
# mkfs.xfs makes nothing smaller than 300M.
data_size=512M
log_size=1G

die() {
	echo "record.sh: $*" >&2
	exit 1
}

if [ $# -lt 2 ] || [ $# -gt 4 ]; then
	echo "usage: record.sh <output dir> <test binary of internal/store> [ext4|xfs|btrfs [<mount options>]]" >&2
	exit 2
fi
out=$1
bin=$2
fs=${3:-ext4}
options=${4:-}
case $fs in
ext4) info=dumpe2fs ;;
xfs) info=xfs_info ;;
btrfs) info=btrfs ;;
*) die "$fs: not ext4, xfs or btrfs" ;;
esac
[ "$(id -u)" -eq 0 ] || die "needs root"
for tool in blockdev dmsetup "$info" losetup mkfifo "mkfs.$fs" mount tr truncate umount; do
	command -v "$tool" >/dev/null 2>&1 || die "needs $tool"
done
[ -f "$bin" ] && [ -x "$bin" ] || die "$bin: not an executable file"
case $bin in
/*) ;;
*) bin=$PWD/$bin ;;
esac
mkdir -p "$out"
out=$(cd "$out" && pwd -P)
[ -z "$(ls -A "$out")" ] || die "$out: not empty"
if dmsetup info "$name" >/dev/null 2>&1; then
	die "the device $name exists"
fi

data_dev=
log_dev=
created=
mounted=
pid=
cleanup() {
	status=$?
	trap - EXIT
	# A process of the workload that waits for a reply ends without one, and the workload ends its process on TERM.
	exec 3>&-
	if [ -n "$pid" ]; then
		kill "$pid" 2>/dev/null || :
		wait "$pid" 2>/dev/null || :
	fi
	if [ -n "$mounted" ]; then umount "$out/mnt" || :; fi
	if [ -n "$created" ]; then dmsetup remove --retry "$name" || :; fi
	if [ -n "$log_dev" ]; then losetup -d "$log_dev" || :; fi
	if [ -n "$data_dev" ]; then losetup -d "$data_dev" || :; fi
	rm -f "$out/workload.in" "$out/workload.out"
	exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
# A write to the workload after it has ended fails instead.
trap '' PIPE

truncate -s "$data_size" "$out/data.img"
truncate -s "$log_size" "$out/log.img"
data_dev=$(losetup --find --show "$out/data.img")
log_dev=$(losetup --find --show "$out/log.img")
size=$(blockdev --getsize64 "$data_dev")
# The check tells the order of the writes by this (Log.Unordered of dmlog.go).
fua=$(cat "/sys/block/${data_dev#/dev/}/queue/fua")
[ "$fua" = 0 ] || die "$data_dev has FUA of its own: the log would not tell the order of its writes"
dmsetup create "$name" --table "0 $(blockdev --getsz "$data_dev") log-writes $data_dev $log_dev"
created=1
dev=/dev/mapper/$name
# Everything of mkfs before the mark, and no discard of the device, which the log would have as one entry.
case $fs in
ext4) mkfs.ext4 -q -F -T default -E nodiscard,lazy_itable_init=0,lazy_journal_init=0 "$dev" ;;
xfs) mkfs.xfs -q -f -K "$dev" ;;
btrfs) mkfs.btrfs -q -f -K "$dev" ;;
esac
dmsetup message "$name" 0 mark mkfs
mkdir "$out/mnt"
mount -t "$fs" ${options:+-o "$options"} "$dev" "$out/mnt"
mounted=1
{
	echo "date: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
	echo "kernel: $(uname -srvm)"
	echo "target: $(dmsetup targets | grep '^log-writes' || :)"
	echo "data: $data_dev, $size bytes, logical sectors of $(blockdev --getss "$data_dev") bytes, without FUA"
	echo "log: $log_dev"
	echo "mount: $(grep " $out/mnt " /proc/mounts || :)"
	echo "workload: $bin${WHITEROOM_DMLOG_LOOSE:+, without the syncs of $WHITEROOM_DMLOG_LOOSE}"
	"mkfs.$fs" -V 2>&1
	case $fs in
	ext4) dumpe2fs -h "$dev" 2>&1 ;;
	xfs) xfs_info "$out/mnt" 2>&1 ;;
	btrfs) btrfs filesystem show "$dev" 2>&1 ;;
	esac
} >"$out/env.txt"

# The workload prints "dmlog: <text>" for a mark, and waits for "go" until the mark is in the log.
mkfifo "$out/workload.in" "$out/workload.out"
WHITEROOM_DMLOG_WORKLOAD=$out/mnt "$bin" -test.run '^TestDMLogWorkload$' -test.count=1 -test.v \
	<"$out/workload.in" >"$out/workload.out" 2>&1 &
pid=$!
exec 3>"$out/workload.in"
marks=0
while IFS= read -r line; do
	printf '%s\n' "$line" >>"$out/workload.txt"
	case $line in
	'dmlog: '*)
		mark=$(printf '%s\n' "${line#'dmlog: '}" | tr ' ' -)
		case $mark in
		'' | *[!a-z0-9-]*) die "a mark of the workload that is not one: $line" ;;
		esac
		dmsetup message "$name" 0 mark "$mark"
		marks=$((marks + 1))
		echo go >&3 || die "the workload has stopped reading"
		;;
	esac
done <"$out/workload.out"
exec 3>&-
status=0
wait "$pid" || status=$?
pid=
[ "$status" -eq 0 ] || die "the workload has failed with $status (see $out/workload.txt)"

umount "$out/mnt"
mounted=
rmdir "$out/mnt"
echo "status: $(dmsetup status "$name")" >>"$out/env.txt"
# Logs the mark dm-log-writes-end.
dmsetup remove --retry "$name"
created=
losetup -d "$log_dev"
log_dev=
losetup -d "$data_dev"
data_dev=
rm -f "$out/workload.in" "$out/workload.out"
echo "$size" >"$out/data-size"
if [ -n "${SUDO_UID:-}" ]; then
	chown -R "$SUDO_UID:${SUDO_GID:-$SUDO_UID}" "$out"
fi
sync
echo "record.sh: $marks marks of the workload in $out/log.img"
