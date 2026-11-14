# AGENTS.md

WordPress の代替となる CMS を Go で作る。設計は `docs/` の構想書が正なので、作業に関わる節を先に読む。構想書は、章と大きい節ごとのファイルに分けてあり（6.5 は `docs/06.05-blocks.md` など）、`docs/README.md` に、節とファイルの対応の表がある。節は、節番号（`6.5.2` など）で参照する。

## 構想書の扱い

- 構想書の 6.12（未決定）の項目の番号は、ほかの節や issue・PR から参照するので、変えない。項目は最後に足す（決まった項目の書き方は、6.12 の初め）。
- 並行した PR が、どちらも 6.12 に項目を足したときは、後にマージする側が、`main` に合わせ直すときに、自分の足した項目を、`main` の最後の番号の続きに振り直し、その PR の中の、振り直した項目を指す参照（構想書の「6.12の<番号>」、AGENTS.md の「6.12 の <番号>」、6.12 の中の「（<番号>）」）も直す。マージ済みの番号は変えない。

## 開発シェル

- `nix develop` で入る（direnv を使うなら、最初に一度 `direnv allow`）。
- ツールの版は flake で決まる（Go のマイナー版は `flake.nix`、それ以外は `flake.lock`）。CI も `nix develop -c` で同じ版を使う。
- 外部のサードパーティの Go のモジュールは使わない（依存の数え方と例外は、構想書の 1.2）。モジュールを足す前には、`golang.org/x/...` も、そのたびに、理由を添えてメンテナーに確かめる（一度認められたモジュールがあっても、ほかのモジュールは改めて確かめる）。

## よく使うコマンド

開発シェルの中で、リポジトリの直下で実行する。CI（`.github/workflows/ci.yml`）も同じ確認をする。

- `go build ./...`・`go vet ./...`・`go test -race ./...`
- `golangci-lint run`：リンター。設定は `.golangci.yml`（規則のことは「規約」）
- `golangci-lint config verify`：`.golangci.yml` を変えたとき（`run` は未知の項目を黙って無視する）
- `golangci-lint fmt`：整形（gofmt・goimports）
- `go mod tidy`
- `CGO_ENABLED=0 go build ./...`・`CGO_ENABLED=0 go test ./...`：リポジトリのすべてが cgo なしでビルドでき、テストが通ること（CI の `build without cgo`・`test without cgo`）
- `GOOS=darwin GOARCH=arm64 go vet ./...`・`GOOS=windows GOARCH=amd64 go vet ./...`：Linux のほかの OS だけのファイル（`cmd/whiteroom/terminal_other.go` など）と、テストのファイルが、その OS でコンパイルできること（CI の `vet for darwin`・`vet for windows`。その OS では、テストは動かさない）

`flake.nix` や `flake.lock` を変えたら、`nix fmt` と `nix flake check --all-systems` も実行する。

保存の層（構想書の 3.5）の長いテストとベンチマークは、CI では動かさない。

- `WHITEROOM_LONG_TESTS=1`：`internal/logdb` と `internal/store` の、クラッシュ・強制終了・失敗の差し込みの行列と、差分テスト（`TestDifferential`）を長くする。開発のマシン（16の CPU）で、`-race` なしで約5分かかる。
- `WHITEROOM_BENCH`：保存の層のベンチマーク（開発のマシンで約2時間）。動かし方は、構想書の 3.5 の「ベンチマークの条件」。
- dm-log-writes：記録（root が要る）と、記録の確かめ（`WHITEROOM_DMLOG`）の動かし方は、構想書の 3.5 の「テスト」。

WordPress の PHP との突き合わせのテスト（構想書の 6.10.5 の「テスト」）も、CI では動かさない。WordPress 7.1.4 のファイルを、リポジトリの外のディレクトリ（`<dir>`）に置いて環境変数で渡し、開発シェルの PHP で動かす。WordPress のファイル（GPL）とその出力は、リポジトリに入れない。

- `WHITEROOM_WP_PARSER`：`class-wp-block-parser.php`・`class-wp-block-parser-block.php`・`class-wp-block-parser-frame.php` を置き、`WHITEROOM_WP_PARSER=<dir> go test -count=1 -v -run '^TestWordPress' ./internal/wpblock` で動かす。
- `WHITEROOM_WP_FORMATTING`：`formatting.php`・`shortcodes.php`・`class-wp-embed.php` を置き、`WHITEROOM_WP_FORMATTING=<dir> go test -count=1 -v -run '^TestWordPress' ./internal/wpformat` で動かす。
- ファイルは、`gh api -H 'Accept: application/vnd.github.raw' 'repos/WordPress/wordpress-develop/contents/src/wp-includes/<ファイル名>?ref=7.1.4' > <dir>/<ファイル名>` か、`curl -fsSL -o <dir>/<ファイル名> https://raw.githubusercontent.com/WordPress/wordpress-develop/7.1.4/src/wp-includes/<ファイル名>` で取る。

ファズテストの探索（`-fuzz`）も、CI では動かさない。手で動かし（例：`go test ./internal/wpimport -run '^$' -fuzz '^FuzzBody$' -fuzztime 60s`）、見つかった入力のコーパス（`testdata/fuzz`）は、リポジトリに入れない。

ビルド（構想書の 4.3 の「メモリ」）と保存の操作（構想書の 6.12 の 150）のベンチマークも、CI では動かさない。

- `WHITEROOM_BUILD_BENCH=<結果のディレクトリ> go test -count=1 -run '^TestBenchmarks$' -timeout 0 ./internal/build`：開発のマシンで約75秒（Linux だけ）。段階ごとのヒーププロファイルも書く。`GOGC`・`GOMEMLIMIT`・`GODEBUG` は、ビルドのプロセスに渡り、`results.md` に残る。
- `WHITEROOM_EDIT_BENCH=<結果のディレクトリ> go test -count=1 -v -run '^TestBenchmarks$' -timeout 0 ./internal/edit`：開発のマシンで約170秒。`WHITEROOM_EDIT_BENCH_POSTS=0,1000` で、試す件数を決められる（約8秒）。
- どちらも、`-race` は付けず（付けると、失敗する）、`TMPDIR` を実際のディスクに置く。結果のディレクトリに、`results.md`・`raw.jsonl` を書く。

見本のサイト（`internal/build/testdata/site/`）をブラウザーで見るには、`go run ./cmd/whiteroom build -theme internal/theme/testdata/example -out <dir>/out -cache <dir>/cache internal/build/testdata/site` でビルドしてから、`go run ./cmd/whiteroom serve public <dir>/out` として、`http://127.0.0.1:8080/` を開く（Ctrl-C で止める）。`<dir>` はリポジトリの外にする（`-out`・`-cache` を省くと、見本のサイトの中の `out/`・`cache/` に書く）。

保存先（データのディレクトリ）から動かして、管理のAPIを呼ぶには、リポジトリの外の `<dir>` で、次のとおりにする（構想書の 9.4 の例と同じ流れ）。

- `go run ./cmd/whiteroom init -data <dir>/data -login admin -name Admin -email admin@example.com -theme internal/theme/testdata/example internal/build/testdata/site`：データのディレクトリと最初の管理者を作り、見本のサイトを取り込む。パスワードは、端末の促しに、2回打つ（コマンドの行には書かない）。
- `go run ./cmd/whiteroom serve admin -data <dir>/data -out <dir>/out -cache <dir>/cache -theme internal/theme/testdata/example internal/build/testdata/site`：待ち受けは、既定の `127.0.0.1:8081` で、起動のビルドが `<dir>/out` に公開する。起動の行のオリジン（`http://127.0.0.1:8081`）を `Origin` に書いて、API を `curl` で呼ぶ（`curl -c <dir>/cookies.txt -H 'Origin: http://127.0.0.1:8081' -H 'Content-Type: application/json' --data-binary @<dir>/login.json http://127.0.0.1:8081/api/session` でログインし、`-b <dir>/cookies.txt` で `http://127.0.0.1:8081/api/builds` などを読む。`login.json` は、権限 0600 のファイルに `{"login": "admin", "password": "…"}` と書く）。ポートが使用中なら `-addr 127.0.0.1:<ポート>`（`Origin` も合わせる）。Ctrl-C で止める。
- 別の端末で、`go run ./cmd/whiteroom serve public <dir>/out` として、`http://127.0.0.1:8080/` を開く。

`-out`・`-cache` を省くと、見本のサイトの中の `out/`・`cache/` に書く（上の段落と同じ）。API の呼び方は構想書の 9.7、ソケットで待ち受ける形（`-socket` と `-origin`、`curl --unix-socket`）と、同じことを設定ファイル（`-config`）で書く形は、9.4 の「例」と「設定の例」。

## パッケージの置き場所

- パッケージは `internal/` の下に置く（コマンド `whiteroom` は `cmd/whiteroom`。`init`・`build`・`rollback`・`serve public`・`serve admin`・`backup`・`doctor`・`account` で、構想書の 9.4）。役割は、それぞれのパッケージの doc コメントにある。主なものと、その構想書の節は、次のとおり。
- コンテンツモデルと検証は `internal/content`（6.1〜6.7・6.9。保存の操作とアカウントの操作が作る ID の `IDSource` も）。テーマ（5.4）の設定ファイルと翻訳ファイルの型・読み込み・検証も、コンテンツの検証が使うので、ここに置く（テーマのディレクトリを読み、テンプレートを実行するのは `internal/theme`）。
- 文中装飾とブロックの HTML と、見た目の CSS は `internal/render`（6.5・6.8）、`html`・`legacy` ブロックのサニタイズは `internal/sanitize`（6.5.4）、公開する画像と派生ファイルは `internal/imaging`（6.6.2）。
- 静的ビルドとリリースは `internal/build`（4.3・4.4）、サイトのディレクトリは `internal/sitedir`（4.3）、公開用の内蔵サーバーは `internal/serve`（9.5）、ビルドと保存の操作が同じコードで確かめる、エンティティをまたぐ検証とパスの計画は `internal/siteplan`（6.7・6.6.8）。
- 保存の操作と公開の操作は `internal/edit`（6.7）、保存先からのビルドは `internal/publish`（4.4）、保存先（Content Store）は `internal/store`（3.4）、そのエンジンは `internal/logdb`（3.5）。
- ログイン履歴とアクティビティログの記録の型・操作の一覧・鎖の頭の行と書き出しは `internal/audit`（7.4）、記録を足す・読む・確かめる口は保存先（`internal/store`）。保存先に書く本番のコードは、書き込みごとに記録を足す（足さない書き込みは、保存先が断る。7.4）。
- ログインアカウントと権限は `internal/account`（7.3）、管理用のサーバーと管理のAPIは `internal/admin`（9.6・9.7）。パスワードとトークンの平文は、保存先にも、エラーの文にも、応答と記録にも出さず、URL のパスとクエリにも入れない。例外は、セッションのトークンを渡す `Set-Cookie`（ログインと本人のパスワードの変更の200）と、招待（201）と再設定（200）の答えの本文の `token` だけ（7.3 の「保存」・9.6 の「記録」・9.7）。`cmd/whiteroom` のコマンドも、パスワードは標準入力だけから受け取り（端末では、エコーなしで読む）、引数・フラグ・環境変数・設定ファイルでは受け取らず、再設定のトークンを出力しない（9.4 の「秘密」）。
- WordPress からの取り込みは `internal/wpimport`（6.10.5。段階を呼ぶ順と回数は、6.10.5 の「流れ」）。メディアの変換（`Media`）は、原本を、サイトのディレクトリの `files/` に書く（ほかの変換は、Go の値を返すだけ。6.10.5 の「メディア」）。
- パッケージの依存の向き（どのパッケージを import してよいか）は、`.golangci.yml` の depguard の規則が正で、`golangci-lint run` が確かめる。向きは規則を直して変え、理由は、規則の `desc` に書く（違反の報告に出る）。
- 作るファイルか、原本を調べた結果が変わる変更をしたら、`internal/imaging/imaging.go` の `processingVersion` を上げる（キャッシュのキーとページの `?v=` に入るので、上げないと、キャッシュの古いファイルが使われる）。
- エンジンのファイルの形を変えるときは `internal/logdb/format.go` の `formatVersion` を、自作の層の表を変えるとき（索引の行が依る `content.SchemaVersion` と要約の項目、アカウントとセッションの行の JSON、監査の記録の JSON（`internal/audit`。7.4）を変えるときも）は `internal/store/logbackend.go` の `logSchemaVersion` を上げる。`main` ブランチに入った版の形と表は書き換えない（その版で書いたファイルを、後の版でも読むため。更新の仕方は、構想書の 3.4 の「表の版」・3.5 の「移行・バックアップ・リストアの条件」・7.3 の「保存」）。
- 取り込みの ID の作り方を変えるときは `internal/wpimport/id.go` の `idVersion`（文字列の最後の数）を上げる（取り込み直した ID が、本文のブロックの ID も、変わるので、構想書の 6.10.5 の ID の規則も合わせる）。

## 生成するファイル

次のファイルは、`go generate` か、テストの `-update`（`go test ./internal/<パッケージ> -run <テスト> -update` のように、パッケージの後ろに書く）で作り、手で編集しない。作るものが変わる変更をしたら作り直し、差分を確かめる。

- `go generate ./internal/nfc`・`go generate ./internal/yaml`・`go generate ./internal/html`：表とテストのデータを、Unicode の UCD・yaml-test-suite・HTML Standard と html5lib-tests と web-platform-tests から取得して作る（ネットが要る）。HTML Standard の構文解析の変更に追従するときは、構想書の 6.12 の 101。
- `go generate ./internal/webp`：開発シェルの libwebp と libvpx のツールで、`testdata/vectors/*.webp` と `vectors.json` を作る。`flake.lock` の更新で、これらのツールの版が変わったら作り直す（新しい版のツールは、違うファイルと期待値を書くことがある）。
- `go generate ./internal/build`：見本のサイト（`internal/build/testdata/site/`。構想書の 6.11）の `files/`（メディアの原本）と `media/`（メディアの文書）を作る。原本は Go の標準ライブラリの書き出しで決まるので、Go の版を上げて変わったら、`internal/build/testdata/out/` も作り直す。ほかの文書は手で書き、6.11 の例のものは、`internal/content/testdata/` の同じ例のファイルの写しなので、例を変えたら両方を変える。
- `internal/theme/testdata/example-out/`：`TestExamplePages` の正解。
- `internal/render/testdata/out/`：`TestGolden` の正解（`internal/render/classes.css` を変えると、`site.css` が変わる）。見本のテーマ（`internal/theme/testdata/example/`）を変えたときは、`internal/theme/testdata/example-out/` と `internal/build/testdata/out/` と `internal/wpimport/testdata/out-blocks/`・`out-classic/`（`render.txt`）の作り直しも要ることがある（`assets/css/style.css` を変えると、`?v=` が変わる。構想書の 5.4.7 と 6.12 の 80 の例の版も合わせる）。構想書の 5.4.9 の例は、見本のテーマのファイルの写しなので、どちらかを変えたら、両方を変える。
- `internal/build/testdata/out/`：`TestSampleSite` の正解で、`cmd/whiteroom`・`internal/serve`・`internal/edit`・`internal/publish` のテストも使う。Go のマイナー版を上げると、ビルドが作る画像の `?v=` が変わり、`manifest.txt` とほとんどのページが変わることがある。そのときも作り直し、それだけが変わったことを確かめる（サイトの CSS・ページの CSS・PDF・テーマの assets は変わらない）。構想書の 6.8 の例は、この出力の写しなので、どちらかを変えたら、両方を変える。
- `internal/wxr/testdata/*.json`：`TestRead` の正解。
- `internal/wpimport/testdata/` の `out/`・`out-blocks/`・`out-classic/`：`TestImportSample`・`TestBodies`・`TestClassicBodies` の正解。`-run` を付けないと、3つとも書き直す。
- テストの WXR などは手で書き、WordPress の書き出し・応答と、WordPress（と Contact Form 7）のコードの文は写さない（第三者のデータを使わない。仕様から作った第三者のデータの扱いは、構想書の 1.2）。

## 規約

- コードと設定ファイルのコメントは英語で、最小限にする。消すと誤った「修正」を招く箇所にだけ、短い1行で書く。
- doc コメントは、名前だけで分からないときだけ書く。公開する識別子のすべてには付けない。
- ドキュメントは日本語で書く。ただしリポジトリの直下の `README.md` は英語で、バッジを除いて150字以内を目安にする。
- コミットは Conventional Commits。type は `feat`・`fix`・`docs`・`refactor`・`test`・`build`・`ci`・`chore` など（`update`・`add` は使わない）。説明と本文は日本語（例：`build: 開発環境の Nix flake を追加`）。
- 機械で確かめられる規約は、`.golangci.yml` の golangci-lint の規則にする（import の向きと外部のモジュールは depguard、テストだけが使う識別子は forbidigo、`go.mod` の行は gomoddirectives）。このモジュールのパッケージを import するパッケージを足すときは、depguard に、そのパッケージの規則を足し、規則 `stdlib-only` から、そのディレクトリを除く（書き方は `.golangci.yml` のコメント）。
- 規則を足すか直したら、`golangci-lint config verify` と `golangci-lint run` を通し、規則ごとに1度わざと破って、落ちることを確かめる（`files` が、どのファイルにも当たらなくても、エラーにならないため）。
- golangci-lint は、linux で、ビルドタグを付けないときにビルドするファイルだけを見るので、ほかの OS だけのファイル（`lock_other.go`・`cmd/whiteroom/terminal_other.go` など）と、タグを付けたときだけ入るファイル（`race_test.go` など）は、どの規則も確かめない。
