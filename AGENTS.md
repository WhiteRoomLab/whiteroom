# AGENTS.md

WordPress の代替となる CMS を Go で作る。設計は `CONCEPT.md` が正なので、作業に関わる節を先に読む。

## 開発シェル

- `nix develop` で入る（direnv を使うなら、最初に一度 `direnv allow`）。
- ツールの版は flake で決まる。Go のマイナー版は `flake.nix` の `go_1_27` で、それ以外は `flake.lock`（更新は `nix flake update`）。CI も `nix develop -c` で同じ版を使うので、`actions/setup-go` などで版を足さない。
- `go.mod` の `go` 行は言語の最低版（いまは `1.27.0`）で、パッチ版に合わせて上げない。`toolchain` 行は書かない（`GOTOOLCHAIN=local` なので使われない）。

## よく使うコマンド

開発シェルの中で、リポジトリの直下で実行する。CI（`.github/workflows/ci.yml`）も同じ確認をする。

- `go build ./...`・`go vet ./...`・`go test -race ./...`
- `golangci-lint run`：リンター。設定は `.golangci.yml`
- `golangci-lint config verify`：`.golangci.yml` を変えたとき（`run` は未知の項目を黙って無視する）
- `golangci-lint fmt`：整形（gofmt・goimports）
- `go mod tidy`

`flake.nix` や `flake.lock` を変えたら、`nix fmt` と `nix flake check --all-systems` も実行する。

## パッケージの置き場所

- パッケージは `internal/` の下に置く。コンテンツモデルは `internal/content`。
- コマンドは、作るときに `cmd/<名前>/` に置く（名前は未定）。

## 規約

- コードと設定ファイルのコメントは英語で、最小限にする。消すと誤った「修正」を招く箇所にだけ、短い1行で書く。
- doc コメントは、名前だけで分からないときだけ書く。公開する識別子のすべてには付けない。
- ドキュメントは日本語で書く。
- コミットは Conventional Commits。type は `feat`・`fix`・`docs`・`refactor`・`test`・`build`・`ci`・`chore` など（`update`・`add` は使わない）。説明と本文は日本語（例：`build: 開発環境の Nix flake を追加`）。
