{
  description = "whiteroom development environment";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs =
    { nixpkgs, ... }:
    let
      inherit (nixpkgs) lib;
      # No x86_64-darwin: nixos-unstable has dropped it.
      forAllSystems =
        f:
        lib.genAttrs [
          "x86_64-linux"
          "aarch64-linux"
          "aarch64-darwin"
        ] (system: f nixpkgs.legacyPackages.${system});
      goOf = pkgs: pkgs.go_1_27;
    in
    {
      devShells = forAllSystems (pkgs: {
        # Not mkShellNoCC: nixpkgs' Go enables cgo, and -race needs it.
        default = pkgs.mkShell {
          packages = [
            (goOf pkgs)
            pkgs.gopls
            pkgs.golangci-lint
            pkgs.jq
            pkgs.python3
            # For go generate ./internal/webp.
            pkgs.libwebp
            pkgs.libvpx
            # For the tests of the server files of internal/build.
            pkgs.nginx
            pkgs.apacheHttpd
            # For the tests of internal/wpblock (WHITEROOM_WP_PARSER) and internal/wpformat
            # (WHITEROOM_WP_FORMATTING) against the PHP of WordPress.
            # Only the out output: the dev output pulls in the build tools of phpize.
            pkgs.php.unwrapped.out
          ]
          # For the dm-log-writes check of internal/store (mke2fs, e2fsck, debugfs); Linux only.
          ++ lib.optionals pkgs.stdenv.hostPlatform.isLinux [ pkgs.e2fsprogs ];

          env.GOTOOLCHAIN = "local";
        };
      });

      # The check of internal/store with dm-log-writes (docs 3.5), in virtual machines: `nix build -L .#dmlog`.
      # It needs KVM, and CI does not build it. Only x86_64-linux has run it.
      packages.x86_64-linux.dmlog =
        let
          pkgs = nixpkgs.legacyPackages.x86_64-linux;
        in
        import ./internal/logdb/dmlog/vm.nix {
          inherit pkgs;
          go = goOf pkgs;
          src = lib.fileset.toSource {
            root = ./.;
            fileset = lib.fileset.unions [
              ./go.mod
              ./internal
            ];
          };
        };

      formatter = forAllSystems (pkgs: pkgs.nixfmt-tree);
    };
}
