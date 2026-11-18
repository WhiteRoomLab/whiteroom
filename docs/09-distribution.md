## 9. Linuxでの配布・実行

### 9.1 決定済み

- AWSや特定クラウドに依存せず、**Linuxにインストールして利用可能**とする。
- **実装言語はGo**とする。保存の層のエンジンの核だけは、Ada/SPARKで書く（3.6。まだ作っていない）。
- コマンドの**名前は `whiteroom`** にし、9.2のコマンドは、そのサブコマンド（`whiteroom build`・`whiteroom serve public` など）にする（9.4）。
- 保存の層のデーモン（DBのデーモン。3.6）は、`whiteroom` のサブコマンドにせず、**別の実行ファイル**にする（名前は、仮に `whiteroom-db`。作った。`cmd/whiteroom-db`。9.4）。核をcgoで入れるのは、デーモンだけにして、`whiteroom` を、cgoなしでビルドできるままにするため（3.6の「実行ファイル」）。
- `whiteroom serve public` は、**公開用の内蔵サーバー**にする（9.4・9.5。ヘッダーとCSPは6.12の122、変更を見て作り直すかは6.12の123）。

### 9.2 提案段階

- 単一バイナリ、または少数の実行ファイルで配布する。
- 実行ファイルは、cgoを使わずにビルドできる（`CGO_ENABLED=0`。3.4の「依存」。静的リンクとクロスビルドは6.12の126）。DBのデーモンの実行ファイル（9.1）は、核を入れた後は、cgoでリンクする（`whiteroom` は、cgoなしのまま）。
- systemdユニットを同梱し、Linuxの標準ディレクトリ体系に合わせて配置する（デーモン・管理用のサーバー・公開用のサーバーの3つ。まだ書いていない。6.12の262）。
- 内蔵HTTP/HTTPSサーバーを標準とし、必要に応じてNginx/Apache/Caddy等の背後でも動作させる。
- **公開用と管理用を別プロセス・別Unixユーザー**で起動できるようにする。
  - 分け方（案。9.7）：3つのプロセスと、3つのUnixユーザーに分ける。(1) DBのデーモン（`whiteroom-db serve`。3.6）：データのディレクトリ（ディレクトリ0700・ファイル0600。3.4）を開く、ただ1つのプロセス。データのディレクトリは、デーモンのユーザーだけのもの。2つのソケット（`writer.sock` と `reader.sock`。ファイルのモード0660）と、書き手のロックのファイル（`writer.lock`。3.6の「接続」）を、ソケットのディレクトリに作る。そのディレクトリは、デーモンのユーザーが、先に作り、持つ（デーモンは作らない。グループは、管理用のユーザーのグループにして、管理用のユーザーが通れるようにする。9.4の「設定の例」）。(2) 管理用のサーバー（`whiteroom serve admin`。書くプロセス。ビルドもその中で動く）：デーモンのソケットで繋ぎ（書き手の接続は1つ、読み手の接続は要るだけ）、出力先（ディレクトリ0755・ファイル0644。4.3）と、画像のキャッシュと、管理のAPIの一時のファイルのディレクトリ（`tmp`。9.4）を書く。書き手として繋げるのは、デーモンの `writerUser` のユーザーだけなので、管理用のユーザーを `writerUser` にする（9.4）。(3) 公開用のサーバー（`whiteroom serve public`。9.5）：出力先だけを読み、保存先にも、デーモンにも繋がない。そのため、データのディレクトリは、デーモンのユーザーだけのもの、出力先は、管理用のユーザーが書き、公開用のユーザーが読めるものにする。管理用のユーザーの umask が 022 より厳しいと、出力先のモードが 0644・0755 より狭くなり（umaskの許す範囲で作る。4.3）、公開用のユーザーが読めない。管理用のサーバーが破られても、データのディレクトリと、過去の記録には、手が届かない（3.6の「デーモンが守る規則」。その限界も同じ所）。起動は、デーモンが先で、管理用のサーバーが後。止めるのは、逆の順（systemdのユニットは、まだ無い。6.12の262・269）。設定の例は9.4の「設定の例」。
- `whiteroom init`、`whiteroom build`、`whiteroom rollback`、`whiteroom serve public`、`whiteroom serve admin`、`whiteroom backup`、`whiteroom doctor`、`whiteroom account` などのコマンドと、デーモンの実行ファイル `whiteroom-db` を設ける（コマンドを `whiteroom` のサブコマンドにすることと、デーモンを別の実行ファイルにすることは決定済み。9.1。名前と、作ったものは9.4）。`init` は、最初の管理者のアカウントも作る（案。7.3。作った。9.4）。
- Ubuntu、Debian、Fedora、NixOSなどへの配布形式を検討する。
- 本番運用時にPHP、Node.js、外部DBサーバーなどを必須としない構成を目指す。

### 9.3 TLS・証明書（提案段階）

- 公開サイト向けにACMEによる証明書取得・自動更新機能を内蔵する。
- TLS暗号プロトコルや暗号アルゴリズムは、実績のある標準実装を採用する。
- 自前CAは公開Webサイトの標準方式にはしない。内部管理通信等の用途なら別途検討する。

独自の証明書**管理機構**は構想に残すが、独自のTLS**暗号実装**を行うことは決定していない。

### 9.4 コマンド（案）

コマンドは、2つの実行ファイルに分ける。`whiteroom`（`cmd/whiteroom`。cgoなしでビルドできる）は、サイトをビルドして公開する `whiteroom build`（4.3）と、公開中のリリースを配信する `whiteroom serve public`（内蔵サーバー。9.5）に、保存先（3.4）とアカウント（7.3）を扱うコマンド（`init`・`serve admin`・保存先からの `build`・`backup`・`doctor`・`account`）と、リリースを戻す `rollback`（4.4）を足したもの。管理用のサーバー（9.6）と管理のAPI（9.7）は、`whiteroom serve admin` が動かす。`whiteroom-db`（`cmd/whiteroom-db`）は、保存の層のデーモン（3.6）で、デーモンを動かす `serve`、データのディレクトリを作る `init`、圧縮と回収の `compact`・`gc`（3.6の「保守の口」）を持つ。データのディレクトリを開くのは、`whiteroom-db` だけで、`whiteroom` のコマンドは、データのディレクトリを知らず、デーモンのソケットのディレクトリ（`-db DIR`。設定の `db`）に、ソケットで繋ぐ（3.6の「接続」）。この節の規則は、「決定済み」と書いたもの（9.1）のほかは、どれも仮に決めたもの（案）。issue #109 の計画への答えで決めたこと（コマンドがデーモンを指す形・データのディレクトリを作る人・1つの設定ファイルを2つの実行ファイルで使うこと・一時のファイルの置き場所・起動の順）も、案（3.6）。

**名前とサブコマンド**

- 名前は `whiteroom` にする（決定済み。9.1）。リポジトリとGoのモジュール（`github.com/WhiteRoomLab/whiteroom`）と同じ名前で、コードを `cmd/whiteroom` に置くので、`go build`・`go install` で作る実行ファイルも、この名前になる。
- 9.2のコマンドは、`whiteroom` のサブコマンドにする（決定済み。9.1）。
- 保存の層のデーモンは、`whiteroom` のサブコマンドにせず、別の実行ファイルにする（決定済み。9.1）。名前は `whiteroom-db`（案。9.1は、仮の名前としている）で、コードは `cmd/whiteroom-db`。`go list -deps ./cmd/whiteroom` に、エンジン（`internal/logdb`。その下の `internal/logdb/vfs` は出てよい）も、デーモンのサーバー（`internal/dbserver`）も出ない（`AGENTS.md`）ので、`whiteroom` は、エンジンなしの、cgoなしのビルドのまま。`whiteroom-db serve` は、Linuxだけで動く（6.12の283）。

`whiteroom` のコマンド：

| コマンド | 内容 |
| --- | --- |
| `whiteroom init [フラグ] [SITE]` | 動いているデーモンの、できたての保存先に、最初の管理者を作り、`SITE` があれば、そのサイトを取り込む（下記） |
| `whiteroom build [フラグ] [SITE]` | ディレクトリ `SITE` のサイトをビルドして、公開する（下記） |
| `whiteroom build -db DIR [フラグ] [SITE]` | デーモン（ソケットのディレクトリ `DIR`）の保存先の文書と原本から、ビルドして、公開する（下記の「保存先からの `whiteroom build`」） |
| `whiteroom rollback OUT [RELEASE]` | 出力先 `OUT` の公開中のリリースを、残っている別のリリースに切り替える（下記） |
| `whiteroom serve public [フラグ] [OUT]` | 出力先 `OUT` の公開中のリリースを、内蔵サーバー（9.5）で配信する（下記） |
| `whiteroom serve admin [フラグ] [SITE]` | 管理用のサーバー（9.6）と管理のAPI（9.7）を、デーモンの保存先から動かし、保存先からビルドして公開する（下記） |
| `whiteroom backup [フラグ] DEST` | デーモンの保存先の写しを `DEST` に取る（下記） |
| `whiteroom doctor [フラグ] [SITE]` | デーモンの保存先の問題を調べて示す。何も直さない（下記） |
| `whiteroom account reset [フラグ] LOGIN`・`whiteroom account delete [フラグ] ID` | サーバーを止めて動かす、アカウントの復旧（下記） |
| `whiteroom help` | 使い方を示す（`whiteroom -h`・`-help`・`--help` も同じ） |

`whiteroom-db` のコマンド（下記の「`whiteroom-db`」）：

| コマンド | 内容 |
| --- | --- |
| `whiteroom-db serve [フラグ]` | データのディレクトリを開き、書き手用と読み手用の2つのソケットで、止めるまで配信する。Linuxだけ |
| `whiteroom-db init [フラグ]` | データのディレクトリを作る（無いか、空のディレクトリ） |
| `whiteroom-db compact [フラグ]` | ログを新しい世代に書き直し、古い世代を消す。デーモンを止めて動かす |
| `whiteroom-db gc [フラグ]` | 状態が要らないファイルを消す。デーモンを止めて動かす |
| `whiteroom-db help` | 使い方を示す（`whiteroom-db -h`・`-help`・`--help` も同じ） |

ビルドを止めて再開するコマンド（4.4の「戻す操作」。管理のAPIにはある。9.7）は、まだ無い（6.12の108・240）。`whiteroom rollback` は、出力先の `current` を切り替えるだけで、ビルドを止めない（下記）。ビルドの後にNginxに設定を読み込み直させるコマンド（6.12の113）と、写しから戻すコマンド、保存先を直すコマンド（`backup` の「戻す手順」と6.12の131）も、まだ無い。デーモンを動かしたまま圧縮・回収するコマンドも無い（3.6の「保守の口」。6.12の133）。

規則：

- フラグ：Goの `flag` パッケージの書き方（`-out DIR`・`-out=DIR`。`--out` も同じ）で、引数（`SITE`・`OUT`）より前に書く。引数より後ろのものは、引数とみなす（引数が多すぎる誤り（`whiteroom build: too many arguments: give at most one SITE, after the flags`）になる）。`whiteroom-db` のサブコマンドは、引数を取らない（フラグだけ。書くと、`whiteroom-db init: unexpected argument "extra": the command takes flags only` と使い方を示して、2で終わる）。
- 使い方：引数の無い `whiteroom` は、使い方を標準エラーに、`whiteroom help` は標準出力に示す。サブコマンドの `-h`（`whiteroom build -h`・`whiteroom serve admin -h`・`whiteroom account reset -h` など）は、そのサブコマンドの使い方とフラグを、標準エラーに示して、0で終わる。`whiteroom-db` も同じ（引数の無い `whiteroom-db` は、使い方を標準エラーに示して、2で終わる。`whiteroom-db help`・`-h`・`-help`・`--help` は標準出力に、`whiteroom-db serve -h` などは標準エラーに示して、0で終わる）。使い方の文は、各コマンドの `-h` が正で、この節は、その要点と、理由を書く。
- 知らないもの：知らないコマンドには、`whiteroom: unknown command "x"` と `Run "whiteroom help" for usage.` を示す（`whiteroom-db` は、`whiteroom-db: unknown command "x"` と `Run "whiteroom-db help" for usage.`）。`whiteroom serve` の後ろには、`public` か `admin` を書く（`help`・`-h`・`-help`・`--help` には `whiteroom help` と同じ使い方を、ほかには `whiteroom serve: unknown server "x"` と `Run "whiteroom help" for usage.` を示す。`whiteroom serve` だけなら、使い方を標準エラーに示して、2で終わる）。`whiteroom account` の後ろには、`reset` か `delete` を書く（`help`・`-h`・`-help`・`--help` には、`account` の使い方を標準出力に示して、0で終わる。ほかには `whiteroom account: unknown subcommand "x"` と `Run "whiteroom help" for usage.` を示す。`whiteroom account` だけなら、使い方を標準エラーに示して、2で終わる）。
- 設定ファイル：`-config FILE` を書いたコマンドだけが読む（下記の「設定ファイル」）。`whiteroom` で `-config` を取るのは、`init`・`build`・`serve admin`・`backup`・`doctor`・`account reset`・`account delete`。`rollback` と `serve public` は、設定ファイルを読まない。`whiteroom-db` は、4つのサブコマンドとも取る。
- パスの `..`：引数・フラグ・設定ファイルのパスの `..` は、OSと同じ順に解く（案）。`link` がシンボリックリンクなら、`link/..` は、`link` の指す先の親で、`link` のあるディレクトリではない。コマンドがパスを繋ぐとき（`SITE` の下の既定の出力先とキャッシュと一時のファイルのディレクトリ、設定ファイルのディレクトリからの相対パス、警告とエラーに示すパス）は、ディレクトリでないもの（シンボリックリンクなど）か、見られないものの後の `..` を、その前の要素と一緒に残し、ほかは文字の上で整える（`SITE` が `link/..` なら、出力先は `link/../out` で、結果の行もそう示す。`conf` がディレクトリなら、`conf/whiteroom.yaml` の `data: ../data` は `data`）。絶対パスにするとき（キャッシュのディレクトリ、`serve public` の出力先、出力先とキャッシュの比べ方（4.3））は、`..` があれば、シンボリックリンクを解いた絶対パスにする。デーモンが開くデータのディレクトリのパスの `..` がシンボリックリンクの後にあれば、それを解いた絶対パスで開く（エンジンは、ファイルの名前を文字の上で繋ぐため。エンジン（3.5）のエラーに示すパスも、その絶対パスから示す）。

**終了コード**

| コード | 意味 |
| --- | --- |
| `0` | 成功（使い方を求めた `whiteroom help`・`-h` も。問題を見つけなかった `whiteroom doctor`。止める合図（Ctrl-C・SIGTERM）で止めた `whiteroom serve public`・`whiteroom serve admin`・`whiteroom-db serve`） |
| `1` | 失敗（サイトの問題、ビルドや配信のエラー、取り消したビルド、デーモンに届かない（2秒待つ）・デーモンが、短い要求に、30秒、答えない・書き手の接続が別のプロセスのもの・書き手として繋げないユーザー、`whiteroom-db` がデータのディレクトリを開けない・作れない・別のプロセスが開いている、待ち受けられない、`whiteroom doctor` が問題を見つけたとき、と、確かめを動かせなかったとき） |
| `2` | 使い方の誤り（引数の無い `whiteroom`・`whiteroom serve`・`whiteroom account`・`whiteroom-db`、知らないコマンド・サブコマンド・フラグ、多すぎる引数・足りない引数、範囲の外の値、データのディレクトリ（`whiteroom-db`）とソケットのディレクトリ（`whiteroom`・`whiteroom-db serve`）を決めていないこと、設定ファイルの問題と、設定ファイルを読めないこと、名前の見つからないユーザーとグループ、通らない `-url`、`-addr` と `-socket` の両方、loopback でない `-addr`、オリジンの無いソケット） |

- 使い方の誤りを2に、`-h` を0にするのは、Goの `flag` パッケージと同じ。
- 標準出力と標準エラーの読む側が終わったとき（パイプの先のプロセスが終わったときなど）：止めるまで動く3つ（`whiteroom-db serve`・`whiteroom serve admin`・`whiteroom serve public`）は、書けなくても、動き続け（書けなかった頭は、次の合図に、また試す）、止める合図で、0で終わる（3.6の「頭の書き出し」。#130）。ほかのコマンドは、Goの既定のまま、ふつうの Unix のコマンドと同じに、SIGPIPE で終わる（`whiteroom doctor | head -n 1` など）。
- デーモンが、接続を受けて、答えないとき（止められたプロセス・返らないディスクの読みなど）：短い要求（`Hello`・`Snapshot`・読み・原本を開くこと・書き込みなど。3.6の「接続」の「答えを待つ時間」。#127）に、30秒、何も送らない（要求を書く間なら、何も読まない）と、クライアントが接続を閉じ、「届かない」と同じに、`whiteroom <コマンド>: <DIR>: cannot reach the daemon: the daemon has sent nothing for 30s`（書く間なら `the daemon has read nothing for 30s`）を示して、1で終わる（30〜31秒の後。`doctor` は、開くときなら、`open: <DIR>: cannot reach the daemon: the daemon has sent nothing for 30s` の問題と `1 problem found in <DIR>`。下記の例。2026-11-18に、答えない偽のデーモンに、`init`・`account reset`・`serve admin` の起動・`doctor`・`build -db`・`backup` を動かして、確かめた）。長い要求の途中で止まったデーモン（`doctor` の `Verify` と、監査の確かめ（`Events`）、`backup` の位置が決まるまで、保存の `Commit` の答え）は、見分けられないので、これまでと同じく、待ち続け、止める合図（Ctrl-C・SIGTERM）で、`whiteroom <コマンド>: interrupted` を示して、1で終わる（6.12の267。#136）。止める合図は、30秒の上限より、いつも先に効く。
- `whiteroom doctor` は、問題があるときも、確かめを動かせなかったときも1にする。どちらかは、標準出力の最後の行と標準エラーで分かる（下記の「`whiteroom doctor`」。6.12の260）。

**設定ファイル**

環境ごとに変える値（パス・待ち受け・サイトのURL・デーモンの上限など）を、1つのYAMLのファイルに集められる（案。環境ごとの値は6.12の68）。同じファイルを、`whiteroom` にも、`whiteroom-db` にも渡せる（下記）。

- 読むのは、`-config FILE` を書いたコマンドだけ（`whiteroom` の `init`・`build`・`serve admin`・`backup`・`doctor`・`account reset`・`account delete` と、`whiteroom-db` の `serve`・`init`・`compact`・`gc`）。既定の場所（`/etc` やホームのディレクトリなど）は無く、環境変数も読まない。`whiteroom serve public` と `whiteroom rollback` は、読まない（フラグと引数だけで足りる）。
- 形：最上位は項目のマッピング。YAMLは、スキーマのファイル（6.6.8）と同じ読み手と範囲で読む（6.6.8の「YAMLの範囲（案）」。`null`・アンカー・複数行の値などは不可）。1MiBまで。`addr: 127.0.0.1:8081` は引用符なしで書ける。`[` で始まる値（`addr: "[::1]:8081"`）は、引用符で囲む。
- 項目（どれも省ける。同じ意味のフラグ・引数があり、それが勝つ）：

| 項目 | 値 | 同じものを決めるフラグ・引数 |
| --- | --- | --- |
| `data` | パス（デーモンのデータのディレクトリ） | `-data`（`whiteroom-db`） |
| `db` | パス（デーモンのソケットのディレクトリ） | `-db` |
| `site` | パス | 引数 `SITE` |
| `theme` | パス | `-theme` |
| `out` | パス | `-out` |
| `cache` | パス | `-cache` |
| `tmp` | パス（管理のAPIの一時のファイルのディレクトリ） | `-tmp`（`whiteroom serve admin`） |
| `workers` | 0以上の整数 | `-workers` |
| `memory` | 0から8796093022207の整数（MiB） | `-memory` |
| `url` | 文字列（サイトのURL。下記の「環境ごとの `url`」） | `-url` |
| `addr` | 文字列（`IPアドレス:ポート`） | `-addr` |
| `socket` | パス | `-socket` |
| `origin` | 文字列（URL） | `-origin` |
| `trustedProxies` | 文字列の並び（IPアドレスか、CIDRの範囲） | `-trusted-proxy`（繰り返す） |
| `writerUser` | ユーザーの名前か、0以上の整数 | `-writer-user`（`whiteroom-db serve`） |
| `writerGroup` | グループの名前か、0以上の整数 | `-writer-group` |
| `readerGroup` | グループの名前か、0以上の整数 | `-reader-group` |
| `maxReaders` | 1以上の整数 | `-max-readers` |
| `commitRate` | 0以上の整数（1秒あたりの回数） | `-commit-rate` |
| `minFree` | 0以上の整数（バイト） | `-min-free` |
| `minFreeInodes` | 0以上の整数（inode の数） | `-min-free-inodes` |
| `snapshotIdle` | 期間の文字列（`5m`・`0s`。0以上） | `-snapshot-idle` |
| `stall` | 期間の文字列（`30s`・`0s`。0以上） | `-stall` |
| `maxStaged` | 1以上の整数（バイト） | `-max-staged` |

- デーモンの項目（`writerUser` から `maxStaged` まで。意味は3.6の「接続」と「上限」）の、範囲と、省いたときの値（下記の「`whiteroom-db`」のフラグの既定と同じ。上限の値は、仮置き。3.6の「上限」）：

| 項目 | 範囲 | 省いたとき |
| --- | --- | --- |
| `writerUser` | ユーザーの名前か、0以上の整数 | `whiteroom-db serve` を動かすユーザー |
| `writerGroup`・`readerGroup` | グループの名前か、0以上の整数 | システムが付けるグループ |
| `maxReaders` | 1以上の整数 | 64 |
| `commitRate` | 0以上の整数。0は、制限なし | 50 |
| `minFree` | 0以上の整数。0は、空きを確かめない | 1073741824（1GiB） |
| `minFreeInodes` | 0以上の整数。0は、inode の空きを確かめない | 65536 |
| `snapshotIdle` | 期間（`5m`・`0s`）。0は、どちらもしない（スナップショットを放さず、読み手の接続も閉じない） | `5m` |
| `stall` | 期間（`30s`・`0s`）。0は、切らない | `30s` |
| `maxStaged` | 1以上の整数 | 268435446 |

- 値の決め方：フラグか引数を書けば、それ。なければ、設定ファイルの項目。なければ、本体の既定。数と期間は、フラグを書いたかどうかで決める（`-workers 0` も、設定ファイルの `workers` に勝ち、`-commit-rate 0` も、`commitRate` に勝つ）。`-trusted-proxy` を1つでも書くと、`trustedProxies` は使わない。`-addr` と `-socket` は、書いたほうだけで決め、設定ファイルの `addr` と `socket` は見ない（下記の「`whiteroom serve admin`」）。
- 相対パス：設定ファイルの中のパス（`data`・`db`・`site`・`theme`・`out`・`cache`・`tmp`・`socket`）は、そのファイルのディレクトリからのもの（`conf/whiteroom.yaml` の `data: data` は、`conf/data`。`..` は、上記の「パスの `..`」）。絶対パスは、そのまま。`socket` の、`@` で始まる値も、そのまま（`serve admin` が断る。下記）。フラグと引数の相対パスは、作業ディレクトリからのもの。ソケットのパスは、Linuxで107バイトまでなので、`db` と `socket` は、短い絶対パスにする（3.6の「接続」）。
- どのコマンドが、どの項目を使うか（`○` が使う）：

| 項目 | `whiteroom init` | `build` | `serve admin` | `doctor` | `backup` | `account` | `whiteroom-db serve` | `whiteroom-db init`・`compact`・`gc` |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `data` | | | | | | | ○（必須） | ○（必須） |
| `db` | ○（必須） | ○（あれば、保存先からビルドする） | ○（必須） | ○（必須） | ○（必須） | ○（必須） | ○（必須） | |
| `site` | | ○ | ○ | ○（決まらなければ、サイトを確かめない） | | | | |
| `theme` | ○（`SITE` があるとき） | ○ | ○ | ○（`SITE` があるとき） | | | | |
| `out`・`cache`・`workers`・`memory` | | ○ | ○ | | | | | |
| `tmp` | | | ○ | | | | | |
| `url` | ○（`SITE` があるとき） | ○ | ○ | ○（`SITE` があるとき） | | | | |
| `addr`・`socket`・`origin`・`trustedProxies` | | | ○ | | | | | |
| `writerUser`・`writerGroup`・`readerGroup`・`maxReaders`・`commitRate`・`minFree`・`minFreeInodes`・`snapshotIdle`・`stall`・`maxStaged` | | | | | | | ○ | |

- コマンドが使わない項目は、型と、値の範囲（文字列が空でないこと、`workers`・`memory` と、デーモンの項目の範囲）だけを確かめる。そのため、1つの設定ファイルを、どのコマンドにも、どちらの実行ファイルにも渡せる：`data` とデーモンの項目は `whiteroom-db` のもの、`db` は、どちらも読む（デーモンは、ソケットを作る所として、`whiteroom` は、繋ぐ所として）ので、デーモンと、それに繋ぐコマンドが、同じ `db` を指す。ユーザーとグループが、システムにあるかは、`whiteroom-db serve` が使うときだけ確かめる。デーモンを複数動かすとき（本番とステージング）は、`data` と `db` が違うので、デーモンごとにファイルを分ける（下記の「設定の例」）。
- 誤り：知らない項目、型の誤り（`workers: many`・文字列でない `addr` など）、範囲の外の値、空の文字列、空のファイル、YAMLの誤り、1MiBを超えるファイル、読めないファイルは、問題を標準エラーに示して、2で終わる（使い方の文は示さず、何も作らない）。問題の行は `FILE:行:列: /項目: 理由` で、値は理由に入れない（`bad.yaml:2:1: /workers: must be an integer of 0 or more`・`bad.yaml:3:1: /colour: unknown item`・`bad.yaml:3:1: /maxReaders: must be an integer of 1 or more`）。期間の項目は、文字列で、`0s` と書く（数だけの `0` や `30` は断る。`bad.yaml:4:1: /stall: must be a duration of 0 or more, such as 30s or 5m`）。YAMLの誤りは `FILE:行:列: 理由`、読めないファイルは `whiteroom build: open nope.yaml: no such file or directory` の形。`addr` と `trustedProxies` の値の誤りと、`addr` と `socket` の両方は、`serve admin` が使うときに確かめるので、行と列の無い形で、`addr` と `trustedProxies` の誤りの行には、その値が入る（待ち受けのアドレスで、秘密ではない。`badaddr.yaml: /addr: "0.0.0.0:8081" is not a loopback IP address and a port, such as 127.0.0.1:8081 or [::1]:8081`・`both.yaml: addr and socket are both given: give one of them, as the server listens on one`）。名前の見つからないユーザーとグループも、`whiteroom-db serve` が使うときに確かめるので、行と列の無い形（`usr.yaml: /writerUser: unknown user "name"`。フラグなら `whiteroom-db serve: -writer-user: unknown user "nosuchuser"` と使い方）。
- 書けないもの：実行のたびに変わる値（`-login`・`-name`・`-email`・`DEST`・`RELEASE`・`ID`・`LOGIN`・`-heads`）と、秘密（パスワードとトークン。下記の「秘密」）。秘密を書く項目は無いので、ファイルのモードと持ち主は、確かめない。
- 設定ファイルに `db` があると、`whiteroom build -config FILE` は、デーモンの保存先からビルドする（下記の「保存先からの `whiteroom build`」）。サイトのディレクトリからビルドするときは、`-config` を渡さない（項目を消すフラグは無い）。残りの論点（既定の場所・環境変数・版の項目・ファイルのモードなど）は6.12の255。

**データのディレクトリと、ソケットのディレクトリと、開発者のファイル**

- データのディレクトリ（保存先。3.4）は、デーモン `whiteroom-db` だけが開く。`-data DIR` か、設定ファイルの `data` で、`whiteroom-db` の4つのサブコマンドに、必ず決める（既定の場所は無い）。決めないと、使い方の誤り（2）になる（`whiteroom-db serve: no data directory: give -data DIR, or data in the config file`）。`whiteroom` のコマンドは、データのディレクトリを知らず、`-data` も `data` も、使わない。
- ソケットのディレクトリ（デーモンが `writer.sock` と `reader.sock` と `writer.lock` を作る所。3.6の「接続」）は、`-db DIR` か、設定ファイルの `db` で、デーモン（作る所）と、デーモンに繋ぐ `whiteroom` のコマンド（繋ぐ所）に、必ず決める（既定の場所は無い）。決めないと、使い方の誤り（2）になる（`whiteroom serve admin: no database: give -db DIR, the directory of the sockets of whiteroom-db, or db in the config file`・`whiteroom-db serve: no socket directory: give -db DIR, or db in the config file`）。`init`・`serve admin`・`backup`・`doctor`・`account` は、決めた所に繋ぎ、`build` だけは、決めなければ、サイトのディレクトリからビルドする。デーモンは、このディレクトリを作らない（`whiteroom-db serve` の前に、作っておく。デーモンのユーザーが作り、繋ぐユーザーが、そこを通り、ソケットのファイルに書けるようにする。下記の「設定の例」）。デーモンを動かし直しても、このディレクトリと `writer.lock` は残す（作り直すと、書き手のロックが効かない。3.6の「接続」の「書き手のロック」）。
- 保存先を作るのは、`whiteroom-db init`（データのディレクトリを作る）と、`whiteroom init`（動いているデーモンの保存先に、最初のコミットと、最初の管理者を入れる）の2つ。`whiteroom init` は、デーモンが先に動いていることが要る（下記の「`whiteroom init`」と「設定の例」）。
- 開発者のファイル（スキーマのファイル `site/schema.yaml` とテーマ。6.6.8）は、保存先の外の、サイトのディレクトリ `SITE` と、`-theme`（か、設定ファイルの `theme`）から読む。`SITE` は、引数か、設定ファイルの `site`。テーマは、`-theme`、設定ファイルの `theme`、`SITE/theme` の順で決める。`serve admin` と `build` は、`SITE` の既定が `.`。`doctor` は、既定が無く、`SITE` が決まらないと、サイトを確かめない（問題にはせず、標準エラーに1行示す）。
- 保存先を使うコマンド（`serve admin`・`build -db`・`doctor`）は、`SITE` から、スキーマのファイルとテーマだけを読む。`SITE` の文書（`entries/` など）は、読まない。読むのは、`init` が `SITE` を取り込むときだけで、取り込むのは、引数の `SITE` だけ（設定ファイルの `site` は取り込まない。取り込みは、設定でなく、そのときに頼むものなので）。

**環境ごとの `url`**

- `-url URL` か、設定ファイルの `url` は、サイトのURL（サイト設定の `url`。6.6.8）を、そのプロセスの中だけで置き換える（案）。決め方は、`-url`（空の文字列は、書かないのと同じ）、設定ファイルの `url`、上書きなし、の順。取るのは、`init`・`build`（`-db` のときも）・`serve admin`・`doctor`。`rollback`・`backup`・`account`・`serve public` は取らない（`-url` は知らないフラグ）。
- 確かめ：サイト設定の `url` と同じ規則（6.6.8）で確かめ、正規化はしない。通らなければ、2で終わり、標準エラーに1行だけ示す（使い方の文は示さず、値も入れない）：`whiteroom build: -url is not a valid site URL: must not end with "/"`・`whiteroom <コマンド>: url of the config file is not a valid site URL: <理由>`。ビルドは、出力先とキャッシュを作る前に、`serve admin` は、待ち受ける前に、確かめる。
- 変わるもの：ビルドが書く絶対URL（`<head>` の正規URL・言語の対応・フィード・サイトマップ・OGP。6.8）、公開するパスの基点（`https://example.com/blog` のサイトなら `/blog`。6.6.8）、リダイレクトの行き先がサイトの中かどうかの判断（自分に戻るもの・ループ。6.6.6・6.6.8）、リリースの名前（4.3）。保存の操作（`serve admin` の API が保存する文書）の検証と、`doctor`・`init` の検証も、上書きしたURLで動く。
- 変わらないもの：保存してある設定の文書。管理のAPIの `GET /api/settings` は、保存してある `url` を返し、`PUT /api/settings` は、書かれた `url` を保存する。サーバーは、上書きしたURLで、ビルドし続ける。文書の中に書いた絶対URL（リダイレクトの `to`、著者のリンクなど）も変わらない：本番のURLで書いたリダイレクトは、上書きの下では、サイトの外へのリダイレクトになる（ステージングのサイトから、本番へ転送する）。サイトのURLのパスについての警告（NginxとApacheの設定に書けないパス。6.6.6）の場所は、上書きしても、設定の文書の `/url`。
- `doctor` と `init` は、`SITE` があるときだけ使う（`SITE` なしの `-url` は、使い方の誤り。設定ファイルの `url` は、`SITE` が決まらなければ見ない）。`backup`・`account` は、設定ファイルの `url` を、型と、空でないことだけ確かめる。
- 用途：同じ保存先（か、その写し）から、本番とステージングのサイトを作る（下記の「設定の例」）。同じ文書と同じ上書きなら、同じ名前のリリースができる（写しから戻した保存先でも）。上書きしたURLを、管理のAPIと管理画面で示すことは、まだ無い（6.12の256）。

**パスワードの入力**

- パスワードは、標準入力だけから読む（`init`・`account reset`）。引数・フラグ・環境変数・設定ファイルでは受け取らない（シェルの履歴と `ps` に残るため）。読んだパスワードは、標準出力・標準エラー・ログ・エラーの文に出さない（下記の「秘密」）。パスワードの規則（長さなど）は7.3。
- 端末（Linux）：促しを標準エラーに書き（`init` は `Password: ` と `Password (again): `。`account reset` は `New password for LOGIN: ` と `New password for LOGIN (again): `）、エコーを止めて読む。2回が合わなければ、聞き直す。パスワードの問題（短すぎるなど）も、聞き直す。聞くのは、合わせて3回まで（`no password was accepted in 3 tries`）。止める合図（Ctrl-C）で、端末の設定を戻して、終わる（Ctrl-\・Ctrl-Z では、戻さない。6.12の265）。端末の設定は、標準ライブラリの `syscall` の `ioctl` で変える（外部のモジュールは使わない。1.2）。
- パイプとファイル（端末でない標準入力）：最初の1行（行末の `\n` か `\r\n` は除く。8192バイトまで）。空の行は `no password was read from the standard input`、長すぎる行は `the password is longer than 8192 bytes` で、1で終わる。聞き直さず、パスワードの問題は、1行で示して、1で終わる（`whiteroom init: the password: too short: 6 characters (min 15)`）。行が来る前に、止める合図（SIGINT・SIGTERM）が来たら、待たずに、`interrupted` を示して、1で終わる。パスワードは、端末で打つか、権限0600のファイルを標準入力に渡す（`< FILE`）。
- Linuxのほかの OS の端末からは、読まずに断る（`cannot read a password without echo from a terminal on this OS: give it on the standard input from a pipe or a file`）。パイプとファイルは使える（6.12の253）。

**`whiteroom build`**

```text
whiteroom build [-config FILE] [-db DIR] [-out DIR] [-cache DIR] [-theme DIR] [-workers N] [-memory MiB] [-url URL] [SITE]
```

サイトのディレクトリ `SITE` のサイトを、このプロセスの中でビルドして（4.3）、出力先に公開する。デーモンのソケットのディレクトリを決めたとき（`-db DIR` か、設定ファイルの `db`）は、デーモンの保存先からビルドする（下記の「保存先からの `whiteroom build`」）。

| 引数・フラグ | 内容 | 省略したとき |
| --- | --- | --- |
| `SITE` | サイトのディレクトリ（4.3の「サイトのディレクトリ」） | 設定ファイルの `site`、無ければ `.`（作業ディレクトリ） |
| `-config FILE` | 設定ファイル（上記） | 読まない |
| `-db DIR` | デーモンのソケットのディレクトリ。決めると、そのデーモンの保存先からビルドする（下記の「保存先からの `whiteroom build`」） | 設定ファイルの `db`、無ければ、サイトのディレクトリからビルドする |
| `-out DIR` | 出力先のディレクトリ（4.3の「出力先」） | 設定ファイルの `out`、無ければ `SITE/out` |
| `-cache DIR` | 画像のキャッシュのディレクトリ（4.3の「画像のキャッシュ」） | 設定ファイルの `cache`、無ければ `SITE/cache` |
| `-theme DIR` | `SITE/theme` の代わりに使うテーマのディレクトリ（5.4.1） | 設定ファイルの `theme`、無ければ `SITE/theme` |
| `-workers N` | ページの描画などを、同時に行う数。0以上 | 設定ファイルの `workers`、無ければ `0`（Goの `GOMAXPROCS` の値） |
| `-memory MiB` | 画像のファイルを作るときの、メモリの見積もりの上限（6.6.2）を、MiBで。0から8796093022207まで（バイトにして、64ビットの整数に入る数） | 設定ファイルの `memory`、無ければ `0`（1024MiB） |
| `-url URL` | サイト設定の `url` の代わりに使うURL（上記の「環境ごとの `url`」） | 設定ファイルの `url`、無ければ上書きしない |

規則：

- 既定の置き場所：出力先とキャッシュは、既定では、サイトのディレクトリの中に並べる（キャッシュは、出力先の中には置けない。4.3の「画像のキャッシュ」）。サイトのディレクトリは、4.3の表に無いもの（`out/`・`cache/` も）を無視するので、サイトのディレクトリで `whiteroom build` とするだけで、ビルドし直せる。キャッシュのディレクトリの扱い（プレビューと本番のビルドで分けるかなど）は6.12の104。
- 相対パス：`-out`・`-cache`・`-theme` の相対パスは、作業ディレクトリからのもの（`SITE` からではない）。
- `-theme`：見本のサイト（6.11。`internal/build/testdata/site/`）は `theme/` を持たず、見本のテーマ（`internal/theme/testdata/example/`。5.4.9）でビルドするため。テーマを作るときに、サイトにコピーせずに試すのにも使える。`-theme` のときは、`SITE` の `theme/` を読まない（無くてもよい）。
- 範囲：負の `-workers` と、範囲の外の `-memory` は、使い方の誤り（2）にする（`whiteroom build: -workers -1 is out of range (0 or more)`、`whiteroom build: -memory -1 is out of range (0 to 8796093022207)`）。
- 同じプロセス：テーマを適用するビルドを、別のプロセスで動かして、時間とメモリに上限をかけること（5.3）は、まだしない（いまのテーマは、開発者が書くもの）。止めるのは、取り消し（下記の「ビルドの取り消し」）だけ。
- サイトのURL：ビルドは、サイト設定の `url`（6.6.8）のまま書く。`-url` か、設定ファイルの `url` を書いたときだけ、その値で書く（上記の「環境ごとの `url`」。環境ごとの値は6.12の68）。ページのリンクと、CSS・画像のURLは、パスで書く（6.5.2・5.4.4）ので、手元の `whiteroom serve public`（下記）でも、ページを開いて、たどれる。本体が `<head>` に書くURL（正規URL・言語の対応・フィード・アイコン・OGP・構造化データ。6.8）と、フィードとサイトマップの中のURL（6.8）は、サイトのURL（上書きしなければ、`https://example.com/` など）のまま。

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

- 起動：出力先が無いとき（`whiteroom serve public: open /srv/site: no such file or directory`）、公開中のリリースが無いとき（`whiteroom serve public: no release is published in /srv/site`）、そのリリースが正しくないとき（`current` がリリースを指さない、`server.json` を読めないか正しくないなど。9.5）、待ち受けられないとき（`whiteroom serve public: listen tcp 127.0.0.1:8080: bind: address already in use`）は、配信を始めずに、1で終わる。出力先のエラーでは、出力先を絶対パスで示す（`..` があれば、シンボリックリンクを解いたもの。上記の「パスの `..`」）。
- URL：待ち受けを始めたら、標準出力に、開くURLを1行で示す（`serving out at http://127.0.0.1:8080/`）。ホストとポートは、待ち受けたもの（ポートの `0` は、決まった番号にする。`0.0.0.0`・`::` は、`localhost` にする）。パスは、公開中のリリースのサイトのURLのパス（`server.json` の `base`。6.6.6）で、`/blog` のサイトでは `http://127.0.0.1:8080/blog/` になる（ASCII以外の文字・空白・制御文字のバイトは、16進の大文字でエンコードする）。示すのは起動のときだけで、後のビルドでサイトのURLのパスが変わっても、示し直さない。
- 作り直さない：変更を見て、ビルドし直すことはしない。`whiteroom build` で公開し直すと、起動し直さなくても、次のリクエストから、新しいリリースを配信する（9.5）。手元での作業のために、変更を見て作り直すかは6.12の123。
- 止める：Ctrl-C（SIGINT）か SIGTERM で、待ち受けをやめ、処理中のリクエスト（ダウンロードの途中のものも）が終わるのを10秒まで待って（終わらないものは、接続を閉じる）、何も示さずに、0で終わる。2回目のシグナルでは、すぐに終わる。
- 標準エラーには、リクエストのエラーを記録する（9.5の「エラーの記録」）。
- 設定ファイルは読まない（`-addr` だけ）。待ち受けは、既定で `127.0.0.1:8080`（管理用のサーバーの既定は `127.0.0.1:8081`。下記）。

**`whiteroom-db`**

```text
whiteroom-db serve [-config FILE] [-data DIR] [-db DIR] [-writer-user USER] [-writer-group GROUP] [-reader-group GROUP] [-max-readers N] [-commit-rate N] [-min-free BYTES] [-min-free-inodes N] [-snapshot-idle DURATION] [-stall DURATION] [-max-staged BYTES]
whiteroom-db init [-config FILE] [-data DIR]
whiteroom-db compact [-config FILE] [-data DIR]
whiteroom-db gc [-config FILE] [-data DIR]
```

DBのデーモン（3.6）の実行ファイル。データのディレクトリ（3.4）を開く、ただ1つのプロセスで、`whiteroom` の `init`・`serve admin`・`account` は書き手として、`build -db`・`backup`・`doctor` と、管理用のサーバーの読みは、読み手として、ソケットで繋ぐ。`serve` は、Linuxだけで動く（相手のUnixユーザーを、OSから受け取るため。6.12の283）。値の決め方は、`whiteroom` と同じ：フラグ、設定ファイルの項目、既定の順（上記の「設定ファイル」）。

`serve`：

| フラグ | 内容 | 省略したとき |
| --- | --- | --- |
| `-config FILE` | 設定ファイル（上記。`data`・`db` と、デーモンの項目を使う） | 読まない |
| `-data DIR` | 開くデータのディレクトリ（`whiteroom-db init` が作ったもの） | 設定ファイルの `data`（必須） |
| `-db DIR` | `writer.sock` と `reader.sock` を作るディレクトリ。あらかじめ、あること | 設定ファイルの `db`（必須） |
| `-writer-user USER` | 書き手として繋げるUnixユーザー（名前か数） | 設定ファイルの `writerUser`、無ければ、デーモンを動かすユーザー |
| `-writer-group GROUP` | `writer.sock` と `writer.lock` のグループ（名前か数） | 設定ファイルの `writerGroup`、無ければ、システムが付けるグループ |
| `-reader-group GROUP` | `reader.sock` のグループ（名前か数） | 設定ファイルの `readerGroup`、無ければ、システムが付けるグループ |
| `-max-readers N` | 同時に繋げる読み手の数。1以上 | 設定ファイルの `maxReaders`、無ければ `64` |
| `-commit-rate N` | 書き手が、1秒に確定できる回数。0は、制限なし | 設定ファイルの `commitRate`、無ければ `50` |
| `-min-free BYTES` | 書き込みの後に、データのディレクトリのファイルシステムに残る空きの下限。0は、確かめない | 設定ファイルの `minFree`、無ければ `1073741824` |
| `-min-free-inodes N` | ファイルを作りうる書き込みの後に、データのディレクトリのファイルシステムに残る inode の空きの下限。0は、確かめない。inode を数えないファイルシステム（btrfs など）では、確かめない | 設定ファイルの `minFreeInodes`、無ければ `65536` |
| `-snapshot-idle DURATION` | 何も送らない接続の、スナップショットを放すまでの時間と、スナップショットを持たない読み手の接続を、答えずに閉じるまでの時間（書き手の接続は閉じない。3.6の「上限」。#129）。0は、どちらもしない | 設定ファイルの `snapshotIdle`、無ければ `5m` |
| `-stall DURATION` | 要求の途中で進まない接続を、切るまでの時間。0は、切らない | 設定ファイルの `stall`、無ければ `30s` |
| `-max-staged BYTES` | `Commit` の前に、書き手が置けるオブジェクトの合計。1以上 | 設定ファイルの `maxStaged`、無ければ `268435446` |

規則：

- 上限：値は、仮置き（3.6の「上限」。見直しは6.12の267）。枠・`Commit` の大きさ・オブジェクトと原本の大きさは、エンジンと文書の上限に結びついているので、フラグも、設定の項目も無い。既定は、フラグの説明にも書く。デーモンが相手に伝える（`Hello` の答え）上限は、枠・`Commit`・オブジェクトと原本・置いたオブジェクトの合計（`-max-staged`）・キーだけ（3.6）。範囲の外の値は、使い方の誤り（2）：フラグは `whiteroom-db serve: -commit-rate: must be an integer of 0 or more` と使い方、設定ファイルの項目は `FILE:行:列: /項目: 理由`（上記の「設定ファイル」）。
- ユーザーとグループ（案）：`-writer-user` のユーザーだけが、書き手用のソケットに書き手として繋げる。デーモンは、繋いだ相手のユーザーを、OSから受け取って（Linuxの `SO_PEERCRED`）確かめる。別のユーザーが繋ぐと、`the user 1000 is not the writer of this daemon` で断る（`whiteroom init`・`account`・`serve admin` は、すぐにこの文で、1で終わる。読み手用のソケットは、相手のユーザーを見ず、`whiteroom doctor`・`backup` は動く）。ソケットのファイルのモードは0660で、`-writer-group`・`-reader-group` を書けば、そのグループにする（`chown`）。ソケットのディレクトリのモードと持ち主は、変えない。名前のユーザー・グループがシステムに無ければ、`whiteroom-db serve: -writer-user: unknown user "x"`（`unknown group "x"`）で、2で終わる。
- 起動の順（失敗したら、そこで止め、開いたものを閉じて、1で終わる。フラグと設定ファイルの誤りは2）：フラグと設定ファイル → ソケットのディレクトリがあること（無ければ `whiteroom-db serve: DIR: the directory of the sockets does not exist; make it first, which serve does not`。ディレクトリでなければ `…: not a directory; the sockets are made in a directory that exists`）→ データのディレクトリを開く（エンジンの書くロック。別のプロセスが開いていれば、約2秒待って、`whiteroom-db serve: data/logdb: another process holds the writer lock` で失敗する。`logdb/` が無ければ `whiteroom-db serve: emptydir/logdb: missing directory`。保守が動いていれば、すぐに `maintenance is running`）。ロックを取った後に、`files/` の、途中で止まった原本の一時のファイルを消す（3.4の「原本」）→ 書き手のロックのファイル（`writer.lock`）を、無ければ作る（モード0660、グループは `-writer-group`。止めても消さない。ふつうのファイルでなければ、`whiteroom-db serve: DB/writer.lock: not a regular file; it is the lock of the writer` で、別のユーザーのファイルなら、`whiteroom-db serve: chmod DB/writer.lock: operation not permitted` で失敗する）→ ソケットを作る（古いソケットのファイルは、置き換える。ほかのプロセスが待ち受けていれば、失敗する。そのとき、この起動が作ったロックのファイルは消す）。
- 起動の行：2つのソケットを作ったら、標準出力に1行（`serving the data directory DATA on the sockets in DB (writer: user N)`）。`DATA` と `DB` は、フラグか設定ファイルの値のまま、`N` は、書き手のユーザーの番号。管理用のサーバーと、ほかのコマンドは、この後で動かす（下記の「設定の例」）。
- 標準エラーに書くもの（3.6の「頭の書き出し」）：(1) 鎖の頭の行（日時なし。7.4）：`audit head <ログ> <番号> sha256:<16進数>`。起動のとき（記録のあるログのすべて）、1分ごと（変わったものだけ）、止めるときに書く。標準エラーを journald などに残して、保存先の外の頭にする（`whiteroom doctor -heads` で照らす。下記。6.12の236）。保存先を作ったばかりで、記録が無いうちは、書かない。(2) ログの行（日時つき）：空きを読めない（`cannot read the free space (…)`）、同期に失敗して、書くのを止めた（`the store has stopped after a sync failed (…); writes are refused until the daemon is started again after a restart of the machine`）、頭を読めない・書けない（`cannot read the heads: …`・`cannot write a head: …`）。行に、キーも、値も入らない。書けなくても（読む側の終わったパイプでも）、デーモンは止まらず、終了コードも変わらない（上記の「終了コード」）。
- 止める：Ctrl-C（SIGINT）か SIGTERM で、待ち受けを閉じ（ソケットのファイルも、ここで消える。書き手のロックのファイルは残る）、動いている要求が終わるのを10秒まで待ち、最後の頭を書き、エンジンを閉じて、0で終わる。2回目のシグナルでは、待たずに、すぐに終わる。強制終了（SIGKILL・電源の断）では、ソケットのファイルが残る（次の起動が、古いものとして置き換える）。止めるのは、管理用のサーバーと、書くコマンドの後（下記の「設定の例」）。
- 動かし続ける間：デーモンは、データのディレクトリごとに1つ。書き手は1つ：書き手のコマンドは、動いている間、書き手のロックを持ち、別の書き手は、約2秒待って、`DB: another process is connected as the writer` で失敗する（デーモンの `busy` も、同じ文。3.6の「接続」）。同期に失敗したときは、そのデーモンは、書くのを止める（読みは続く。3.6の「同期の失敗」）：マシンを再起動してから、起動し直す（運用の規則。3.5の「運用の制約」。起動を拒むことは #68）。

`init`・`compact`・`gc` は、`-config FILE` と `-data DIR` だけを取る（`data` を使う）：

- `init`：データのディレクトリを作る。無いか、空のディレクトリで、親が無ければ作り、作った名前を同期する（3.4の「作る」）。作るのは、`files/` と、コミットの無い `logdb/` だけで、最初のコミットは、書き手が作る（`whiteroom init`）。成功したら、標準出力に `created the data directory DATA`。空でなければ、何も作らずに、`whiteroom-db init: data: not empty; a new data directory needs a missing or empty directory` で、1で終わる。作った後の失敗（書き込みの失敗など）は、作ったものを消さない：`DATA` を空にしてから、やり直す。
- `compact`：いまの状態を、新しい世代のログに書き直して、古い世代を消す（何も落とさない。3.5の「保守モード」）。成功したら、`compacted DATA`。
- `gc`：いまの世代でないセグメントと、状態が持たないオブジェクトのファイルと、一時のファイルを消す（原本は消さない。6.12の125）。成功したら、`collected garbage in DATA: N segments, N object files, N temporary files`。
- `compact` と `gc` は、デーモンを止めて、サーバーの上で動かす：履歴を消せる操作なので、ソケットには出さない（3.6の「保守の口」）。データのディレクトリを開いているプロセスがあれば、約2秒待って、何も変えずに、`whiteroom-db gc: data: the data directory is in use; stop whiteroom-db serve on it first` で、1で終わる。待つ間も、保守が、エンジンのロックを持つので、動いているデーモンへの `whiteroom backup` は、その間、`whiteroom backup: the engine failed` で、1で終わる（2026-11-16に、動かして確かめた）。
- どれも、データのディレクトリを決めなければ、使い方の誤り（2）。

**`whiteroom init`**

```text
whiteroom init [-config FILE] [-db DIR] -login LOGIN -name NAME -email ADDRESS [-theme DIR] [-url URL] [SITE]
```

動いているデーモン（ソケットのディレクトリ `DIR`）の、できたての保存先（`whiteroom-db init` が作ったデータのディレクトリ。3.6の「実行ファイル」）に、最初のコミットと、最初の管理者のアカウント（7.3の「作る」）を入れ、`SITE` があれば、そのサイトの文書とメディアを取り込む（6.7の「取り込み（案）」）。データのディレクトリは、作らない（開けない。`whiteroom-db init` が作る）。

| 引数・フラグ | 内容 | 省略したとき |
| --- | --- | --- |
| `SITE` | 取り込むサイトのディレクトリ（4.3の「サイトのディレクトリ」） | 取り込まない（設定ファイルの `site` は、取り込まない） |
| `-config FILE` | 設定ファイル（上記。`db`・`theme`・`url` を使う） | 読まない |
| `-db DIR` | 繋ぐデーモンのソケットのディレクトリ。書き手として繋ぐ | 設定ファイルの `db`（無ければ、使い方の誤り） |
| `-login LOGIN`・`-name NAME`・`-email ADDRESS` | 最初の管理者のログイン名・表示名・メールアドレス（7.3） | 必須（空の値は、書かないのと同じ） |
| `-theme DIR` | `SITE/theme` の代わりのテーマ。`SITE` の検証だけに使う | 設定ファイルの `theme`、無ければ `SITE/theme`（`SITE` を書いたときだけ） |
| `-url URL` | `SITE` を、サイト設定の `url` が `URL` のものとして検証する（上記の「環境ごとの `url`」）。保存する設定は、書かれた `url` のまま | 設定ファイルの `url`（`SITE` を書いたときだけ） |

規則（保存先を新しいものに限ること・試しの取り込み・失敗のあとの扱いは、案）：

- 作るもの：保存先の最初のコミット（まだ無ければ。表の版・`store.create` の記録。3.4・7.4）と、ロールが admin の最初の管理者（パスワードつき。`account.CreateFirst`）。`SITE` があれば、その文書とメディアの原本を、最初の管理者が取り込んだものとして保存する（`edit.ImportWith`。設定の文書も）。作るのは、これだけ：既定のテーマも、スキーマのファイルとテーマの雛形も作らない（6.12の69）。`SITE` を書かなかった保存先は、設定の文書が無いので、管理のAPIで最初の設定を保存する（`PUT /api/settings`。9.7）まで、サイトをビルドできない。
- 新しい保存先だけ：保存先に、アカウントも、文書（設定・エントリー・ターム・メディア・著者・メニュー・フォーム・リダイレクト）も、あってはならない。あれば、何も変えずに、`whiteroom init: the store of DB has accounts: init is for a new store`（文書なら `has documents`）で、1で終わる。管理者を作る前に失敗した、前の `whiteroom init` が残した保存先（コミットはあり、アカウントも文書も無い）は、使える。表の版が1〜6の保存先は、開くときに、表の版を上げる（1回のコミット。3.4の「表の版」）ので、そのコミットだけは、断る前に書かれる。
- 確かめる順（先に確かめるのは、聞いても使えないパスワードを、聞かないため）：(1) フラグと引数（`-login`・`-name`・`-email` のどれかが無い・空、引数が2つ以上、`SITE` の無い `-theme`・`-url`。2）、(2) 設定ファイル（2）、(3) ソケットのディレクトリが決まること（2）と、`-url`（2）、(4) 保存先を開くこと（デーモンに2秒で届かない・デーモンが30秒答えない・書き手の接続が別のプロセスのもの・書き手として繋げないユーザー。1。コミットがまだ無ければ、ここで最初のコミットを作る）、(5) 保存先が新しいこと（1）、(6) `SITE` を開くこと（スキーマのファイル・サイト設定・テーマ。1）、(7) `SITE` の試しの取り込み（下記。1）、(8) パスワードを読む（上記の「パスワードの入力」）、(9) 管理者を作る、(10) `SITE` を取り込む。ログイン名・表示名・メールアドレスとパスワードの問題は、管理者を作るときに分かるので、パスワードを読んだ後に、1行ずつ示す（`whiteroom init: -email: …`・`whiteroom init: the password: …`。端末では、パスワードの問題だけのときに、聞き直す。6.12の263）。
- 試しの取り込み：`SITE` は、パスワードを聞く前に、試しの取り込み（保存先に、文書も、記録も保存しない）で確かめる。取り込みを断る問題は、試しの問題で、標準エラーに、ビルドの警告と同じ形で示して（ファイルは、作業ディレクトリからのパス）、`init` は、そこで止まる。試しが置いた原本は、デーモンに残る（どの文書も指さない原本。あとの本取り込みが使う。`whiteroom-db gc` は、原本を消さない。6.12の125）。取り込みの警告は、本取り込みのときに、標準エラーに出る。
- 標準出力：成功したら、`initialized the store of DB`・`created the administrator LOGIN (ID)`、`SITE` があれば、`imported the site SITE`。`DB` は、フラグか設定ファイルのソケットのディレクトリ。標準エラー：取り込みの警告（ビルドの警告と同じ形。ファイルは、作業ディレクトリからのパス）。
- 失敗したとき：`init` は、ファイルを消さない（データのディレクトリを知らず、開かない）。管理者を作る前の失敗（保存先が新しくない・サイトの問題・パスワードを読めない・取り消し）は、保存先に、最初のコミット（と、試しが置いた原本）のほかは、書かない。直して、もう一度 `whiteroom init` を動かせば、その保存先を使う。管理者を作った後の失敗（取り込みの問題・取り消し・書き込みの失敗）は、保存先に、管理者があって、サイトが無い（デーモンが、止まったときに届いていた取り込みのコミットを、保存したことを除く）。標準エラーに、その保存先の名前と管理者のログイン名を言って、やり直す手順を示す（`whiteroom init: the store of DB now has the administrator LOGIN, and no site, unless the daemon stored the import as the command stopped; to start over, stop whiteroom-db, remove its data directory, make it again with whiteroom-db init, start it, and run whiteroom init again`）。やり直すには、デーモンを止め、データのディレクトリを手で消し、`whiteroom-db init` で作り直して、デーモンを動かし、もう一度 `whiteroom init`（6.12の263）。強制終了（SIGKILL・2回目のCtrl-C・電源の断）の後も、保存先は、どちらかのとおり。
- 記録：保存先を作る `store.create`（`system`）、管理者を作る `account.createFirst`（`system`）、取り込む `site.import`（最初の管理者）（7.4）。
- 秘密：パスワードは、標準入力だけ。`-login`・`-name`・`-email` は、引数なので、`ps` に見える（秘密ではないが、個人の情報）。

**`whiteroom serve admin`**

```text
whiteroom serve admin [-config FILE] [-db DIR] [-addr ADDR | -socket PATH] [-origin URL] [-trusted-proxy PREFIX]... [-tmp DIR] [-out DIR] [-cache DIR] [-theme DIR] [-workers N] [-memory MiB] [-url URL] [SITE]
```

デーモン（ソケットのディレクトリ `DIR`）に、書き手として繋ぎ（接続は1つ。読むための接続は、読み手用のソケットに、要るだけ。3.6の「接続」）、管理用のサーバー（9.6）と管理のAPI（9.7）を、止めるまで動かす。データのディレクトリは開かず（開くのはデーモンだけ）、鎖の頭も書き出さない（デーモンが、自分の標準エラーに書く。3.6の「頭の書き出し」）。サイトは、起動のときと、公開に結びつく変更の後に、保存先からビルドして、出力先に公開する（`Publisher`。4.4）。`SITE` からは、スキーマのファイルとテーマだけを読む。コマンドは `internal/admin` を変えず、`-origin` と `-trusted-proxy` と `-tmp` から、その設定（`Origin`・`TrustedProxies`・`TempDir`。9.6）を作る。

| 引数・フラグ | 内容 | 省略したとき |
| --- | --- | --- |
| `SITE` | スキーマのファイルとテーマのディレクトリ | 設定ファイルの `site`、無ければ `.` |
| `-config FILE` | 設定ファイル（上記） | 読まない |
| `-db DIR` | 繋ぐデーモンのソケットのディレクトリ | 設定ファイルの `db`（必須） |
| `-tmp DIR` | 管理のAPIの、一時のファイルのディレクトリ（下記）。このサーバーだけが使う | 設定ファイルの `tmp`、無ければ `SITE/tmp` |
| `-addr ADDR` | 待ち受けるloopbackのTCPのアドレス（`IPアドレス:ポート`）。`-socket` とは、どちらか1つ | 設定ファイルの `addr`、無ければ `127.0.0.1:8081` |
| `-socket PATH` | TCPの代わりに、待ち受けるUnixドメインソケットのパス。`-origin` が要る | 設定ファイルの `socket` |
| `-origin URL` | ブラウザーが管理用のサーバーを開くオリジン（9.6） | 設定ファイルの `origin`。無ければ、TCPのときだけ、`http://` と待ち受けたアドレス |
| `-trusted-proxy PREFIX` | `X-Forwarded-For` を信じるリバースプロキシのアドレス（IPアドレスか、CIDRの範囲。繰り返せる） | 設定ファイルの `trustedProxies`、無ければ無し |
| `-out DIR`・`-cache DIR`・`-theme DIR`・`-workers N`・`-memory MiB`・`-url URL` | `whiteroom build` と同じ（上記） | 同じ |

規則：

- 待ち受け：loopbackのTCP（`-addr`）か、Unixドメインソケット（`-socket`）の、どちらか1つ（1つのプロセスは、両方では待ち受けない。6.12の254）。サーバーは、TLSを終えない（9.6）。
  - TCP：`IPアドレス:ポート` で、アドレスは `127.0.0.0/8` か `[::1]` だけ。ホストの名前（`localhost` も。別のアドレスに解決されることがあるため）、空のホスト（`:8081`）、loopbackでないアドレス（`0.0.0.0`・`192.0.2.1`）は、2で断る（`whiteroom serve admin: -addr "0.0.0.0:8081" is not a loopback IP address and a port, such as 127.0.0.1:8081 or [::1]:8081`）。平文のhttpを、ネットワークに出さないため。ポートの `0` は、OSが選ぶ。
  - ソケット：ファイルを、モード0660で作り（作った後に `chmod` する。6.12の254）、止めるときに消す。パスは、Linuxで107バイトまで（長いと、パスにあるものを見る前に、断る：`listen unix PATH: the path of a socket has at most 107 bytes, and this one has 108`）。`@` で始まるパスは断る（Linuxでは、ファイルの無い抽象ソケットになるため。ファイルの名前が `@` で始まるときは、`./` を付ける。設定ファイルの `socket` も同じで、`@` で始まる値は、そのファイルのディレクトリを付けずに、断る）。パスにソケットでないものがあれば、そのまま残して、1で断る。ソケットのファイルがあれば、繋いでみて、繋がれば断り（動いているサーバーがある）、`connection refused` なら、止まったプロセスが残した古いものとして、消して作る（ほかのエラーは断る）。`-socket ""` は使い方の誤り。
  - 決め方：`-addr` と `-socket` の両方を書くのは、使い方の誤り（2）。どちらかを書けば、そのほうだけで決まり、設定ファイルの `addr` と `socket` は見ない。どちらも書かず、設定ファイルに両方あるのは、設定ファイルの誤り（2）。どこにも無ければ、`127.0.0.1:8081`。
- オリジン：`-origin`、設定ファイルの `origin`、TCPのときだけ `http://` と実際に待ち受けたアドレス（既定なら `http://127.0.0.1:8081`）、の順。ソケットには、オリジンを作れるアドレスが無いので、書かないと、2で終わる。リバースプロキシの背後では、TCPでも、公開する `https` のオリジンを書く。オリジンと待ち受けが合うかは、確かめない。オリジンの形（`https`か、localhostのhttp。9.6）は、起動のときに `internal/admin` が確かめ、断れば1で終わる（`whiteroom serve admin: admin: invalid origin "http://example.com": use https, or http on localhost`）。
- 信頼するプロキシ：`-trusted-proxy` は省ける。ソケットから来た接続は、いつも、信頼するプロキシとして扱う（9.6）ので、ソケットのときに書くのは、`X-Forwarded-For` の中の、その先のプロキシだけ。TCPで、リバースプロキシの背後なら、そのプロキシのアドレス（同じマシンなら `127.0.0.1`）を書く。書かないと、すべてのクライアントが、プロキシの1つのアドレスとして数えられ、試行の制限とログイン履歴が、クライアントを区別できない（9.6の「設定の責務」）。コマンドは、待ち受けから、信頼するプロキシを足さない。フラグを書くと、設定ファイルの並びは使わない。すべてのアドレスを含む範囲（`0.0.0.0/0`）は、2で断る。
- 警告：オリジンが `https` で、待ち受けがTCPで、信頼するプロキシが1つも無いと、起動のときに、標準エラーに1行示す（`whiteroom serve admin: warning: the origin is https, but no trusted proxy is given: behind a reverse proxy every client counts as the proxy; give its address with -trusted-proxy, or trustedProxies in the config file`）。ソケットのときは、示さない。
- 一時のファイル（案。6.12の270）：管理のAPIは、メモリに乗らない本文と答え（256KiBを超えるもの）を、`-tmp` のディレクトリの一時のファイル（`.tmp-` と16進数16桁。モード0600）に置く（9.7の「上限」）：アップロードは、全部受け取ってから、保存先に渡し、原本の取得は、保存先から、先にこのディレクトリへ写してから、送る（遅いクライアントが、デーモンへの接続を持ち続けないため）。デーモンの `files/` には置かない（3.6の「原本」）。ディレクトリは、このサーバーだけが使う、管理用のユーザーだけが読み書きするディスクのもの（ほかのサーバーが同じディレクトリを使うと、起動のときに、その一時のファイルを消す）。無ければ、モード0700で作る（親は作らない：`whiteroom serve admin: mkdir DIR: no such file or directory`）。あるものは、モードを変えない（ディレクトリでなければ、`whiteroom serve admin: DIR: not a directory`）。起動のとき、前のプロセスが残した一時のファイルを消す（名前が `.tmp-` と小文字の16進数16桁のふつうのファイルだけ。ほかのファイルとディレクトリは、消さない）。止めても、ディレクトリは残る。既定の `SITE/tmp` は、サイトのディレクトリの読み込みも、ビルドも、画像のキャッシュの掃除も、見ない。`SITE` をGitで管理するなら、`tmp/` は、`out/`・`cache/` と同じく、無視するものになる。
- 起動の順（失敗したら、そこで止め、開いたものを閉じて、1で終わる。フラグと設定ファイルの誤りは2）：フラグと設定ファイル → `SITE` のスキーマのファイルとテーマ → デーモン（書き手として繋ぐ。届かなければ、2秒待って、`whiteroom serve admin: DIR: cannot reach the daemon: dial unix DIR/writer.sock: connect: no such file or directory` で失敗する。書き手のロックか、書き手の接続が、別のプロセスのものなら（ほかのサーバーか、`whiteroom init`・`account` が動いていれば。デーモンが止まっていても）、同じ2秒の後、`whiteroom serve admin: DIR: another process is connected as the writer`。書き手のユーザーでなければ、すぐに `whiteroom serve admin: the user 1000 is not the writer of this daemon`。デーモンが、接続を受けて、30秒、何も答えなければ、`whiteroom serve admin: DIR: cannot reach the daemon: the daemon has sent nothing for 30s`）→ 一時のファイルのディレクトリ → 待ち受け → アカウント → `Publisher` → サーバー。デーモンが先に動いていること（下記の「設定の例」の起動の順）。
- 起動の行：待ち受けを始めたら、標準出力に1行（`serving admin of the store of DB at ORIGIN (listening on ADDR)`。ソケットは `(listening on socket PATH)`）。`DB` は、フラグか設定ファイルの、ソケットのディレクトリのまま。`ORIGIN` は、サーバーが `Origin` ヘッダーと比べる形（ブラウザーが送る形にそろえたもの。9.6）で、書いた値と違うことがある（`-origin HTTPS://Admin.Example.COM/` は `https://admin.example.com`、`-addr 127.0.0.1:80` だけなら `http://127.0.0.1`）。`curl` などの `Origin` には、この行のものを書く。`ADDR` は実際に待ち受けたアドレス（ポート `0` は、決まった番号）。`PATH` は、フラグか設定ファイルの値のまま（設定ファイルの相対パスは、そのファイルのディレクトリからのもの）。
- 起動のビルド：起動したら、保存先のいまの内容のビルドを始める（4.4）。ビルドの結果は、標準出力に書かない（管理のAPIの `GET /api/builds` で読む。9.7）。`whiteroom build -db` を、動いているサーバーの横で動かすときは、サーバーとは別の `-out` と `-cache` を渡す（6.12の104。下記の「保存先からの `whiteroom build`」）。
- 標準エラーに書くもの：(1) 上記の警告。(2) エラーのログ（日時つきの行）：サーバーのエラー、信頼しない相手の `X-Forwarded-For`（9.6の「記録」）、`the record of <操作> was not written: …`、`cannot record login.suppressed: …`、`closing the store: …`、デーモンに届かない・30秒、答えない（`the daemon of the store cannot be reached: …`。1分に1行まで。下記）。鎖の頭の行は、書かない（デーモンが書く。上記の「`whiteroom-db`」）。書けなくても（読む側の終わったパイプでも）、サーバーは止まらない（上記の「終了コード」）。
- デーモンが無い間：起動した後は、デーモンが止まっても、サーバーは止まらない。保存先を使う要求（ログインと、Cookieのあるセッションの確かめに、保存先を読むので、ログインを要する口のすべて。Cookieの無い要求は、保存先を読まずに、ふつうに401）は、デーモンに2秒届かなければ、503（`busy`。`Retry-After: 5`。9.6・9.7）で答え、ログに、1分に1行（`the daemon of the store cannot be reached: DIR: cannot reach the daemon: dial unix DIR/reader.sock: connect: no such file or directory`）。デーモンが、接続を受けて、30秒、答えなければ、期限が30秒より長い要求（アップロード・原本の取得・ログ。9.7）は、同じ503と、同じ行（`the daemon of the store cannot be reached: DIR: cannot reach the daemon: the daemon has sent nothing for 30s`）で終わる。期限が30秒の要求は、要求の期限が先に来て、503になる（行は無い。9.6）。長い要求の途中で止まったデーモン（`Verify`・`Commit` の答えなど）は、見分けられず、要求の期限まで待つ（3.6の「接続」。#136）。デーモンが戻れば、次の要求から、新しい接続で動く（サーバーの起動し直しは要らない。ログインしていたセッションは、そのまま使える。Cookieは、この503では消さない）。書き手の接続は、次の書き込みで張り直す（それまでの間は、別の書き手が繋げる。下記）。
- 止める：Ctrl-C（SIGINT）か SIGTERM で、次の順に止める：(1) HTTPのサーバー（待ち受けを閉じる。ソケットのファイルも、ここで消える。処理中のリクエストが終わるのを10秒まで待つ。終わらないものは、接続を閉じる）、(2) `Publisher`（動いているビルドは、取り消す）、(3) 1分ごとの仕事（数えたログインの失敗を、最後に書く。5秒まで）、(4) デーモンへの接続を閉じる。0で終わる。2回目のシグナルでは、待たずに、すぐに終わる。HTTPのサーバーか `Publisher` が、自分で失敗したときも、同じ順に止めて（処理中のリクエストも、同じに待つ）、エラーを示し、1で終わる。強制終了（SIGKILL・電源の断）では、最後の1分の、数えたログインの失敗を失い、ソケットのファイルが残る（次の起動が、古いものとして置き換える）。デーモンは、止めない：デーモンは、管理用のサーバーを止めた後で止める（下記の「設定の例」）。
- 同じデーモンに繋ぐ、2つ目のサーバーは、書き手のロックで失敗する（上記）。`whiteroom init`・`whiteroom account reset`・`account delete` も、書き手として繋ぐので、サーバーが動いている間は動かない。`whiteroom backup`・`doctor`・`build -db` は、読み手なので、動いていてもよい。
- デーモンを動かし直した後、サーバーの次の書き込みまでは、サーバーの書き手の接続が無いが、サーバーが書き手のロックを持つので、その間も、2つ目のサーバーと `whiteroom init`・`account` は断られる（3.6の「接続」の「書き手のロック」。#126）。直す前は、その間に動かした書き手が繋がった：2つ目のサーバーは起動して動き、先のサーバーは、書き込み（ログインも）に、2秒ずつ待って、500で答えた（2026-11-16と17に、1つのUnixユーザーで確かめた。2026-11-17のログインは、4.1秒で500）。先のサーバーと同じ設定で動かした2つ目も、一時のファイルのディレクトリを触る前に、書き手のロックで失敗する。書き手のロックを取らないプログラムは、ロックでは止まらない（デーモンが入れる書き手は1つで、保存先は壊れない。残る取り違えは、3.6の「答えが来ないとき」）。
- 1分ごとの仕事：数えただけで、まだ書いていないログインの失敗（7.4の `login.suppressed`。6.12の235）を書く（`FlushSuppressed`）。止めるときにも、1回行う。
- 記録：書くプロセスとして、管理のAPIの操作（ログイン履歴とアクティビティログ。7.4）と、ビルドの結果を、保存先に足す。コマンド自身は、表の版が古い保存先に、書き手として繋いだときの更新の記録（`store.upgrade`。3.4）のほかは、足さない。
- 秘密：コマンドは、パスワードもトークンも扱わない（標準入力は読まない）。セッションのトークンは、管理のAPIの `Set-Cookie` だけで渡り、招待と再設定のトークンは、管理のAPIの答えの本文だけにある（9.6・9.7）。標準エラーに書くのは、鎖の頭ではなく、日時つきのログだけ（上記）。

**保存先からの `whiteroom build`**

```text
whiteroom build -db DIR [-config FILE] [-out DIR] [-cache DIR] [-theme DIR] [-workers N] [-memory MiB] [-url URL] [SITE]
```

`-db DIR` か、設定ファイルの `db` を決めると、ビルドは、サイトのディレクトリでなく、デーモン（ソケットのディレクトリ `DIR`）の保存先の文書と原本から作る（4.4の「スナップショット」）。`SITE` からは、スキーマのファイルとテーマだけを読む。ほかは、サイトのディレクトリからのビルドと同じ（出力先・キャッシュ・`-theme`・`-workers`・`-memory`・`-url`、警告・エラー・結果の行、取り消し。上記）。

- 読み手として繋ぐ：読み手用のソケットに繋ぎ、保存先に何も書かず、記録（7.4）も足さない。予約の公開（4.4）もしない（書く操作のため）。動いている `whiteroom serve admin` が、同じデーモンに繋いでいてもよい。デーモンに届かなければ、2秒待って、`whiteroom build: DIR: cannot reach the daemon: dial unix DIR/reader.sock: connect: no such file or directory` で、1で終わる（出力先もキャッシュも、作らない）。デーモンが、接続を受けて、30秒、何も答えなければ、`whiteroom build: DIR: cannot reach the daemon: the daemon has sent nothing for 30s` で、1で終わる（同じく、何も作らない）。
- 動いているサーバーの横で：サーバーとは別の `-out` と `-cache` を渡す（画像のキャッシュのディレクトリは、同時に動く2つのビルドで共有できない。4.3の「画像のキャッシュ」。6.12の104）。サーバーと同じ出力先で、サーバーのビルドが動いていれば、`whiteroom build: another build is running in OUT` で、1で終わる。動いていなければ、同じ文書と同じ `url` から、同じ名前のリリースができ、`release … in OUT is already published` になる。
- 行の名前：保存先の文書は、名前（`menus/mnu_….json` など）のまま示し、`SITE` を前に付けない。スキーマのファイルは `SITE/site/schema.yaml`、テーマは `-theme` か `SITE/theme` から。
- 設定の文書が無い保存先は、1で失敗する（`whiteroom build: the store of DB has no site settings: whiteroom init imports those of a site, and the admin API saves them (PUT /api/settings)`）。出力先もキャッシュも、作らない。
- 設定ファイルに `db` があると、`-config FILE` つきの `whiteroom build` は、デーモンの保存先からビルドする（項目を消すフラグは無い。6.12の255）。ビルドの記録と、予約の公開をしないことの残りは6.12の257。
- 用途：ステージングのビルド（上記の `-url` と、「設定の例」）と、`whiteroom doctor` が見ないもの（テンプレートの実行・画像。下記）の確かめ。

**`whiteroom rollback`**

```text
whiteroom rollback OUT [RELEASE]
```

出力先 `OUT` の公開中のリリースを、残っているリリース `RELEASE` に切り替える（`OUT/current` を変える。4.4の「戻す操作」）。`RELEASE` は、`whiteroom build` か `whiteroom serve admin` が作った名前。省くと、公開中のリリースがあって、公開中でないリリースが、ただ1つのとき、それ（2つ以上あれば、名前を挙げて、`RELEASE` を求め、無ければ、`no release to roll back to: no other release is left`。公開中のリリースが無いときは、残っているリリースが1つでも、名前を挙げて、`RELEASE` を求める：`no release is published, so there is no release to go back to: give RELEASE, one of …`。どれも1）。ビルドは、置き換えたリリースを残す（4.3）ので、戻した後に、もう一度戻すと、元に戻る。

- デーモンに繋がず、設定ファイルを読まない。ビルドも、`Publisher` も通さない：記録（`release.rollback`。7.4）を足さず、動いている `whiteroom serve admin` の自動のビルドも、止めない。そのサーバーは、次のビルドで、戻したリリースを置き換える。止めて戻すのは、管理のAPI（`POST /api/releases/{name}/rollback`。9.7）。コマンドの操作を、記録するか、は6.12の240。
- 出力：切り替えたら、標準出力に `rolled back to release NAME in OUT (was BEFORE)`（公開中のものが無かったときは、`(was …)` なし）と、標準エラーに、動いているサーバーが置き換えることの警告を1行（`whiteroom rollback: warning: a running whiteroom serve admin replaces this release in its next build; to keep it, roll back through the admin API, POST /api/releases/{name}/rollback, which stops the builds until they are resumed`）。すでに公開中なら、`release NAME in OUT is already published` だけで、0。`BEFORE` と、省いた `RELEASE` の選び方は、出力先のロックを取る前に見た一覧によるので、その間に、別のビルドが公開すると、`BEFORE` が、切り替える直前のリリースでないことがある（切り替えは正しい。6.12の264）。
- 失敗（1）：`OUT` が無い、リリースの名前が正しくない（16桁の16進数でない。`invalid release name "x"`）、そのリリースが `OUT` に残っていない、`OUT` を別のビルドか戻す操作が使っている（待たずに断る）。`OUT` を書かないことと、引数が3つ以上は、使い方の誤り（2）。
- `whiteroom serve public` が `OUT` を配信していれば、戻した後の最初のリクエストから、戻したリリースを配信する（9.5）。

**`whiteroom backup`**

```text
whiteroom backup [-config FILE] [-db DIR] DEST
```

デーモン（ソケットのディレクトリ `DIR`）のデータのディレクトリの写しを、`DEST` に取る。デーモンが、写しのファイルを、ソケットで送り、このコマンドが、`DEST` に書く（コマンドは、データのディレクトリを開かない）。`DEST` は、引数だけで決め（設定ファイルには書けない）、無いか、空のディレクトリ。親のディレクトリは、あること（無ければ、1で断る。親は作らない）。`DEST` が無ければ、`DEST` だけを、モード0700で作り、親のディレクトリを同期する（`DEST` の名前を、ディスクに置く。3.4の「作る」）。`-config` と `-db` は、ほかのコマンドと同じ（上記）。

- 動き：読み手用のソケットに繋ぎ、デーモンが始めたときの、最後の永続的なコミットまでを写す（`store.Backup`。3.6の「保守の口」）。動いている `whiteroom serve admin` が、その間も書き続けてよく、コマンドは、保存先に何も書かない。デーモンは、`Backup` のために、エンジンの読み手を、自分の中で開き、読み手が開いて、写す位置が決まるまでの間だけ、`Commit` を待たせ（ファイルを送る間は、待たせない）、メモリを多く使う（3.6の「保守の口」「測った値」。330MiBのデータのディレクトリで、待たせた `Commit` は最長62〜116ミリ秒、写すのは0.774〜0.859秒。2026-11-16。1つのマシン）。写しは、データのディレクトリと同じ形（`logdb/` と `files/`）で、表の版も、エンジンの形の版も、そのまま（3.4の「表の版」のとおり、同じ版か、後の版の本体で開ける）。写しも1つのデータのディレクトリなので、デーモンを、その上で動かせる（下記の「写しの確かめ」）。
- 入るもの：文書・履歴・リビジョン・原本・アカウント・セッション・ログイン履歴とアクティビティログ（パスワードのハッシュ、セッションのハッシュ、アドレスとログイン名などの個人の情報も）。入らないもの：出力先・画像のキャッシュ・スキーマのファイルとテーマ（開発者のファイル。Gitなどで持つ。6.6.8）・外に書き出した鎖の頭。そのため、写しは、データのディレクトリと同じに守る（ディレクトリ0700・ファイル0600。3.4）。置き場所・世代・暗号化は、使う人が決める（6.12の258）。
- 標準出力：`backed up the store of DB to DEST`、続けて、写しの鎖の頭を、記録のあるログごとに1行（`audit head <ログ> <番号> sha256:<16進数>`。最初の行は頭ではない）。頭は、個人の情報を持たない。そのまま `whiteroom doctor -heads` に渡せる（頭ではない行は、読み飛ばされる）ので、ファイルに残す（`| tee heads1.txt`）。写しとは別の場所に残す。標準エラーには、何も書かない。
- 失敗：`DEST` が、ディレクトリでないもの・空でないディレクトリ、親が無いときは、何も作らずに、1で終わる（`whiteroom backup: backup1: not empty; DEST must be missing or an empty directory`）。`DEST` は、デーモンに繋ぎ、表の版と `DEST` の場所（下記）を確かめてから作るので、それより前の失敗（デーモンに届かない・デーモンが30秒答えない・表の版が古い・`DEST` がデータのディレクトリの中）も、何も作らずに、1で終わる（デーモンに届かないとき：`whiteroom backup: DIR: cannot reach the daemon: dial unix DIR/reader.sock: connect: no such file or directory`。30秒、答えないとき：`whiteroom backup: DIR: cannot reach the daemon: the daemon has sent nothing for 30s`。写しの位置が決まるまでの途中で止まったデーモンは、見分けられず、止める合図まで待つ。上記の「終了コード」）。`DEST` を作った後の失敗（写している途中の、デーモンの停止と、30秒、何も送らないことと、取り消し）は、この実行が作ったものだけを消して（`DEST` が無かったなら、`DEST` も）、1で終わる。`DEST` を書かない・2つ以上書く・ソケットのディレクトリを決めていないのは、使い方の誤り（2）。
- 表の版が7より前の保存先は、写せない（監査の記録の頭の行が無く、写しの頭を読めないため。何も作らずに、1で終わる：`whiteroom backup: the store has a table schema before 7, which has no rows of the heads; open it as its writer once to upgrade it`）。先に、`whiteroom serve admin` で1度繋ぐ（書き手として繋ぐと、表の版が上がる。3.4）。保存先がまだ無いデータのディレクトリ（`whiteroom-db init` の後で、`whiteroom init` の前）も、何も写さずに断る（`whiteroom backup: DIR: not a store of whiteroom (no schema version)`）。
- 動いているデーモンの保守（`whiteroom-db compact`・`gc`）が、エンジンのロックを待つ約2秒の間は、`whiteroom backup: the engine failed` で、1で終わる（3.6の「保守の口」）。
- `DEST` の場所（#124）：`DEST` が、デーモンのデータのディレクトリそのものか、その中なら、何も作らずに、1で断る（`whiteroom backup: DATA/files/b: DEST is in the data directory of the daemon`）。デーモンが `Hello` の答えで伝える、データのディレクトリの識別（デバイス番号と inode。Linuxだけ）を、`DEST` と、その親（シンボリックリンクと `..` は、カーネルのとおりに解く。`DEST` が無ければ、親から）と比べる（3.6の「保守の口」）。データのディレクトリそのものは、空でないので、その前に断る（`not empty`）。別のマウント名前空間で動くデーモン（コンテナーなど）は、デバイス番号が違いうるので、確かめが効かない：`DEST` を、データのディレクトリの中に置かない。理由：デーモンは、`files/` を、名前の順にたどって送り、コマンドが `DEST` に書いたものに、たどっている先で出会うと、それも送る。直す前は、`DEST` を `files/` の中（`<データのディレクトリ>/files/b`）にした実行は、終了コード0で終わり、`b/` は、保存先の `files/` に残って、あとのバックアップも、それを持った（次に `files/c` に取ると、`c/` の中に、`b/` と、原本が重なって入った）。写しの中に、原本が何重に入るかは、デーモンが、そのディレクトリを読むときに、コマンドが、そこまでに何を書いていたかで決まり、上限の保証は無かった（見本のサイトの6つの原本で、2026-11-16の、窓の前の木では3重、後の木では5回とも1重。2026-11-17の `main` では、`files/c` の写しに12）。設定の例（別のUnixユーザーで、データのディレクトリが0700）では、`DEST` のユーザーが、データのディレクトリに書けないので、権限も `DEST` を断る。
- 記録：保存先にも写しにも、記録（7.4の操作 `backup`）を足さない。読み手のプロセスは、保存先に書けないため（6.12の234）。残りの論点（世代・圧縮・暗号化・`Check` を動かさないこと・同じ `DEST` に、同時に書くプロセスがあるときの後始末）は6.12の258。
- 写しの確かめ：写しは、データのディレクトリなので、もう1つのデーモンを、別のソケットのディレクトリで、写しの上で動かし（`whiteroom-db serve -data COPY -db OTHER`。書き手として開くので、切れかけのログの末尾を切る。3.5の「復旧」）、`whiteroom doctor -db OTHER -heads HEADS`（下記）で確かめる。`backup` は、写しを確かめない。写しから同じ名前のリリースをビルドできることは、テストが確かめる（`cmd/whiteroom` の `TestBackupBuildsSameRelease`。10章）。

戻す手順（写しから戻すコマンドは無い。写しを、データのディレクトリの場所に置くこと。3.5の「移行・バックアップ・リストアの条件」）：

1. 管理用のサーバーを止める（`whiteroom serve admin` を、Ctrl-C か SIGTERM で止める）。続けて、デーモンを止める（`whiteroom-db serve` を、同じに止める。止めるときに、最後の頭が、標準エラーに書かれる）。動いているプロセスは、開いたときのデータのディレクトリのつもりで書き続けるため。
2. 壊れたデータのディレクトリを、別の名前に変えて残し（`mv data data.old`）、写しを、その場所に置く（`cp -a backup1 data`。モードを保つ：ディレクトリ0700・ファイル0600。持ち主は、デーモンのユーザー：別のUnixユーザーに分けた形では、デーモンのユーザーで写すか、root で持ち主を替える。動かしていない。6.12の262）。
3. デーモンを起動する（`whiteroom-db serve`）。
4. `whiteroom doctor -config whiteroom.yaml -heads HEADS SITE` で確かめる（`HEADS` は、`backup` が標準出力に書いた頭の行のファイル。それより前に書き出した頭を、足してもよい）。問題が無ければ（0）、次へ進む。
5. 管理用のサーバーを起動する。

戻した後に起こること：写しは、取ったときの保存先そのものなので、アカウントとセッションの行も、写しのときに戻る（パスワードの変更は、2026-11-16に、デーモンを通して動かして確かめた。ほかは、2026-11-14に、デーモンを入れる前の形で、この順に動かして確かめた：ログアウトで終えたセッション、写しの後に作ったセッション、無効化。どれも、行を書き戻すだけなので、デーモンを通しても、同じ理由で、そうなる）。

- 写しの後に終えたセッション（ログアウト・再設定・無効化で終えたもの）は、期限の中なら、また使える。写しの後に作ったセッションは、無い（ログインし直す）。
- 写しの後に変えたパスワードは、写しのときのものに戻る（新しいパスワードでは、ログインできない）。
- 写しの後に無効にしたアカウントは、有効に戻る（写しの後に消したアカウントも、戻る。写しの後に作ったアカウントは、無い）。
- 写しの後の文書・メディア・記録は、無い。
- いまのコードでできる直し方：アカウントごとに、`whiteroom account reset LOGIN`（パスワードを決め直し、そのアカウントのセッションをすべて終える。サーバーを止めて動かす。下記）か、管理のAPIの `POST /api/accounts/{id}/reset`・`POST /api/accounts/{id}/disable`（どちらも、そのアカウントのセッションをすべて終える。9.7）。すべてのアカウントのセッションを、1度に終える口は、無い（6.12の259）。
- `whiteroom doctor -heads` には、写しを取ったときまでの頭（`backup` が書いた頭と、それより前に書き出された頭）を渡す。写しの後に、デーモンが書き出した頭は、戻した鎖に無いので、`audit` の問題として示される（正しい動き。`TestDoctorHeads`）：`audit: audit log "activity": the head kept outside the store is record 7, but the log ends with record 4 (event 9)`。

**`whiteroom doctor`**

```text
whiteroom doctor [-config FILE] [-db DIR] [-heads FILE] [-theme DIR] [-url URL] [SITE]
```

デーモン（ソケットのディレクトリ `DIR`）の保存先の問題を調べて、示す。何も直さない（直す道具は、まだ無い。6.12の131）。

| 引数・フラグ | 内容 | 省略したとき |
| --- | --- | --- |
| `SITE` | スキーマのファイルとテーマを読むディレクトリ。あれば、公開するものの検証もする | 設定ファイルの `site`。無ければ、サイトを確かめない（`.` にはしない） |
| `-config FILE` | 設定ファイル（上記） | 読まない |
| `-db DIR` | 調べる保存先のデーモンの、ソケットのディレクトリ | 設定ファイルの `db`（必須） |
| `-heads FILE` | 保存先の外に書き出した鎖の頭のファイル。`-` は標準入力 | 照らさない |
| `-theme DIR` | `SITE/theme` の代わりのテーマ（`SITE` のときだけ） | 設定ファイルの `theme`、無ければ `SITE/theme` |
| `-url URL` | サイトのURLの上書き（`SITE` のときだけ。上記の「環境ごとの `url`」） | 設定ファイルの `url` |

- 動き：読み手用のソケットに繋ぎ、保存先に何も書かず、何も直さない（`whiteroom serve admin` が動いていてもよい）。`engine` の確かめは、デーモンの `Verify`（3.6の「保守の口」）で、デーモンが、すべてのログをディスクから読み直す間、デーモンの `Commit` を待たせる（管理用のサーバーの書き込みも、その分、待つ。読みは続く）。1回の `doctor` が待たせるのは、`Verify` の1回分：330MiBのデータのディレクトリで、`Verify` は0.189〜0.260秒、その間に送った `Commit` は、197〜256ミリ秒待った（2026-11-16。1つのマシン。ほかの大きさでは、測っていない。3.6の「測った値」）。
- 費用：`engine`・`index`・`original` の確かめ（`store.Check`）にかかる時間は、サンプルサイトだけ（投稿0件）・投稿1,000件・5,000件・20,000件の保存先で、8.46ミリ秒・288ミリ秒・1,396ミリ秒・5,599ミリ秒（5回の中央値。2026-11-16。1つのマシンで、デーモンを、テストのプロセスの中に置き、ソケットのファイルで繋いだ測り。3.6の「測った値」）。`audit` と `site` の確かめと、`-heads` の読みは、含まない。費用は、データのディレクトリの大きさに比例する（原本の確かめは、いまのメディアの原本の合計の大きさに比例する。原本の大きいサイトでの時間は、測っていない。6.12の260）。
- 見るもの：次の領域の順に調べ、領域ごとに、問題を示す。
  - `open`：デーモンに届かない（2秒待つ）か、30秒、答えないか、保存先を読めない（コミットがまだ無いときも）。ほかは、調べない。
  - `engine`：エンジンのログとオブジェクト（`Verify`。3.5）。
  - `index`：索引の行（`summaries`・`references`・最後の番号）が、文書から作る行と合うか（3.4の「索引」）。
  - `original`：いまのメディアの文書が指す原本が、あり、ハッシュが合うか（3.4の「原本」）。
  - `audit`：監査の記録の鎖（`VerifyAudit`。7.4）。`-heads` があれば、その頭が、その番号の記録のハッシュと合うか。
  - `heads`：`-heads` のファイルの、頭として読めない行と、頭が1つも無いこと。
  - `site`：`SITE` があるとき、公開するものを、ビルドが、リリースを書く前にする検証（`build.Check`。公開するドキュメントとスキーマ・参照・リダイレクト・出力するパス。4.3・6.7）で調べる。スキーマのファイルや使う言語を変えた後に、通らなくなる公開中のものも、ここで分かる（6.9・6.12の109）。設定の文書が無い保存先は、`site: the store has no site settings` の1行。
- 見ないもの：下書きと、公開していないリビジョン、履歴の版だけが指す原本と、どの文書も指さない原本（6.12の125）、アカウントとセッションの表、`serve admin` の設定（`-origin`・`-trusted-proxy` など。6.12の254）、テンプレートの実行と画像（ビルドが、検証の後にすること）。後の2つは、別の `-out` と `-cache` に、`whiteroom build -db` をして確かめる。
- 出力：問題は、標準出力に、1行ずつ、領域で始めて示す（`<領域>: <対象>: <理由>`か、`<領域>: <理由>`）。問題の行に、文書の中身は出ない。最後に、まとめの1行：`no problems found in DB`（0）、`N problems found in DB`（1）、1000件で止めたときは `stopped after N problems in DB`（1）。`DB` は、ソケットのディレクトリ。
- 確かめを動かせなかったとき（`-heads` のファイルを開けない・`SITE` を開けない・中断など）：標準エラーに `whiteroom doctor: <理由>` を示し、まとめの行は示さず、1で終わる。デーモンに届かないときは、確かめが始まっていないので、`open` の問題（`open: DB: cannot reach the daemon: dial unix DB/reader.sock: connect: no such file or directory` と `1 problem found in DB`）で、1で終わる。デーモンが、接続を受けて、30秒、答えないときも、同じ（`open: DB: cannot reach the daemon: the daemon has sent nothing for 30s` と `1 problem found in DB`。30〜31秒の後）。確かめの途中で、デーモンに届かなくなったとき（短い要求が、30秒、答えられなかったときも）は、届かないことを、問題としては示さず、標準エラーに `whiteroom doctor: DB: cannot reach the daemon: …` を示して、まとめの行なしで、1で終わる（それまでに見つけた問題の行は、出ている）。`Verify` と、監査の確かめ（`Events`）の途中で止まったデーモンは、見分けられず、止める合図（Ctrl-C）まで待ち、標準エラーに `whiteroom doctor: interrupted` を示して、まとめの行なしで、1で終わる（上記の「終了コード」）。`SITE` が決まらないときは、サイトを確かめるはずだった場合にだけ（保存先を読めて、問題が1000件に達していないとき）、標準エラーに、サイトを確かめなかったことの1行（`whiteroom doctor: note: no SITE, so the published documents are not checked against the schema file and the theme; give SITE, or site in the config file`）を示す（ほかに問題が無ければ、0）。
- 終了コード：0（問題なし）、1（問題があるとき。確かめを動かせなかったとき）、2（使い方の誤り。`SITE` の無い `-theme`・`-url` も）。
- `-heads`：保存先の外に書き出した頭を、デーモンの標準エラーのログ（頭の行のほかに、日時つきの行がある）と、`backup` の標準出力を、そのまま渡せる：`audit head ` を含む行だけを使い、ほかは読み飛ばす（`-` は標準入力。1行は1MiBまで）。頭として読めない行は、`heads` の問題で、頭が1つも読めなければ、それも `heads` の問題（`heads: FILE: no line with a head of an audit log`）。journald から、サービスの行だけを取り出す例は、まだ無い（6.12の262）。
- 写しの確かめ：写しの上で、もう1つのデーモンを、別のソケットのディレクトリで動かし、そのディレクトリを渡す（上記の `backup` の「写しの確かめ」）。
- 例は、下の「例」（写しの確かめ・頭との照らし合わせ・原本の欠けた保存先）。

**`whiteroom account`**

```text
whiteroom account reset [-config FILE] [-db DIR] LOGIN
whiteroom account delete [-config FILE] [-db DIR] ID
```

アカウントの復旧のための、サーバー上のコマンド（7.3。システム `account.System` として動かす）。`-config` と `-db` は、ほかのコマンドと同じ。デーモンに、書き手として繋ぐので、書き手の接続を持つ `whiteroom serve admin` を止めてから動かす（デーモンは、動かしたままにする）。動いていると、約2秒待って、`whiteroom account reset: DB: another process is connected as the writer` と、サーバーを止めることと、管理のAPIで同じことができること（`POST /api/accounts/{id}/reset`・`DELETE /api/accounts/{id}`。別の管理者がログインできるとき）を言う1行を示して、1で終わる。デーモンに届かなければ、同じ2秒の後、`whiteroom account reset: DB: cannot reach the daemon: dial unix DB/writer.sock: connect: no such file or directory` で、1で終わる。デーモンが、接続を受けて、30秒、何も答えなければ、`whiteroom account reset: DB: cannot reach the daemon: the daemon has sent nothing for 30s` で、1で終わる。書き手のユーザーでなければ、すぐに `whiteroom account reset: the user 1000 is not the writer of this daemon` で、1で終わる。サーバーが動いている間は、デーモンを動かし直した後の、サーバーの書き手の接続が無い間も、断られる（サーバーが書き手のロックを持つ。上記の「`whiteroom serve admin`」）。

- `reset LOGIN`：ログイン名が `LOGIN` のアカウントに、新しいパスワードを決める。パスワードは、標準入力だけから読む（上記の「パスワードの入力」。促しは `New password for LOGIN: `）。パスワードは、先に `account.CheckPassword` で確かめる。アカウントを再設定して（パスワードを消し、セッションをすべて終える）、続けて、そのパスワードを設定する（再設定のトークンは、プロセスの中だけで使い、出力しない）。アカウントは、有効になる。最後の有効な管理者・招待されたままで、パスワードの無いアカウント・2つの手順の間で切れたアカウントにも使える。無効のアカウントは断る（`the account X (ID) is disabled: enable it first, with the admin API`）。読めない行が1つでもあれば、その ID を言って、止まる（`delete it with: whiteroom account delete -db DB ID`。`DB` は、空白などがあれば、シェルに貼れるように、`'…'` で囲む）。2つ目の手順に失敗すると、アカウントにパスワードが無いので、もう一度動かす（`the account X has no password now: run the command again`）。成功したら、標準出力に `reset the password of LOGIN (ID)`。記録は、`account.reset`（`system`）と `account.setPassword`（そのアカウント）。終わったセッションは、ログイン履歴の `session.end`（7.4）。
- `delete ID`：IDのアカウントを消す。読めない行（`reset` が名指しする `accounts "usr_…": invalid value`）は、その行だけを先に読み、ほかの行を読まずに消す。ほかのアカウントは、アカウントの規則（7.3の「無効と削除」）のとおり（最後の有効な管理者は消せない）。セッションは終わる。文書は変わらない（作った人・保存した人の記録は残る）。標準入力は読まない。成功したら、標準出力に `deleted account ID (LOGIN)`（読めない行は `deleted account ID (its row could not be read)`）。記録は `account.delete`（`system`）。
- アカウントの一覧を示すコマンドは無い（ログイン名は、管理のAPIの `GET /api/accounts` で見る。6.12の261）。
- 失敗（1）：アカウントが無い（`whiteroom account reset: no account has the login name "nobody"`）、デーモンに繋げない・デーモンが30秒、答えない・書き手の接続を取れない（上記）、`delete` の ID が無い（`whiteroom account delete: accounts "usr_…": not found`）。使い方の誤り（2）：`LOGIN`・`ID` が無い・2つ以上、ソケットのディレクトリを決めていない。

**秘密**

コマンドは、パスワードとトークンを、引数・フラグ・環境変数・設定ファイルで受け取らず、標準出力・標準エラー・ログ・エラーの文に出さない（URLのパスとクエリにも入れない。9.7）。出す例外は、管理のAPIの、セッションのトークンを渡す `Set-Cookie` と、招待と再設定の答えの本文の `token` だけ（9.6・9.7。コマンドは扱わない）。

| もの | コマンドでの扱い |
| --- | --- |
| パスワード（`init`・`account reset`） | 標準入力だけ（端末では、エコーなし）。どこにも出さない（標準エラーには、促しの行だけ）。保存先には、ハッシュだけ（7.3） |
| 再設定のトークン（`account reset`） | プロセスの中だけで使う。出さない |
| セッションのトークン | コマンドは扱わない。管理のAPIの `Set-Cookie` だけ（9.6）。保存先には、ハッシュだけ |
| 鎖の頭 | 秘密ではない。個人の情報も入らない（`backup` の標準出力と、`whiteroom-db serve` の標準エラー） |
| ログイン名・表示名・メールアドレス（`init` の引数） | 引数なので `ps` に見える。秘密ではないが、個人の情報 |
| 設定ファイル | 秘密を書く項目は無い（パス・オリジン・URL・信頼するプロキシのアドレスは、秘密ではない） |
| `backup` の写し | パスワードのハッシュ・セッションのハッシュ・監査の記録（アドレスとログイン名）が入る。データのディレクトリと同じに守る |

ログインのAPIを `curl` で呼ぶときの、本文（`{"login": "…", "password": "…"}`）も、パスワードを含むので、権限0600のファイルに書き、`--data-binary @FILE` で渡す（コマンドの行に書かない）。

**設定の例**

デーモン・管理用のサーバー・コマンドと、下の設定ファイルの形は、2026-11-16に、Linuxで、1つのUnixユーザー（手元のユーザー）で、動かして確かめた（パスだけを、手元のものに替えて。「例」の出力は、そのときのもの）。ステージングの設定ファイルは、`socket` と `origin` を除いた形で、`whiteroom-db serve`・`doctor`・`build` に渡し、その出力先で `rollback` を動かして確かめた。`serve admin` に `url` を渡す形は、テスト `TestServeAdminURL` が確かめる。確かめていないのは、別のUnixユーザーとグループに分けた形：ユーザーとグループを作り、ソケットのディレクトリとデータのディレクトリの持ち主を替えるので、root が要る（この変更の確かめでは、使わなかった）。下の3つのユーザーと、ソケットのディレクトリの持ち主とグループと、2つのソケットのグループは、設定の仕組み（`writerUser`・`writerGroup`・`readerGroup`、ソケットのファイルのモード0660、データのディレクトリの0700。3.6の「接続」）から書いたもので、動かしていない（6.12の262）。1つのユーザーで確かめたのは、次の2つ：`writerGroup` と `readerGroup` に、自分のグループを書くと、2つのソケットのファイルが、モード0660と、そのグループで作られること。`-writer-user` に、別のユーザーの番号を書くと、書き手として繋ぐコマンド（`whiteroom account`・`serve admin`・`init`）は、`the user 1000 is not the writer of this daemon` で、すぐ1で終わり、読み手のコマンド（`doctor`・`backup`）は動くこと。確かめたNginxは1.30.6（root でないユーザー、127.0.0.1の空いたポート、httpで、`location` だけを置いたもの）。この確かめと、SSHのポート転送の確かめは、2026-11-14に、管理用のサーバーが、データのディレクトリを直接開いていた形で動かしたもので、確かめたのは、管理用のサーバーのHTTPの側（`socket`・`origin`・`addr`・`trustedProxies` と `location`）。この側は、デーモンを入れても変えていないが、動かし直していない。TLS（`listen 443 ssl` と証明書）の設定は、書かない（9.3・9.6）。SSHのポート転送は、書き方をマニュアルで確かめただけで、動かしていない。

- 3つのプロセスと、Unixユーザー（9.2の分け方のとおり。ユーザーの名前は、例）：
  - デーモンのユーザー（例：`whiteroomdb`）：`whiteroom-db init`・`serve`・`compact`・`gc` を動かす。データのディレクトリ（ディレクトリ0700・ファイル0600。3.4）は、このユーザーだけのもので、`/var/lib/whiteroom-db` も、このユーザーのもの。ソケットのディレクトリ（例：`/run/whiteroom-db`）も、このユーザーが作る（デーモンは、作らない。`/run` は、起動のたびに空になるので、起動の手順（systemdのユニットは、まだ無い）が作る）：持ち主はデーモンのユーザー、グループは管理用のユーザーのグループ（例：`whiteroom`）、モードは0750（グループが、通れる）。デーモンを動かし直すときも、消さない（中の `writer.lock` を残す。3.6の「接続」）。デーモンが、ソケットのファイルのグループを替える（`chown`）には、デーモンのユーザーが、そのグループ（ここでは `whiteroom`）に入っている必要がある（root で動かすなら、要らない）。
  - 管理用のユーザー（例：`whiteroom`）：`whiteroom init`・`serve admin`・`backup`・`doctor`・`account`・保存先からの `build` を動かす。デーモンの `writerUser` に、このユーザーを書く（書き手として繋げるのは、そのユーザーだけ。既定は、デーモン自身で、それでは、管理用のサーバーは繋げない）。`writerGroup` と `readerGroup` に、このユーザーのグループを書き、2つのソケットのファイル（モード0660）に、そのグループで繋げるようにする。出力先は、このユーザーが、umask 022 で書き（ディレクトリ0755・ファイル0644。4.3）、画像のキャッシュと、一時のファイルのディレクトリ（`tmp`）も、このユーザーのもの。
  - 公開用のユーザー（`whiteroom serve public` を動かすユーザーか、Nginx のユーザー）：出力先を読む。umask が 022 より厳しいと、出力先のモードが狭くなり、公開用のユーザーが読めない。公開用のサーバー（9.5）は、出力先だけを読み、保存先も、デーモンも開かない。
- 設定ファイル（本番）。デーモンと、管理用のサーバーと、コマンドが、同じファイル（例：`/etc/whiteroom/whiteroom.yaml`。秘密を書かないので、3つのユーザーが読めるモード0644）を、`-config` で読む（上記の「設定ファイル」。それぞれ、自分の項目を使い、相手の項目は、型と範囲だけを確かめる）。`socket` は、管理用のユーザーだけが書けるディレクトリに置く：

  ```yaml
  data: /var/lib/whiteroom-db/data
  db: /run/whiteroom-db
  writerUser: whiteroom
  writerGroup: whiteroom
  readerGroup: whiteroom
  site: /srv/whiteroom/site
  out: /var/www/whiteroom/out
  cache: /var/cache/whiteroom
  tmp: /var/lib/whiteroom/tmp
  socket: /run/whiteroom/admin.sock
  origin: https://admin.example.com
  ```

- 起動と停止の順（案。systemdのユニットは、まだ無い。6.12の262・269）：デーモンが先で、管理用のサーバーが後。止めるのは、逆の順。
  - 起動：(1) デーモンのユーザーで、ソケットのディレクトリを作り（上記）、1回だけ `whiteroom-db init -config /etc/whiteroom/whiteroom.yaml`（データのディレクトリを作る）。(2) デーモンのユーザーで、`whiteroom-db serve -config /etc/whiteroom/whiteroom.yaml`（動かしたままにする）。(3) 管理用のユーザーで、1回だけ `whiteroom init -config /etc/whiteroom/whiteroom.yaml -login … -name … -email … /srv/whiteroom/site`（最初の管理者と、サイトの取り込み。パスワードは、標準入力）。(4) 管理用のユーザーで、`whiteroom serve admin -config /etc/whiteroom/whiteroom.yaml`。管理用のサーバーとコマンドは、デーモンに、2秒まで待って繋ぐので、(2) の直後に動かせる。
  - 停止：(1) 管理用のサーバーと、`whiteroom account` などの書くコマンドを止める。(2) デーモンを止める（Ctrl-C か SIGTERM。最後の頭が、標準エラーに書かれる）。逆にすると、管理用のサーバーは、書き込みの途中でデーモンを失い、そのコミットが確定したかどうかが、分からなくなることがある（3.6の「1回の保存の流れ」の「答えが来ないとき」）。起動のあとで、デーモンだけが止まったときは、管理用のサーバーは、止まらずに、503で答え、デーモンが戻れば、続けて動く（上記の「`whiteroom serve admin`」）。
- 設定ファイル（ステージング）。もう1つのデーモン（もう1つのデータのディレクトリと、ソケットのディレクトリ）と、もう1つの管理用のサーバーの組で、同じ形で、`data`・`db`・`out`・`cache`・`tmp`・`socket`・`origin` を変え、`url` を足す。`data` は、本番の写しを戻したもの（上記の「戻す手順」の2まで）：

  ```yaml
  data: /var/lib/whiteroom-db/staging/data
  db: /run/whiteroom-db-staging
  writerUser: whiteroom
  writerGroup: whiteroom
  readerGroup: whiteroom
  site: /srv/whiteroom/site
  out: /var/www/whiteroom-staging/out
  cache: /var/cache/whiteroom-staging
  tmp: /var/lib/whiteroom/staging-tmp
  socket: /run/whiteroom-staging/admin.sock
  origin: https://admin-staging.example.com
  url: https://staging.example.com
  ```

- リバースプロキシ（Nginx）の後ろで動かす：2つの形がある。どちらも、`location` に、`Host` と `X-Forwarded-For` の2行を書く（9.6の「プロキシが渡すもの」。書かないと、`Host` が上流のアドレスになり、管理用のサーバーは421で断る。`X-Forwarded-For` には、プロキシから見た相手が足されない）。本文の上限と、上流を待つ時間の2行は、9.6・9.7の「リバースプロキシ」のとおり。
  - ソケットで繋ぐ形（ほかのユーザーのいるマシンでは、こちらを使う。下記の「どちらを選ぶか」）：`serve admin` を、上の設定ファイル（`socket` と `origin`）で動かし、Nginxは、次の `location` で繋ぐ。`proxy_pass` の `unix:` の書き方は、Nginx の文書（https://nginx.org/en/docs/http/ngx_http_proxy_module.html の `proxy_pass`：「or as a UNIX-domain socket path specified after the word “unix” and enclosed in colons」。2026-11-14に確かめた）による。

    ```nginx
    location / {
        proxy_pass http://unix:/run/whiteroom/admin.sock:;
        proxy_set_header Host $http_host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        client_max_body_size 65m;
        proxy_read_timeout 120s;
    }
    ```

    ソケットのモードは0660なので、Nginxのユーザーが繋げるように、Nginxのユーザーを、管理用のユーザーのグループに入れるか、ソケットを置くディレクトリを、Nginxのユーザーが入っているグループの、setgidのディレクトリ（`chmod 2750`）にする（setgidのディレクトリの中で作ったソケットは、そのディレクトリのグループになる）。繋ぐには、ソケットのファイルへの書き込みの権限と、そのディレクトリを検索する権限が要る（2026-11-14に、Linuxで、ファイルのモードを変えて確かめた。別のユーザーとグループから繋ぐことは、確かめていない。6.12の262）。ソケットに繋げるプロセスは、どれも `X-Forwarded-For` を書けるので、モードとグループで、繋げるものを、プロキシだけにする。ソケットから来た接続は、いつも信頼するプロキシとして扱う（9.6）ので、`trustedProxies` は、要らない。
  - loopbackのTCPで繋ぐ形：設定ファイル（上の本番のもの）の `socket` の行を、`addr` と、`trustedProxies` に `127.0.0.1` を書く行に替え、`location` の `proxy_pass` を `proxy_pass http://127.0.0.1:8081;` にする（ほかの4行は、同じ）：

    ```yaml
    addr: 127.0.0.1:8081
    trustedProxies:
      - 127.0.0.1
    ```

    `trustedProxies` を書かないと、起動のときに警告が出て、すべてのクライアントが、127.0.0.1（Nginx）として数えられる（2026-11-14に確かめた）。
  - どちらを選ぶか：loopbackのポートには、同じマシンのどのユーザーのプロセスも繋げる（ソケットのような、ファイルの権限が無い）。127.0.0.1 を信頼していると、そのプロセスは、`X-Forwarded-For` を偽って、クライアントごとの枠を逃れ、ログイン履歴に、偽ったアドレスを残せる（操作には、ログインが要る。9.6の「設定の責務」）。そのため、ほかのユーザーのいるマシンでは、ソケットを使う。別のマシンやコンテナのリバースプロキシから、TCPで繋ぐことは、できない（loopbackでないアドレスは、断る。6.12の254）。
  - 確かめたこと（2026-11-14。Nginx 1.30.6。同じマシンで、`curl --interface 127.0.0.2` から）：どちらの形でも、ログインして `GET /api/builds` を読め、ログイン履歴のクライアントは、Nginxから見た相手（127.0.0.2）になり、クライアントが書いた `X-Forwarded-For`（203.0.113.9）は使われない。`Host` の行を書かないと、421になる。オリジンが `https` のとき、Cookieの名前が `__Host-session`（`__Host-` で始まる名前）になる。Cookieは、いつも `Secure` つきで、curl は、`Secure` つきのCookieを、httpの接続では、ホストが `localhost`・`127.0.0.1` のときだけ保存する（下記）。ここでは、ホストが `Host` ヘッダーの `admin.example.com` になるので、保存されない。そのため、ログインの応答のヘッダー（`curl -D FILE`）の `Set-Cookie` の値を、`Cookie` ヘッダーで付け、`Origin` と `Host` のヘッダーも、`https://admin.example.com` に合わせて付ける。
- リバースプロキシを置かず、SSHのポート転送で開く（9.6の経路の2つ目）：サーバーを、既定の `127.0.0.1:8081` で動かし、手元で `ssh -N -L 8081:127.0.0.1:8081 USER@HOST` を動かして、起動の行のオリジン（`http://127.0.0.1:8081`）を開く。`-L [bind_address:]port:host:hostport` と `-N`（コマンドを実行しない）は、OpenSSH 10.5p1 の ssh(1) で、手元の側は、既定では loopback だけで待ち受けること（`GatewayPorts`）は、ssh_config(5) で、2026-11-14に確かめた。ブラウザーの `Host` と `Origin` が、オリジンと同じでないと、421か403になるので、手元のポートを変えるときや、`localhost` と書いて開くときは、`-origin` に、ブラウザーで開くもの（`http://localhost:9000` など）を書く。管理画面は、まだ無いので、開けるのは、APIだけ（9.6）。
- loopbackでないアドレスでは、待ち受けない（`-addr` が断る）。平文のhttpを、ネットワークに出さないため。
- 手元で確かめる：TCPなら `curl -H 'Origin: ORIGIN' http://127.0.0.1:8081/api/…`、ソケットなら `curl --unix-socket SOCKET -H 'Origin: ORIGIN' http://localhost/api/…`（`ORIGIN` は、起動の行のオリジン。`-origin` に、そろえる前の形で書いたときも、起動の行のものを使う。ホストは、`ORIGIN` のホストに合わせる）。curl 8.22.0 は、`Secure` つきのCookieを、`http://127.0.0.1:<ポート>` と、ソケット越しの `http://localhost` では、`-c`・`-b` のファイルに保存して送る（2026-11-14に確かめた）。ブラウザーが、`http://127.0.0.1:<ポート>` で `Secure` つきのCookieを扱うかは、確かめていない（9.6の「Cookie」）。systemdのユニットと、socket activationの例は、まだ無い（6.12の262）。

**例**

見本のサイト（6.11。`internal/build/testdata/site/`）の写しに、見本のテーマ（`internal/theme/testdata/example/`）を `theme/` として置いたディレクトリで（保存先に触れない例。2026-11-16に動かし直して、出力が前と同じことを確かめた）：

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

保存先を使う例。リポジトリの外のディレクトリに、上の見本のサイトの写し（見本のテーマを `theme/` として置いたもの）を `site/` として置き、設定ファイル `whiteroom.yaml` を書く。ソケットのディレクトリ（`/run/user/1000/whiteroom`）は、あらかじめ作っておく（`mkdir -m 0700 /run/user/1000/whiteroom`。デーモンは作らない）。2026-11-16に、1つのUnixユーザーで動かした出力で、リリースの名前・ID・時刻・鎖の頭のハッシュは `…` と省いた（`curl` は、`-w '\n'` で、本文の後に改行を足した）。待ち受けのポートは、`-addr 127.0.0.1:0`（OSが選ぶ）で動かしたので、実行のたびに変わる（何も書かなければ、`127.0.0.1:8081` と `127.0.0.1:8080`。起動の行は、そのアドレスになる）。`whiteroom-db serve` と `whiteroom serve admin` は、止めるまで動くので、それぞれ別の端末で動かし、ほかの行は、3つ目の端末で動かす。パスワードは、権限0600のファイル `password.txt` に書き、標準入力で渡した（端末では、`Password: ` と `Password (again): ` の2行を標準エラーに示して、エコーなしで、2回打つ。上記の「パスワードの入力」。この実行は、端末ではないので、促しは出ない）：

```text
$ cat whiteroom.yaml
data: data
db: /run/user/1000/whiteroom
site: site
out: out
cache: cache
tmp: tmp
$ whiteroom-db init -config whiteroom.yaml
created the data directory data
$ whiteroom-db serve -config whiteroom.yaml
serving the data directory data on the sockets in /run/user/1000/whiteroom (writer: user 1000)
$ whiteroom init -config whiteroom.yaml -login admin -name 'Site Admin' -email admin@example.com site < password.txt
site/redirects/rdr_01JC0000000000000000000006.json:5:3: /to: entry "ent_01JC0000000000000000000003" is not published; the redirect is left out
initialized the store of /run/user/1000/whiteroom
created the administrator admin (usr_…)
imported the site site
$ whiteroom serve admin -config whiteroom.yaml -addr 127.0.0.1:0
serving admin of the store of /run/user/1000/whiteroom at http://127.0.0.1:44159 (listening on 127.0.0.1:44159)
```

- 取り込みの警告は、標準エラーに出る。`whiteroom init` は、`whiteroom-db init` が作ったデータのディレクトリを、動いているデーモンを通して使う：先にデーモンが動いていること。
- デーモンを止める（Ctrl-C）と、デーモンの標準エラーに、鎖の頭の行が出る（起動のときと、1分ごとにも、変わったものだけ出る）：

```text
audit head activity 4 sha256:…
audit head login 1 sha256:…
```

別の端末で、管理のAPIを `curl` で呼ぶ。ログインの本文 `login.json`（`{"login": "admin", "password": "…"}`）は、権限0600のファイルに書く（上記の「秘密」）。`Origin` は、起動の行のオリジン：

```text
$ curl -sS -c cookies.txt -H 'Origin: http://127.0.0.1:44159' -H 'Content-Type: application/json' --data-binary @login.json -w '\n' http://127.0.0.1:44159/api/session
{"account":{"id":"usr_…","login":"admin","name":"Site Admin","email":"admin@example.com","role":"admin"},"session":{"authenticatedAt":"…","expiresAt":"…","idleTimeout":3600,"factors":["password"]}}
$ curl -sS -b cookies.txt -H 'Origin: http://127.0.0.1:44159' -w '\n' http://127.0.0.1:44159/api/builds
{"pending":false,"paused":false,"last":{"startedAt":"…","endedAt":"…","requests":[{"at":"…","reason":"start"}],"release":"…","changed":true,"pages":50,"files":69,"warnings":[…],"failed":false},"schedules":[]}
$ whiteroom serve public -addr 127.0.0.1:0 out
serving out at http://127.0.0.1:46729/
$ curl -sS http://127.0.0.1:46729/ | grep -o '<title>[^<]*</title>'
<title>株式会社〇〇</title>
```

- `warnings` は、起動のビルドの警告（上の `whiteroom build` の例と同じ5件）。ビルドの結果は、標準出力に出ず、`GET /api/builds` で読む（9.7）。

サーバーが動いている間に、写しを取り、別のデーモンで確かめる。`heads1.txt` は、`backup` の標準出力を、そのまま書いたもの（頭ではない最初の行は、`-heads` が読み飛ばす）。写しの上のデーモンは、別のソケットのディレクトリ（`/run/user/1000/whiteroom-copy`。あらかじめ作る）で動かし、同じ形の設定ファイル `copy.yaml` で、デーモンにも `doctor` にも渡す：

```text
$ whiteroom backup -config whiteroom.yaml backup1 | tee heads1.txt
backed up the store of /run/user/1000/whiteroom to backup1
audit head activity 4 sha256:…
audit head login 1 sha256:…
$ whiteroom doctor -config whiteroom.yaml
no problems found in /run/user/1000/whiteroom
$ cat copy.yaml
data: backup1
db: /run/user/1000/whiteroom-copy
site: site
$ whiteroom-db serve -config copy.yaml
serving the data directory backup1 on the sockets in /run/user/1000/whiteroom-copy (writer: user 1000)
$ whiteroom doctor -config copy.yaml -heads heads1.txt
no problems found in /run/user/1000/whiteroom-copy
```

- 写しの上のデーモンを止めた後に `whiteroom doctor -config copy.yaml` を動かすと、デーモンに2秒届かずに、次のように示して、1で終わる：

```text
open: /run/user/1000/whiteroom-copy: cannot reach the daemon: dial unix /run/user/1000/whiteroom-copy/reader.sock: connect: no such file or directory
1 problem found in /run/user/1000/whiteroom-copy
```

写しを、ステージングのデータのディレクトリとして置き、もう1つのデーモンで開いて、ステージングのURLでビルドして、戻す（`staging.yaml` の `url` が、サイトのURLを上書きする。上記の「環境ごとの `url`」）：

```text
$ cp -a backup1 data-staging
$ cat staging.yaml
data: data-staging
db: /run/user/1000/whiteroom-staging
site: site
out: out-staging
cache: cache-staging
url: https://staging.example.com
$ whiteroom-db serve -config staging.yaml
serving the data directory data-staging on the sockets in /run/user/1000/whiteroom-staging (writer: user 1000)
$ whiteroom doctor -config staging.yaml
no problems found in /run/user/1000/whiteroom-staging
$ whiteroom build -config staging.yaml
published release … in out-staging: 50 pages, 69 files, 5 warnings
$ grep -o '<link rel="canonical"[^>]*>' out/current/public/index.html out-staging/current/public/index.html
out/current/public/index.html:<link rel="canonical" href="https://example.com/">
out-staging/current/public/index.html:<link rel="canonical" href="https://staging.example.com/">
$ whiteroom build -config staging.yaml -url https://staging2.example.com
published release … in out-staging: 50 pages, 69 files, 5 warnings
$ whiteroom rollback out-staging
rolled back to release … in out-staging (was …)
whiteroom rollback: warning: a running whiteroom serve admin replaces this release in its next build; to keep it, roll back through the admin API, POST /api/releases/{name}/rollback, which stops the builds until they are resumed
```

- ビルドの警告（5件）は、標準エラーに出る（上の例では省いた。保存先からのビルドでは、テーマの行は `site/theme: no translation …` と、`SITE` を前に付けた形）。保存先のメディアの原本が1つ無くなった写し（`data-d`）の上のデーモンでは、`doctor` は、次のように示して、1で終わる（ハッシュは `…` と省いた）：

```text
$ whiteroom-db serve -data data-d -db /run/user/1000/whiteroom-copy
serving the data directory data-d on the sockets in /run/user/1000/whiteroom-copy (writer: user 1000)
$ whiteroom doctor -db /run/user/1000/whiteroom-copy site
original: sha256:026f…: files/02/026f…: not found (of the media med_01JBG3N6V3X6Z9C2E5G8J1K4M7)
1 problem found in /run/user/1000/whiteroom-copy
```

デーモンが止まっている間の、管理用のサーバー（サーバーは、止めずに、デーモンを止めて確かめた。`curl` のヘッダーは、状態の行のほかは省いた）。デーモンが戻れば、同じCookieで、続けて動く：

```text
$ curl -sS -b cookies.txt -H 'Origin: http://127.0.0.1:44159' -i http://127.0.0.1:44159/api/builds
HTTP/1.1 503 Service Unavailable
…
{"type":"about:blank","title":"Service Unavailable","status":503,"detail":"the server could not answer in time; try again later","code":"busy"}
```

```text
2026/11/16 21:11:44 the daemon of the store cannot be reached: /run/user/1000/whiteroom: cannot reach the daemon: dial unix /run/user/1000/whiteroom/reader.sock: connect: no such file or directory
```

（上は、そのときの、管理用のサーバーの標準エラー。1分に1行まで。どの要求も、デーモンを2秒待ってから、503で答える。）

ソケットで待ち受ける形（`-socket` と `-origin`。TCPのポートを使わない）。上の `whiteroom.yaml` と同じデーモンに繋ぎ、`socket.yaml` の `socket` は、管理用のユーザーだけが書けるディレクトリ（ここでは `/run/user/1000/whiteroom-admin/`。あらかじめ作る）に置いた。書き手の接続は1つなので、先のサーバーは、止めてある：

```text
$ cat socket.yaml
data: data
db: /run/user/1000/whiteroom
site: site
out: out-s
cache: cache-s
tmp: tmp-s
socket: /run/user/1000/whiteroom-admin/admin.sock
origin: http://localhost
$ whiteroom serve admin -config socket.yaml
serving admin of the store of /run/user/1000/whiteroom at http://localhost (listening on socket /run/user/1000/whiteroom-admin/admin.sock)
$ curl -sS --unix-socket /run/user/1000/whiteroom-admin/admin.sock -c cookies-s.txt -H 'Origin: http://localhost' -H 'Content-Type: application/json' --data-binary @login.json -w '\n' http://localhost/api/session
{"account":{"id":"usr_…","login":"admin","name":"Site Admin","email":"admin@example.com","role":"admin"},"session":{"authenticatedAt":"…","expiresAt":"…","idleTimeout":3600,"factors":["password"]}}
$ curl -sS --unix-socket /run/user/1000/whiteroom-admin/admin.sock -b cookies-s.txt -H 'Origin: http://localhost' -w '\n' http://localhost/api/builds
{"pending":false,"paused":false,"last":{…,"release":"…","changed":true,"pages":50,"files":69,…},"schedules":[]}
```

- 止めると（Ctrl-C）、ソケットのファイルは消える。ソケットに繋いだ接続には、`X-Forwarded-For` が無いので、ログイン履歴のクライアントは、`::`（区別しないクライアント。9.6）になる。リバースプロキシが、`X-Forwarded-For` を付ける。

サーバーを止めた後に、管理者のパスワードを決め直す（デーモンは、動かしたまま。パスワードは、端末では、エコーなしで、2回打つ。この実行は、権限0600のファイルから、標準入力で渡した）。サーバーが動いている間は、書き手のロックを取れず、動かない：

```text
$ whiteroom account reset -config whiteroom.yaml admin < password2.txt
whiteroom account reset: /run/user/1000/whiteroom: another process is connected as the writer
whiteroom account reset: stop whiteroom serve admin on this store, and run the command again; if another administrator can log in, the admin API does without that: POST /api/accounts/{id}/reset
$ whiteroom account reset -config whiteroom.yaml admin < password2.txt
reset the password of admin (usr_…)
```

（上の1つ目が、サーバーが動いているときで、2つ目が、止めた後。）

写しから戻す（上の「戻す手順」）。管理用のサーバーと、デーモンを止めた後（デーモンの標準エラーの頭の行は、`daemon.err` に残した）：

```text
$ mv data data.old
$ cp -a backup1 data
$ whiteroom-db serve -config whiteroom.yaml
serving the data directory data on the sockets in /run/user/1000/whiteroom (writer: user 1000)
$ whiteroom doctor -config whiteroom.yaml -heads heads1.txt
no problems found in /run/user/1000/whiteroom
$ whiteroom doctor -config whiteroom.yaml -heads daemon.err
audit: audit log "activity": the head kept outside the store is record 7, but the log ends with record 4 (event 9)
1 problem found in /run/user/1000/whiteroom
```

- 2つ目の `doctor` には、写しの後に、デーモンが書き出した頭（`daemon.err`）を渡したので、問題になる（正しい動き：戻した鎖は、その頭の記録を持たない）。戻した後の管理用のサーバーでは、写しの後に決めたパスワードでは、ログインできず（401）、写しのときのパスワードで、ログインできる。

デーモンの動かし方の誤りと、保守（`whiteroom-db`）：

```text
$ whiteroom-db init -config whiteroom.yaml
whiteroom-db init: data: not empty; a new data directory needs a missing or empty directory
$ whiteroom-db serve -config whiteroom.yaml
whiteroom-db serve: data/logdb: another process holds the writer lock
$ whiteroom-db gc -config whiteroom.yaml
whiteroom-db gc: data: the data directory is in use; stop whiteroom-db serve on it first
$ whiteroom-db compact -data data.old
compacted data.old
$ whiteroom-db gc -data data.old
collected garbage in data.old: 0 segments, 0 object files, 0 temporary files
```

- 上の `init` は、データのディレクトリがあるとき。`serve` は、同じデータのディレクトリを、別のデーモンが開いているとき（約2秒待って、失敗する）。`gc` は、デーモンが動いているとき（約2秒待って、何も変えずに失敗する）。`compact` と `gc` は、デーモンを止めて動かし、ここでは、前のデータのディレクトリ `data.old` に動かした。

デーモンに届かないとき（2秒待って、1で終わる）と、保存先が新しくないとき：

```text
$ whiteroom serve admin -config whiteroom.yaml
whiteroom serve admin: /run/user/1000/whiteroom: cannot reach the daemon: dial unix /run/user/1000/whiteroom/writer.sock: connect: no such file or directory
$ whiteroom init -config whiteroom.yaml -login admin -name A -email a@example.com < password2.txt
whiteroom init: the store of /run/user/1000/whiteroom has accounts: init is for a new store
```

（2つ目は、デーモンが動いているとき。）

デーモンが、接続を受けて、30秒、何も答えないとき（止められたプロセスなど。30〜31秒の後、1で終わる。2026-11-18に、答えない偽のデーモンで動かした行で、引数とパスだけ、例のものに替えた）：

```text
$ whiteroom serve admin -config whiteroom.yaml
whiteroom serve admin: /run/user/1000/whiteroom: cannot reach the daemon: the daemon has sent nothing for 30s
$ whiteroom doctor -config whiteroom.yaml
open: /run/user/1000/whiteroom: cannot reach the daemon: the daemon has sent nothing for 30s
1 problem found in /run/user/1000/whiteroom
```

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

- リリースの問題（上記の3）と、ファイルの問題（公開ディレクトリを開けない、外を指すシンボリックリンク、読めないファイルなど。404のページのファイルのものも）は、標準エラーに、日時・メソッド・リクエストのターゲット（`"` で囲む。制御文字や双方向の書式文字を含むことがあるため）・エラーを、1行に記録する（`2026/11/03 01:02:03 GET "/": no release is published in /srv/site`）。Goの `net/http` のエラーも、日時を付けて、同じく書く。書けなくても（読む側の終わったパイプでも）、サーバーは止まらない（9.4の「終了コード」）。
- 404のページのファイルが読めないとき（外を指すリンクなど）は、記録して、ただの404を返す。
- ほかの応答（200・206・301・302・304・404・405など）は記録しない。アクセスログは、まだ無い（6.12の122）。
