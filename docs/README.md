# 自作CMS 構想書

> **ステータス:** 議論中（作業ドラフト）  
> **最終更新:** 2026-10-31  
> **目的:** これまでの議論で決まったことと、提案・未決定事項を分離して記録する。  
> **注記:** この文書の「決定済み」は製品仕様の凍結を意味しない。以後の議論で変更可能。

## 0. 文書の読み方

各事項を次の区分で扱う。

| 区分 | 意味 |
| --- | --- |
| **決定済み** | 会話で採用・希望が明示された方針。変更する際は明示的に更新する。 |
| **方向性として合意** | 目標や段階的な進め方は共有しているが、具体的な仕様・実現方法は未確定。 |
| **提案段階** | 実装候補として挙げたが、採用は決まっていない。 |
| **未決定** | 比較・検討が必要な論点。 |

---

## 目次

構想書は、章と、大きい節ごとにファイルを分けている。参照は節番号で書き（`6.5.2` など）、ファイルを分けても節番号は変えない。

| 章・節 | ファイル |
| --- | --- |
| 1. プロジェクトの目的 | [01-purpose.md](01-purpose.md) |
| 2. 全体アーキテクチャ（概念） | [02-architecture.md](02-architecture.md) |
| 3. ストレージ設計 | [03-storage.md](03-storage.md) |
| 4. コンテンツ編集・ビルド・公開 | [04-publishing.md](04-publishing.md) |
| 5. テーマと編集体験（5.1〜5.3） | [05-theme.md](05-theme.md) |
| 5.4 テーマの形式（案） | [05.04-theme-format.md](05.04-theme-format.md) |
| 6. コンテンツモデル（6.1〜6.3） | [06-content-model.md](06-content-model.md) |
| 6.4 エントリー | [06.04-entries.md](06.04-entries.md) |
| 6.5 本文（ブロック） | [06.05-blocks.md](06.05-blocks.md) |
| 6.6 本文以外のエンティティ | [06.06-entities.md](06.06-entities.md) |
| 6.7 検証のルール（Validator） | [06.07-validation.md](06.07-validation.md) |
| 6.8 描画の取り決め（Renderer） | [06.08-rendering.md](06.08-rendering.md) |
| 6.9 バージョン管理 | [06.09-versioning.md](06.09-versioning.md) |
| 6.10 WordPressとの対応 | [06.10-wordpress.md](06.10-wordpress.md) |
| 6.11 例 | [06.11-examples.md](06.11-examples.md) |
| 6.12 未決定 | [06.12-open-questions.md](06.12-open-questions.md) |
| 7. 管理画面・認証・監査 | [07-admin.md](07-admin.md) |
| 8. お問い合わせ機能 | [08-contact.md](08-contact.md) |
| 9. Linuxでの配布・実行 | [09-distribution.md](09-distribution.md) |
| 10. セキュリティ設計の基本原則 | [10-security.md](10-security.md) |
| 11. 初期実装の範囲（提案） | [11-initial-scope.md](11-initial-scope.md) |
| 12. 次回以降の主要論点（未決定事項） | [12-next-topics.md](12-next-topics.md) |
| 13. 決定履歴 | [13-decision-history.md](13-decision-history.md) |
| 14. 将来の更新ルール（提案） | [14-update-rules.md](14-update-rules.md) |
