# Records the workload of internal/store through dm-log-writes and checks the recordings (docs 3.5), each in a virtual
# machine of QEMU with the kernel of nixpkgs, where the build is root: it needs KVM, and no root. flake.nix makes it
# the package dmlog:
#
#	nix build -L .#dmlog
#
# The result has summary.txt, and a directory for each recording with the output of its check (check.txt) and of its
# workload (workload.txt), and its environment (env.txt). The build fails if a check does. One recording is an
# attribute of the package: nix build -L .#dmlog.xfs
{
  pkgs,
  go,
  # go.mod and the packages of the module.
  src,
}:
let
  inherit (pkgs) lib;

  # The test binary of internal/store, static: the workload and the check.
  storeTest = pkgs.stdenvNoCC.mkDerivation {
    name = "whiteroom-store-test";
    inherit src;
    nativeBuildInputs = [ go ];
    env = {
      CGO_ENABLED = "0";
      GOTOOLCHAIN = "local";
      GOPROXY = "off";
    };
    buildPhase = ''
      export HOME=$TMPDIR
      go test -c -o $out/bin/store.test ./internal/store
    '';
    dontInstall = true;
    dontFixup = true;
  };

  tools = {
    ext4 = [ pkgs.e2fsprogs ];
    xfs = [ pkgs.xfsprogs ];
    btrfs = [ pkgs.btrfs-progs ];
  };

  recordings = [
    {
      name = "ext4";
      fs = "ext4";
    }
    {
      name = "ext4-data-journal";
      fs = "ext4";
      options = "data=journal";
    }
    {
      name = "ext4-data-writeback";
      fs = "ext4";
      options = "data=writeback";
    }
    {
      name = "xfs";
      fs = "xfs";
    }
    {
      name = "btrfs";
      fs = "btrfs";
    }
    # The store of these workloads drops syncs: their checks must find problems. ext4 and xfs write the names with
    # the sync of any file, and show nothing without the syncs of the directories.
    {
      name = "ext4-no-file-syncs";
      fs = "ext4";
      loose = "files";
    }
    {
      name = "xfs-no-file-syncs";
      fs = "xfs";
      loose = "files";
    }
    {
      name = "btrfs-no-file-syncs";
      fs = "btrfs";
      loose = "files";
    }
    {
      name = "btrfs-no-dir-syncs";
      fs = "btrfs";
      loose = "dirs";
    }
  ];

  # record records the workload on the file system fs, mounted with options, and checks the recording in every state
  # of the device, which it mounts. With loose, the workload drops those syncs, and the check passes only if it finds
  # problems.
  record =
    {
      name,
      fs,
      options ? "",
      loose ? "",
    }:
    pkgs.vmTools.runInLinuxVM (
      pkgs.runCommand "whiteroom-dmlog-${name}"
        {
          memSize = 4096;
          nativeBuildInputs = [
            pkgs.kmod
            pkgs.lvm2
            pkgs.util-linux
          ]
          ++ tools.${fs};
        }
        ''
          ln -s ${lib.getOutput "modules" pkgs.linux}/lib /lib
          modprobe -a loop dm-log-writes ${fs}
          # No udev in the virtual machine: libdevmapper makes the nodes of the devices.
          export DM_DISABLE_UDEV=1
          ${lib.optionalString (loose != "") "WHITEROOM_DMLOG_LOOSE=${loose} "}sh ${./record.sh} \
            /tmp/recording ${storeTest}/bin/store.test ${fs} ${lib.escapeShellArg options}
          mkdir $out
          cp /tmp/recording/env.txt /tmp/recording/workload.txt $out/
          export WHITEROOM_DMLOG=/tmp/recording WHITEROOM_DMLOG_MOUNT=${fs} WHITEROOM_DMLOG_STATES=all
          ${lib.optionalString (loose != "") "export WHITEROOM_DMLOG_WANT=problems"}
          if ! ${storeTest}/bin/store.test -test.run '^TestDMLogCheck$' -test.count=1 -test.timeout 0 -test.v \
            >$out/check.txt 2>&1; then
            head -c 20000 $out/check.txt
            exit 1
          fi
          grep -m 1 ' entries, ' $out/check.txt
        ''
    );
in
pkgs.runCommand "whiteroom-dmlog"
  {
    passthru = lib.listToAttrs (map (r: lib.nameValuePair r.name (record r)) recordings);
  }
  ''
    mkdir $out
    ${lib.concatMapStrings (r: ''
      ln -s ${record r} $out/${r.name}
      echo "${r.name}: $(grep -m 1 ' entries, ' $out/${r.name}/check.txt | sed 's/^ *[^ ]* //')" >>$out/summary.txt
    '') recordings}
  ''
