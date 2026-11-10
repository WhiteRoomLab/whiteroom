#!/bin/sh
# Records the workload of internal/store (TestDMLogWorkload) on ext4 through the dm-log-writes target of Linux, for
# TestDMLogCheck (docs 3.5). It needs root, and has not been run yet (6.12, item 130).
#
#	usage: record.sh <output dir> <test binary of internal/store>
#
# Make the test binary in the dev shell with: go test -c -o <file> ./internal/store
# The output dir, missing or empty, gets log.img (the log), data.img (the device at the end), data-size (its size in
# bytes), workload.txt (the output of the workload) and env.txt (the environment). Check it as any user, in the dev
# shell: WHITEROOM_DMLOG=<output dir> go test -count=1 -v -run '^TestDMLogCheck$' ./internal/store
set -eu
LC_ALL=C
export LC_ALL

name=whiteroom-dmlog
data_size=256M
log_size=1G

die() {
	echo "record.sh: $*" >&2
	exit 1
}

if [ $# -ne 2 ]; then
	echo "usage: record.sh <output dir> <test binary of internal/store>" >&2
	exit 2
fi
out=$1
bin=$2
[ "$(id -u)" -eq 0 ] || die "needs root"
for tool in blockdev dmsetup dumpe2fs losetup mkfifo mkfs.ext4 mount tr truncate umount; do
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
dmsetup create "$name" --table "0 $(blockdev --getsz "$data_dev") log-writes $data_dev $log_dev"
created=1
dev=/dev/mapper/$name
# Everything of mkfs before the mark, and no discards.
mkfs.ext4 -q -F -T default -E nodiscard,lazy_itable_init=0,lazy_journal_init=0 "$dev"
dmsetup message "$name" 0 mark mkfs
mkdir "$out/mnt"
mount -t ext4 "$dev" "$out/mnt"
mounted=1
{
	echo "date: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
	echo "kernel: $(uname -srvm)"
	echo "target: $(dmsetup targets | grep '^log-writes' || :)"
	echo "data: $data_dev, $size bytes, logical sectors of $(blockdev --getss "$data_dev") bytes"
	echo "log: $log_dev"
	echo "mount: $(grep " $out/mnt " /proc/mounts || :)"
	echo "workload: $bin"
	mkfs.ext4 -V 2>&1
	dumpe2fs -h "$dev" 2>&1
} >"$out/env.txt"

# The workload prints "dmlog: <text>" for a mark, and waits for "go" until the mark is in the log.
mkfifo "$out/workload.in" "$out/workload.out"
WHITEROOM_DMLOG_WORKLOAD=$out/mnt/store "$bin" -test.run '^TestDMLogWorkload$' -test.count=1 -test.v \
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
