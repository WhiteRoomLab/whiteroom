## 9. Linuxでの配布・実行

### 9.1 決定済み

- AWSや特定クラウドに依存せず、**Linuxにインストールして利用可能**とする。
- **実装言語はGo**とする。
- コマンドの**名前は `whiteroom`** にし、9.2のコマンドは、そのサブコマンド（`whiteroom build`・`whiteroom serve public` など）にする（9.4）。
- `whiteroom serve public` は、**公開用の内蔵サーバー**にする（9.4・9.5。ヘッダーとCSPは6.12の122、変更を見て作り直すかは6.12の123）。

### 9.2 提案段階

- 単一バイナリ、または少数の実行ファイルで配布する。
- 実行ファイルは、cgoを使わずにビルドできる（`CGO_ENABLED=0`。3.4の「依存」。静的リンクとクロスビルドは6.12の126）。
- systemdユニットを同梱し、Linuxの標準ディレクトリ体系に合わせて配置する。
- 内蔵HTTP/HTTPSサーバーを標準とし、必要に応じてNginx/Apache/Caddy等の背後でも動作させる。
- **公開用と管理用を別プロセス・別Unixユーザー**で起動できるようにする。
  - 分け方（案。9.7）：管理用のサーバー（書くプロセス。ビルドもその中で動く）が、データのディレクトリ（ディレクトリ0700・ファイル0600。3.4）を読み書きし、出力先（ディレクトリ0755・ファイル0644。4.3）を書く。公開用のサーバー（9.5）は、出力先だけを読み、保存先は開かない。そのため、データのディレクトリは管理用のユーザーだけのもの、出力先は管理用のユーザーが書き、公開用のユーザーが読めるものにする。管理用のユーザーの umask が 022 より厳しいと、出力先のモードが 0644・0755 より狭くなり（umaskの許す範囲で作る。4.3）、公開用のユーザーが読めない。設定の例は #49。
- `whiteroom init`、`whiteroom build`、`whiteroom serve public`、`whiteroom serve admin`、`whiteroom backup`、`whiteroom doctor` などのコマンドを設ける（`whiteroom` のサブコマンドにすることは決定済み。9.1。名前と、作ったものは9.4）。`init` は、最初の管理者のアカウントも作る（案。7.3。#49）。
- Ubuntu、Debian、Fedora、NixOSなどへの配布形式を検討する。
- 本番運用時にPHP、Node.js、外部DBサーバーなどを必須としない構成を目指す。

### 9.3 TLS・証明書（提案段階）

- 公開サイト向けにACMEによる証明書取得・自動更新機能を内蔵する。
- TLS暗号プロトコルや暗号アルゴリズムは、実績のある標準実装を採用する。
- 自前CAは公開Webサイトの標準方式にはしない。内部管理通信等の用途なら別途検討する。

独自の証明書**管理機構**は構想に残すが、独自のTLS**暗号実装**を行うことは決定していない。

### 9.4 コマンド（案）

コマンド `whiteroom` を、`cmd/whiteroom` に作った。サイトをビルドして公開する `whiteroom build`（4.3）と、公開中のリリースを配信する `whiteroom serve public`（内蔵サーバー。9.5）がある。この節の規則は、「決定済み」と書いたもの（9.1）のほかは、どれも仮に決めたもの（案）。

**名前とサブコマンド**

- 名前は `whiteroom` にする（決定済み。9.1）。リポジトリとGoのモジュール（`github.com/WhiteRoomLab/whiteroom`）と同じ名前で、コードを `cmd/whiteroom` に置くので、`go build`・`go install` で作る実行ファイルも、この名前になる。
- 9.2のコマンドは、`whiteroom` のサブコマンドにする（決定済み。9.1）。

| コマンド | 内容 |
| --- | --- |
| `whiteroom build [フラグ] [SITE]` | ディレクトリ `SITE` のサイトをビルドして、公開する（下記） |
| `whiteroom serve public [フラグ] [OUT]` | 出力先 `OUT` の公開中のリリースを、内蔵サーバー（9.5）で配信する（下記） |
| `whiteroom help` | 使い方を示す（`whiteroom -h`・`-help`・`--help` も同じ） |
| `whiteroom init`・`whiteroom doctor`・`whiteroom backup`・`whiteroom serve admin` | まだ無い（9.2。`init` で作るものは6.12の69と、最初の管理者の7.3、`doctor` は6.9、`serve admin` の土台は9.6で、コマンドは #49） |

前のリリースに戻す操作と、ビルドを止めて再開する操作（4.4の「戻す操作」）のコマンドは、保存先からのビルドのコマンドとともに、まだ無い（#49。6.12の108）。ビルドの後にNginxに設定を読み込み直させるコマンド（6.12の113）も、まだ無い。

規則：

- フラグ：Goの `flag` パッケージの書き方（`-out DIR`・`-out=DIR`。`--out` も同じ）で、引数（`SITE`・`OUT`）より前に書く。引数より後ろのものは、引数とみなす（引数が多すぎる誤り（`whiteroom build: too many arguments: give at most one SITE, after the flags`）になる）。
- 使い方：引数の無い `whiteroom` は、使い方を標準エラーに、`whiteroom help` は標準出力に示す。サブコマンドの `-h`（`whiteroom build -h`・`whiteroom serve public -h`）は、そのサブコマンドの使い方とフラグを、標準エラーに示す。
- 知らないもの：知らないコマンドには、`whiteroom: unknown command "x"` と `Run "whiteroom help" for usage.` を示す。`whiteroom serve` の後ろには、`public` を書く（`admin` には `whiteroom serve: admin is not available yet` を、`help`・`-h`・`-help`・`--help` には `whiteroom help` と同じ使い方を、ほかには `whiteroom serve: unknown server "x"` と `Run "whiteroom help" for usage.` を示す。`whiteroom serve` だけなら、使い方を示す）。

**終了コード**

| コード | 意味 |
| --- | --- |
| `0` | 成功（使い方を求めた `whiteroom help`・`-h` も） |
| `1` | 失敗（サイトの問題、ビルドや配信のエラー、取り消したビルド） |
| `2` | 使い方の誤り（引数の無い `whiteroom`、知らないコマンドやフラグ、まだ無い `whiteroom serve admin`、多すぎる引数、範囲の外の値） |

- 使い方の誤りを2に、`-h` を0にするのは、Goの `flag` パッケージと同じ。

**`whiteroom build`**

```text
whiteroom build [-out DIR] [-cache DIR] [-theme DIR] [-workers N] [-memory MiB] [SITE]
```

サイトのディレクトリ `SITE` のサイトを、このプロセスの中でビルドして（4.3）、出力先に公開する。

| 引数・フラグ | 内容 | 省略したとき |
| --- | --- | --- |
| `SITE` | サイトのディレクトリ（4.3の「サイトのディレクトリ」） | `.`（作業ディレクトリ） |
| `-out DIR` | 出力先のディレクトリ（4.3の「出力先」） | `SITE/out` |
| `-cache DIR` | 画像のキャッシュのディレクトリ（4.3の「画像のキャッシュ」） | `SITE/cache` |
| `-theme DIR` | `SITE/theme` の代わりに使うテーマのディレクトリ（5.4.1） | `SITE/theme` |
| `-workers N` | ページの描画などを、同時に行う数。0以上 | `0`（Goの `GOMAXPROCS` の値） |
| `-memory MiB` | 画像のファイルを作るときの、メモリの見積もりの上限（6.6.2）を、MiBで。0から8796093022207まで（バイトにして、64ビットの整数に入る数） | `0`（1024MiB） |

規則：

- 既定の置き場所：出力先とキャッシュは、既定では、サイトのディレクトリの中に並べる（キャッシュは、出力先の中には置けない。4.3の「画像のキャッシュ」）。サイトのディレクトリは、4.3の表に無いもの（`out/`・`cache/` も）を無視するので、サイトのディレクトリで `whiteroom build` とするだけで、ビルドし直せる。キャッシュのディレクトリの扱い（プレビューと本番のビルドで分けるかなど）は6.12の104。
- 相対パス：`-out`・`-cache`・`-theme` の相対パスは、作業ディレクトリからのもの（`SITE` からではない）。
- `-theme`：見本のサイト（6.11。`internal/build/testdata/site/`）は `theme/` を持たず、見本のテーマ（`internal/theme/testdata/example/`。5.4.9）でビルドするため。テーマを作るときに、サイトにコピーせずに試すのにも使える。`-theme` のときは、`SITE` の `theme/` を読まない（無くてもよい）。
- 範囲：負の `-workers` と、範囲の外の `-memory` は、使い方の誤り（2）にする（`whiteroom build: -workers -1 is out of range (0 or more)`、`whiteroom build: -memory -1 is out of range (0 to 8796093022207)`）。
- 同じプロセス：テーマを適用するビルドを、別のプロセスで動かして、時間とメモリに上限をかけること（5.3）は、まだしない（いまのテーマは、開発者が書くもの）。止めるのは、取り消し（下記の「ビルドの取り消し」）だけ。
- サイトのURL：ビルドは、サイト設定の `url`（6.6.8）のまま書き、コマンドで変えることはしない（環境ごとの値は6.12の68）。ページのリンクと、CSS・画像のURLは、パスで書く（6.5.2・5.4.4）ので、手元の `whiteroom serve public`（下記）でも、ページを開いて、たどれる。本体が `<head>` に書くURL（正規URL・言語の対応・フィード・アイコン・OGP・構造化データ。6.8）と、フィードとサイトマップの中のURL（6.8）は、サイトのURLのまま（`https://example.com/` など）。

**ビルドの前の確認**

- ビルドを始める前に、`SITE` を開き、サイト設定の2つのファイル（`site/settings.json`・`site/schema.yaml`）を読み、テーマのディレクトリを開く。できなければ、ビルドを始めずに、そのエラー（下記の「ビルドの出力」）を示して、1で終わる（`site/schema.yaml: missing file` と `site/settings.json: missing file`、`theme: missing directory`、`whiteroom build: open nope: no such file or directory`）。
- ビルドは、初めの段階で、出力先とキャッシュのディレクトリを作る（4.3の「段階」の1）。先に確かめるのは、サイトでないディレクトリで動かしたときに、そこに `out/` と `cache/` を作らないため。

**ビルドの出力**

- 警告とエラー：サイトの問題（警告と、ビルドを止めた問題）は、標準エラーに、4.3の「エラーと警告」の形と順のまま、1行に1つ示す。ファイルは、作業ディレクトリからのパスにする（`SITE` の後ろに、サイトのディレクトリからのパスを続ける。`-theme` のときは、`theme` と `theme/` の下のパスを、そのディレクトリからのパスにする）ので、示したパスで、そのファイルを開ける。`SITE` が `.` なら、4.3のとおりの行になる。警告とエラーは同じ形で、前に `warning:` などを付けない（警告の数は、結果の1行で示す。警告では、ビルドを止めない。どの警告で止めるかは6.12の82）。例（リポジトリの直下で、`whiteroom build -theme internal/theme/testdata/example -out /tmp/sample/out -cache /tmp/sample/cache internal/build/testdata/site` としたときの警告の一部）：

```text
internal/build/testdata/site/menus/mnu_01JC0000000000000000000002.json:5:3: /location: the theme has no location "sidebar"; the menu is left out
internal/theme/testdata/example: no translation in en for the key "postedBy"; pages in en use its text in ja, the default language of the theme
```

- ほかのエラー：サイトの問題でないもの（4.3の「エラーと警告」の「ほかのエラー」）は、前に `whiteroom build: ` を付けて示す（`whiteroom build: another build is running in out`）。まとめたエラー（ビルドの作ったものでない、出力先のファイルなど。4.3の「出力先」）は、1つずつ1行に示す。
- 結果：公開したら、標準出力に、結果（4.3の「渡すものと結果」）を1行で示す。`current` を切り替えたとき（`Changed`）は1行目、同じリリースが、すでに公開中だったときは2行目の形にする。

```text
published release <名前> in <出力先>: <P> pages, <F> files, <W> warnings
release <名前> in <出力先> is already published: <P> pages, <F> files, <W> warnings
```

- `<P>` は書いたページの数（`Pages`）、`<F>` は公開ディレクトリのファイルの数（`Files`）、`<W>` は警告の数（警告が無ければ、`, <W> warnings` を書かない）。1のときは、単数にする（`1 warning` など）。出力先は、渡したもの（既定では、`SITE` の後ろに `out` を続けたもの。`SITE` が `.` なら `out`）。
- 公開の後の失敗（4.3の「リリースと切り替え」）では、警告と結果の1行の後に、エラーを示して、1で終わる。ほかの失敗では、エラーだけを示し（ビルドは、失敗したときに結果を返さないので、警告も示さない。4.3の「渡すものと結果」）、標準出力に何も書かない。

**ビルドの取り消し**

- Ctrl-C（SIGINT）か SIGTERM で、ビルドを取り消す（4.3の「段階」の取り消し）。取り消したビルドは、`whiteroom build: interrupted` を示して、1で終わる。出力先は、前のままにする（4.3の「失敗したとき」）。`current` を切り替えた後は、取り消さずに、公開を終えて、結果を示す（4.3）。
- 2回目のシグナルでは、取り消しを待たずに、すぐに終わる（残った一時のものは、次に成功したビルドが消す。4.3の「出力先」）。

**`whiteroom serve public`**

```text
whiteroom serve public [-addr ADDR] [OUT]
```

出力先 `OUT` の公開中のリリースを、止めるまで、内蔵サーバー（9.5）で配信する。公開用のサーバー（9.2。決定済み。9.1）で、手元での確認にも使う。出力先は読むだけで、何も書かない。

| 引数・フラグ | 内容 | 省略したとき |
| --- | --- | --- |
| `OUT` | 出力先のディレクトリ（4.3の「出力先」） | `out`（サイトのディレクトリで `whiteroom build` としたときの出力先） |
| `-addr ADDR` | 待ち受けるTCPのアドレス（`host:port`。`:8080` は、すべてのインターフェース。ポートの `0` は、空いているもの） | `127.0.0.1:8080`（同じマシンからだけ開ける） |

規則：

- 起動：出力先が無いとき（`whiteroom serve public: open /srv/site: no such file or directory`）、公開中のリリースが無いとき（`whiteroom serve public: no release is published in /srv/site`）、そのリリースが正しくないとき（`current` がリリースを指さない、`server.json` を読めないか正しくないなど。9.5）、待ち受けられないとき（`whiteroom serve public: listen tcp 127.0.0.1:8080: bind: address already in use`）は、配信を始めずに、1で終わる。出力先のエラーでは、出力先を絶対パスで示す。
- URL：待ち受けを始めたら、標準出力に、開くURLを1行で示す（`serving out at http://127.0.0.1:8080/`）。ホストとポートは、待ち受けたもの（ポートの `0` は、決まった番号にする。`0.0.0.0`・`::` は、`localhost` にする）。パスは、公開中のリリースのサイトのURLのパス（`server.json` の `base`。6.6.6）で、`/blog` のサイトでは `http://127.0.0.1:8080/blog/` になる（ASCII以外の文字・空白・制御文字のバイトは、16進の大文字でエンコードする）。示すのは起動のときだけで、後のビルドでサイトのURLのパスが変わっても、示し直さない。
- 作り直さない：変更を見て、ビルドし直すことはしない。`whiteroom build` で公開し直すと、起動し直さなくても、次のリクエストから、新しいリリースを配信する（9.5）。手元での作業のために、変更を見て作り直すかは6.12の123。
- 止める：Ctrl-C（SIGINT）か SIGTERM で、待ち受けをやめ、処理中のリクエスト（ダウンロードの途中のものも）が終わるのを10秒まで待って（終わらないものは、接続を閉じる）、何も示さずに、0で終わる。2回目のシグナルでは、すぐに終わる。
- 標準エラーには、リクエストのエラーを記録する（9.5の「エラーの記録」）。

**例**

見本のサイト（6.11。`internal/build/testdata/site/`）の写しに、見本のテーマ（`internal/theme/testdata/example/`）を `theme/` として置いたディレクトリで：

```text
$ whiteroom build
menus/mnu_01JC0000000000000000000001.json:10:5: /items/2: entry "ent_01JC0000000000000000000003" is not published; the item and its children are left out
menus/mnu_01JC0000000000000000000002.json:5:3: /location: the theme has no location "sidebar"; the menu is left out
redirects/rdr_01JC0000000000000000000006.json:5:3: /to: entry "ent_01JC0000000000000000000003" is not published; the redirect is left out
revisions/rev_01JB8X9Y3ZE5G8K1N4Q7S2V6W9.json:5:3: /template: the theme has no page template "standard"; rendered with entry.html
theme: no translation in en for the key "postedBy"; pages in en use its text in ja, the default language of the theme
published release … in out: 50 pages, 69 files, 5 warnings
$ whiteroom serve public
serving out at http://127.0.0.1:8080/
```

- もう一度 `whiteroom build` とすると、同じ警告の後に、`release … in out is already published: 50 pages, 69 files, 5 warnings` を示す（名前は、1回目と同じ）。
- リリースの名前（`…`）は省いた。名前は中身から作る（4.3の「リリースと切り替え」）ので、出力が変わると（Goのマイナー版を上げたときも）変わるため。

### 9.5 内蔵サーバー（案）

公開用の内蔵サーバー（9.2。決定済み。9.1）を、`internal/serve` に作った。`whiteroom serve public`（9.4）が動かし、ビルドが出力先に公開したリリース（4.3）を、そのリリースの `server.json`（6.6.6）の、リダイレクト・404のページ・ディレクトリの索引のとおりに配信する。この節の規則は、「決定済み」と書いたもの（9.1）のほかは、どれも仮に決めたもの（案）。

**配信するもの**

- `<出力先>/current` の指すリリースの、公開ディレクトリ（`releases/<名前>/public/`。4.3の「出力先」）のファイルを、サイトのURLのパス（`server.json` の `base`）の下で配信する。
- `current` は、リクエストのたびに読む。指すリリースが変われば、そのリリースの `server.json` を読む（同じリリースのあいだは、読んだものを使う）。そのため、ビルドが `current` を切り替えると（4.3の「リリースと切り替え」）、起動し直さなくても、次のリクエストから、新しいリリースを、その `server.json` で（リダイレクト・404のページ・サイトのURLのパスも新しくして）配信する。1つのリクエストには、1つのリリースで答える。`current` は、たどらずに自分で読み（`readlink(2)`）、そのリリースのファイルは、`current` を通らずに、`releases/<名前>/` から開く（Goの `os.Root`）。そのため、切り替えの瞬間にも、どのリクエストも、前のリリースか新しいリリースで答え、切り替えのために404や503を返すことは無い（4.3の「切り替えの瞬間」）。Nginxでは、読み込み直しが要る（6.6.6・6.12の113）。
- `server.json` は、読んだときに、ビルドが書く形かを確かめる（知らない項目、正規の形でない `base`・`from`（6.6.6）、301と302でない `status`、空の `index` などは不可）。正しくなければ、そのリリースへのリクエストに、500を返す。

**応答の順**

リクエストには、次の順に答える（どれかで答えたら、そこで終わる）。

1. ヘッダー：どの応答にも、下記の「ヘッダー」の2つを付ける（転送・404・エラーにも）。
2. メソッド：GETとHEADだけに答える。ほかのメソッドには、405（`Allow: GET, HEAD`。本文は `405 method not allowed`）を返す。フォームの送り先（`/_forms/<フォームのID>`。6.6.5・8.2）へのPOSTも、お問い合わせの処理（8章）ができるまでは、405になる（6.12の86）。
3. リリース：`current` の指すリリース（上記）。公開中のリリースが無ければ、503（`503 service unavailable`）を返す。`current` がリリースを指さないときと、`server.json` を読めないか、正しくないときは、500（`500 internal server error`）を返す。どちらも記録する（下記の「エラーの記録」）。
4. サイトのパス：リクエストのパス（送られたままの、パーセントエンコードした形）を `/` で区切り、サイトのURLのパス（`base`）の区切りと、1つずつ、パーセントエンコードを戻して比べる（下記の「サイトのURLのパス」）。
   - 一致しなければ、サイトの外として、ただの404（本文は `404 page not found`）を返し、サイトの404のページは返さない（`/blog` のサイトでは、`/`・`/blogx/`・`/Blog/` など）。
   - サイトのURLのパスそのもの（末尾の `/` の無い `/blog`）には、`/` を付けたパスへの301を返す（クエリは残す）。NginxとApacheと同じく、`/blog?p=123` は、まず `/blog/?p=123` へ転送する（6.6.6の「NginxとApacheでの照合」の末尾の `/`）。
   - その下のパスでは、サイトのURLのパスの後ろ（`/blog/about/` では `/about/`）を、`from` の正規の形（6.6.6）にして、5〜7で使う。サイトのURLにパスが無ければ、リクエストのパスのすべてを、正規の形にする。
5. リダイレクト：サイトのURLからのパスに、クエリがあれば `?` とクエリを続けたものを、`server.json` の `redirects` と照合する（下記の「リダイレクトの照合」）。一致すれば、その `to` を `Location` に、その `status`（301か302）をステータスにして、本文の無い応答を返す。リクエストのクエリは付けない（6.12の55）。ファイルより先に照合するので、`/` にページがあっても、`/?p=123` は転送する（クエリの無い `from` は、出力するパスと重ならない。6.6.8の「出力パスの重複」）。
6. ファイル：パスの区切りを戻して、公開ディレクトリのファイルの名前にする（公開ディレクトリを開けなければ、500を返す）。
   - `/` で終わるパスには、そのディレクトリの、`server.json` の `index` の名前（`index.html`、次に `index.xml`。6.6.6）のうち、最初にあるふつうのファイルを返す。索引の無いディレクトリは、ファイルが無いものとする（ディレクトリの一覧は返さない）。
   - ほかのパスには、その名前のファイルを返す。ディレクトリなら、`/` を付けたパスへの301を返す（クエリは残す。6.6.8の「末尾の `/`」）。
   - ファイルには、`Content-Type`（下記）・`Content-Length`・`Last-Modified`（ファイルの更新日時）を付け、範囲のリクエスト（`Range`。206）と、条件付きのリクエスト（`If-Modified-Since` の304など）にも答える。`ETag` と `Cache-Control` は、まだ付けず、圧縮（`Content-Encoding`）もしない（6.12の122）。
7. 404：ファイルが無ければ、`server.json` の `notFound` のうち、`prefix` がパスの先頭に一致する最初のもの（長いものから並ぶ。6.6.6）の `page`（言語ごとの404のページ。6.6.8）を、ステータス404で返す。一致するものが無いか、そのページのファイルが無ければ、ただの404を返す。

- 転送の先：4と6の転送の `Location` は、送られたままのパスに、`/` と、あればクエリを続けたもので、スキームとホストは付けない（NginxとApacheは付ける。6.6.6）。5の `Location` は、`server.json` の `to` のまま（サイトの中の行き先は、サイトのURLのパスを付けたパス）。
- HEADには、GETと同じヘッダーを、本文なしで返す。

**サイトのURLのパス**

- 区切りごとに戻して比べるのは、`server.json` の `base` が `from` の正規の形で、`(`・`)`・`'` をそのまま持つ（`https://example.com/a(b)` のサイトでは `/a(b)`）が、描画するリンクと転送の先では、これらをエンコードする（`/a%28b%29/about/`。6.5.2）ため。正規の形では、`%28` と `(` は違う（6.6.6）ので、文字列のまま比べると、このサイトのリンクが、どれも404になる。区切りごとに戻して比べれば、`/a%28b%29/` と `/a(b)/` は、どちらもこのサイトになる。
- 区切りごとなので、エンコードした `/`（`%2F`）は、区切りにならない（`https://example.com/a%2Fb` のサイトに、`/a/b/` は一致しない）。大文字と小文字は区別する（`/Blog/` は、`/blog` のサイトの外）。

**リダイレクトの照合**

- 6.6.6の「照合」のとおりに、リクエストのパスとクエリを正規の形にして比べる（一致するものが複数あれば、パラメーターの多いものを、同じ数なら `from` の文字列の順で先のものを使う）。NginxとApacheは、パスを戻してから比べ、クエリを、ブラウザーが送る形とだけ比べる（6.6.6の「NginxとApacheでの照合」）ので、次のリクエストでは、答えが違う（6.12の112）。
  - `/a+b/`：`/a%2Bb/` と `/a+b/` からのリダイレクトがあれば、`/a+b/` からのものになる（`+` と `%2B` は違う）。NginxとApacheは、戻すと同じになるので、`/a%2Bb/` からのものにする。
  - `/%24var`：`/$var/` からのリダイレクトに一致しない（`%24` と `$` は違う）。NginxとApacheは、一致させる。
  - `/?q=%e6%96%b0`（16進の小文字）：`/?q=新` からのリダイレクトに一致する。NginxとApacheは、`q=%E6%96%B0` にだけ一致させる。
  - NginxとApacheの設定に書かないリダイレクト（`/a%2Fb/`・`/a%01b/`・`/a//b/`・`/%FF/`・`/.htaccess?x=1` からのものなど。6.6.6の「書かないもの」）も、`server.json` にあるので、転送する。
- 速さ：1つのリクエストで比べるのは、そのパスの、クエリの無いものと、リクエストのパラメーターで引いたものだけなので、1つのパスに何万ものリダイレクト（取り込んだサイトの `/?p=123` の形のもの。6.10.4）があっても、遅くならない（6.12の113のNginxとApacheと違う）。

**配信しないもの**

- 隠しファイルなど：戻した区切りが、`.` で始まるもの（`.htaccess`・`.git`。`.`・`..` も）、空のもの（`//x`）、`/` を含むもの（`%2F`）、制御文字を含むもの（`%00` など）、UTF-8として正しくないもの（`%FF`）、255バイトを超えるもの（6.6.8の区切りの上限）のパスは、ファイルを探さずに、ファイルが無いもの（7の404）とする。パスの `.`・`..` を解決してから探すことはしない（`/a/../b` も404）。ビルドは、`.htaccess`（Apacheのためのもの。6.6.6）のほかに、こうした名前のファイルを出力しない（5.4.1・5.4.7・6.6.2・6.6.8）。
- 公開ディレクトリの外：公開ディレクトリの外のもの（リリースの直下の `server.json`・`nginx.conf` など）は返さない。公開ディレクトリの中のシンボリックリンクは、中を指すものだけをたどる。外を指すもの（絶対パスのものも）、ループしたもの、読めないもの（権限の無いものなど）は、返さずに500にして、記録する。名前付きパイプなど、ふつうのファイルとディレクトリのほかのものは、開かずに（開くと待つことがあるため）、無いものとする。

**`Content-Type`**

- ファイルの名前の拡張子（大文字と小文字は区別しない）で、次の表から決める（404のページも）。表に無いもの（拡張子の無いものも）は、`application/octet-stream` にする。システムの `mime.types` などは読まない（同じリリースを、どのマシンでも、同じ `Content-Type` で配信するため）。中身から推測もしない。

| 拡張子 | `Content-Type` |
| --- | --- |
| `.html`・`.htm` | `text/html; charset=utf-8` |
| `.css` | `text/css; charset=utf-8` |
| `.js`・`.mjs` | `text/javascript; charset=utf-8` |
| `.json`・`.map` | `application/json` |
| `.webmanifest` | `application/manifest+json` |
| `.xml` | `application/xml`（フィードとサイトマップ。6.6.8） |
| `.txt` | `text/plain; charset=utf-8` |
| `.csv` | `text/csv; charset=utf-8` |
| `.pdf` | `application/pdf` |
| `.jpg`・`.jpeg` | `image/jpeg` |
| `.png` | `image/png` |
| `.gif` | `image/gif` |
| `.webp` | `image/webp` |
| `.avif` | `image/avif` |
| `.svg` | `image/svg+xml` |
| `.ico` | `image/x-icon` |
| `.woff` | `font/woff` |
| `.woff2` | `font/woff2` |
| `.ttf` | `font/ttf` |
| `.otf` | `font/otf` |
| `.mp4` | `video/mp4` |
| `.webm` | `video/webm` |
| `.mp3` | `audio/mpeg` |
| `.wasm` | `application/wasm` |
| ほか | `application/octet-stream` |

**ヘッダー**

- サーバーの返すどの応答にも、次の2つを付ける（Goの `net/http` が、上記の「応答の順」より前に返すもの（壊れたリクエストへの400、`OPTIONS *` への200）には付かない）。

```text
X-Content-Type-Options: nosniff
Content-Security-Policy: script-src 'self'; style-src 'self'; object-src 'none'; base-uri 'self'
```

- `nosniff`：ブラウザーは、`Content-Type` のとおりに扱い、中身から推測しない。
- CSP：スクリプトとCSSは、サイトのもの（同じオリジンのファイル）だけを使える。ブラウザーは、インラインのスクリプト（`<script>` の中身・イベントハンドラーの属性・`javascript:` のURL）と、インラインのスタイル（`style` の属性・`<style>` の要素）と、ほかのサイトのスクリプトとCSSを止める。`object-src 'none'` は `<object>`・`<embed>` を、`base-uri 'self'` は、ほかのサイトを指す `<base>` を止める。
- ビルドの出力は、インラインのスタイルを使わない（6.5.3の「出力」）ので、このCSPのまま動く。テーマや本体の変更が、インラインのスタイルを持ち込めば、ブラウザーが止めて、コンソールに示すので、6.5.3の規則を確かめることにもなる。
- 構造化データの `<script type="application/ld+json">`（6.8）は、実行しないデータなので、止まらない。
- テーマは、インラインのスクリプトと、ほかのサイトのスクリプトとCSS（Webフォントのサービスのものなど）を使えない（スクリプトとCSSは、`assets/` に置く。5.4.7）。`html` ブロックのスクリプトを許すかは6.12の6。
- 画像・iframe・フォント・動画などは制限しない（`default-src` を書かない）。埋め込み（6.5.4）は、ほかのサイトのiframeで、`html`・`legacy` の画像と、`html` のiframeにも、ほかのサイトのものがある（6.12の98・99）ため。
- 書き出すNginxとApacheの設定（6.6.6）には、いまは、これらのヘッダーを書かない。CSPを変えられるようにするか（インラインのスクリプトや、ほかのサイトのスクリプトの要るテーマのため）と、ほかのヘッダーは6.12の122。

**HTTPと時間の上限**

- 暗号化しないHTTP（HTTP/1.1）だけで、TLS（HTTPS。9.3）は、まだ無い。
- 時間の上限（Goの `http.Server` の項目）：

| 上限 | 時間 |
| --- | --- |
| リクエストのヘッダーを受け取るまで（`ReadHeaderTimeout`） | 10秒 |
| 本文も含めて、リクエストを受け取るまで（`ReadTimeout`） | 30秒 |
| 次のリクエストを待つ、使い回す接続（`IdleTimeout`） | 120秒 |
| 応答を送り終えるまで（`WriteTimeout`） | なし |

- ヘッダーと本文の上限は、少しずつ送るクライアントや、送ると言った本文を送らないクライアント（本文は使わないが、Goの `net/http` は、応答の前に読む）が、接続を持ち続けないため。応答の送信には上限を付けない。遅い回線での大きいファイルのダウンロード（30秒を超えるものも）を切らないため。受け取らないクライアントも接続を持ち続けられることは、6.12の124。

**エラーの記録**

- リリースの問題（上記の3）と、ファイルの問題（公開ディレクトリを開けない、外を指すシンボリックリンク、読めないファイルなど。404のページのファイルのものも）は、標準エラーに、日時・メソッド・リクエストのターゲット（`"` で囲む。制御文字や双方向の書式文字を含むことがあるため）・エラーを、1行に記録する（`2026/11/03 01:02:03 GET "/": no release is published in /srv/site`）。Goの `net/http` のエラーも、日時を付けて、同じく書く。
- 404のページのファイルが読めないとき（外を指すリンクなど）は、記録して、ただの404を返す。
- ほかの応答（200・206・301・302・304・404・405など）は記録しない。アクセスログは、まだ無い（6.12の122）。
