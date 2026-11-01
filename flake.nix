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
    in
    {
      devShells = forAllSystems (pkgs: {
        # Not mkShellNoCC: nixpkgs' Go enables cgo and needs a C compiler.
        default = pkgs.mkShell {
          packages = [
            pkgs.go_1_27
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
          ];

          env.GOTOOLCHAIN = "local";
        };
      });

      formatter = forAllSystems (pkgs: pkgs.nixfmt-tree);
    };
}
