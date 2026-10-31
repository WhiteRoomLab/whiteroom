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

- パッケージは `internal/` の下に置く。コンテンツモデルは `internal/content`、Unicode の NFC は `internal/nfc`、メディアのファイルの中身（形式・大きさ・向き）を読む処理は `internal/mediafile`、開発者が書くファイル（`site/schema.yaml`、テーマの `theme.yaml` と翻訳ファイル）の YAML を読む処理は `internal/yaml`。
- テーマ（`CONCEPT.md` の 5.4）の設定ファイルと翻訳ファイルの型・読み込み・検証は、コンテンツの検証からも使うので `internal/content` に置く。テーマのディレクトリ（設定・ページとブロックのテンプレート（`blocks/`）・部品・翻訳・assets）を読み、テンプレートを実行する処理は `internal/theme`。
- 文中装飾とブロックの HTML は `internal/render`。テーマにブロックのテンプレートが無いときに使う、本体の既定のテンプレート（`CONCEPT.md` の 6.8）は `internal/render/blocks/` に置く。
- ほかの描画とビルドのパッケージは、作るときに置く：`style` の CSS は `internal/css`、`html`・`legacy` のサニタイズは `internal/sanitize`、公開する画像は `internal/imaging`、静的ビルドは `internal/build`。依存の向きは `build` → `render`・`css`・`sanitize`・`imaging` → `theme` → `content`。
- コマンドは、作るときに `cmd/<名前>/` に置く（名前は未定）。

## 生成するファイル

- `internal/nfc/tables.go` と `internal/nfc/testdata/NormalizationTest.txt.gz` は `go generate ./internal/nfc` で作る（unicode.org から UCD を取得するので、ネットが要る）。手で編集しない。
- 表の Unicode の版は、Go の `unicode.Version` に合わせる。Go のマイナー版を上げて `unicode.Version` が変わると `internal/nfc` のテストが失敗するので、そのときに作り直す。
- `internal/yaml/testdata/yaml-test-suite.json.gz` は `go generate ./internal/yaml` で作る（GitHub から yaml-test-suite の data-2022-01-17 を取得し、sha256 を確かめるので、ネットが要る）。手で編集しない。ライセンスは同じディレクトリの `YAML-TEST-SUITE-LICENSE.txt`。
- `internal/theme/testdata/example-out/` は、見本のテーマ（`internal/theme/testdata/example/`）で描画したページの正解で、`go test ./internal/theme -run TestExamplePages -update` で作る（`-update` はパッケージの後ろに書く）。手で編集しない。出力が変わる変更をしたら作り直し、差分を確かめる。
- `internal/render/testdata/out/` は、テストの文書（`internal/content/testdata/` の `every-block.json`・`about-page-body.json` と、`internal/render/testdata/every-mark.json`）を、ブロックのテンプレートの無いテーマ（`default`。本体の既定のテンプレートで描画する）と見本のテーマ（`example`）で、日本語（サイトのパスなし）と英語（`/blog`）に描画した HTML と警告の正解で、`go test ./internal/render -run TestGolden -update` で作る（`-update` はパッケージの後ろに書く）。手で編集しない。出力が変わる変更をしたら作り直し、差分を確かめる。見本のテーマを変えたときは、`internal/theme/testdata/example-out/` の作り直しも要ることがある（`assets/css/style.css` を変えると、`?v=` が変わる）。

## 規約

- コードと設定ファイルのコメントは英語で、最小限にする。消すと誤った「修正」を招く箇所にだけ、短い1行で書く。
- doc コメントは、名前だけで分からないときだけ書く。公開する識別子のすべてには付けない。
- ドキュメントは日本語で書く。ただし `README.md` は英語で、バッジを除いて150字以内を目安にする。
- コミットは Conventional Commits。type は `feat`・`fix`・`docs`・`refactor`・`test`・`build`・`ci`・`chore` など（`update`・`add` は使わない）。説明と本文は日本語（例：`build: 開発環境の Nix flake を追加`）。
