# paritydesk

English: [README.md](README.md)

[![ci](https://github.com/kazupc/paritydesk/actions/workflows/ci.yml/badge.svg)](https://github.com/kazupc/paritydesk/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

1 台の Mac で Claude デスクトップアプリを複数のアカウントで使い、コマンド 1 つで切り替えられるようにします。アカウント以外のもの（Code のセッション、環境、サイドバー、ピン）は、どのアカウントでも同じになります。

## できること

- **1 回で切り替え。** ターミナルで `paritydesk switch`、または Code のセッションで `/paritydesk:switch` を実行します。Claude が終了し、もう一方のアカウントで起動し直して、作業していたセッションを開き直します。
- **Claude Code の作業環境がついてきます。** セッション、タスク、環境、サイドバー、ピン、worktree、スクラッチワークスペース、ウィンドウの大きさを、どちらの向きにも引き継ぎます。
- **新しい変更が勝ちます。** 両方のアカウントで同じものを変えていたら、新しい方を残します。古い方はバックアップに残ります。
- **どのアカウントもサインインしたままです。** アカウントごとにデータフォルダとサインインを分けたまま、paritydesk がフォルダを入れ替えます。

**状態: リリース前の試験段階です。** まだバージョンはありません。土台の部分（Claude の終了、フォルダの入れ替え、セッションの開き直し）は実際の Mac で確かめましたが、切り替えの全体と新しい機能は、作り物のデータでしかテストしていません。試すときは Time Machine でバックアップを取ってください。非公式のツールで、Anthropic とは関係ありません。

## インストール

macOS と Go 1.26.1 以降が必要です（`brew install go`）。Homebrew の tap と配布ファイルは、最初のリリースで用意します。

```
go install github.com/kazupc/paritydesk/cmd/paritydesk@latest
```

バイナリは `$(go env GOPATH)/bin` に入ります。必要なら `PATH` に足してください。詳しくは [docs/install.md](docs/install.md)（英語）にあります。

**Claude Code のプラグイン**（任意）: `/paritydesk:switch`、`/paritydesk:reconcile`、`/paritydesk:artifacts` が使えるようになります。Code のセッションで次を実行します。

```
/plugin marketplace add kazupc/paritydesk
/plugin install paritydesk@paritydesk
```

## はじめかた

Claude に 1 つ目のアカウントでサインインした状態で、ターミナルで実行します。

```
paritydesk init --alias work        # いまサインインしているアカウントに名前を付ける
paritydesk profile add personal     # Claude が空の状態で開くので、2 つ目のアカウントでサインイン
paritydesk recover                  # 2 つ目のアカウントの追加を仕上げる
paritydesk plan                     # 次の切り替えの計画を見る
paritydesk switch                   # 切り替える。もう一度実行すると元に戻る
```

`init` は、そのアカウントが勤め先や取引先の管理下にあるかを聞きます。macOS がターミナルから Claude を操作する許可を 1 回だけ求めるので、許可してください。2 つ目のアカウントがすでに `Claude-personal` のようなフォルダにある場合は、`profile add` の代わりに `paritydesk profile adopt personal Claude-personal` を使います。最初の切り替えは両方のアカウントの中身をまとめるだけで、何も消しません。

Code のセッションでは、`/paritydesk:switch` が計画を見せて、**Switch** と **Cancel** のボタンで確認します。設定の確認は `paritydesk doctor` で、すべてのコマンドは [docs/commands.md](docs/commands.md)（英語）にあります。

## 引き継ぐもの

| 切り替えのたびに引き継ぐ | 切り替えの後に Code のセッションで反映（`/paritydesk:reconcile`） | 引き継がない |
|---|---|---|
| Code のセッション、バックログと予約タスク | どのセッションにピンを付けているか | サインインと認証情報 |
| スクラッチワークスペース | サイドバーの並べ替えとグループ分け | claude.ai のチャットと利用上限 |
| 環境（秘密の値は管理下にないアカウントどうしでだけ） | | セッションの過去の思考 |
| サイドバーのグループ、セクション、ピンの順番、折りたたみ | | 権限の許可 |
| worktree の登録、ウィンドウの大きさ、一部のアプリ設定 | | Claude in Chrome（アカウントごとに Chrome のプロファイルを分ける） |

MCP サーバーは `sync.mcp_servers` を有効にすると引き継ぎます。アーティファクト（ベータ）は `/paritydesk:artifacts` でこの Mac に保存します。設定は [docs/configuration.md](docs/configuration.md)（英語）にあります。

## 安全のために

- **サインインには手を付けません。** どのアカウントもサインインしたままで、トークン、Cookie、キーチェーンの項目も読みません（[PRIVACY.md](PRIVACY.md)）。
- **通信しません。** 利用状況の送信も更新の確認もありません。バイナリにネットワークのコードが入っていないことをテストで確かめています。
- **Claude を行儀よく終了します。** 「終了」を選んだときと同じやり方で終了し、作業中のセッションがあれば待ちます。Claude が終了してよいかを聞いたときは、あなたが決めます。
- **書き込む前に必ずバックアップを取ります。** 途中で止まった切り替えは `paritydesk recover` で仕上げるか元に戻せます。`paritydesk rollback` で前の状態に戻せます。

## 知っておくこと

- 切り替えた後にセッションを続けると、**「Claude can't use thinking from another organization」** と表示されることがあります。害はありません。過去の思考は書いたときのアカウントに結び付いているので、Claude は次の返事でセッションを読み直します。そのため返事が少し遅く、トークンも多めに使います。
- **Claude in Chrome** は、Claude のアカウントごとに Chrome のプロファイルを分け、それぞれをそのアカウントでサインインしておく必要があります。
- **Claude の中でアカウントを切り替えないでください。** 切り替えは paritydesk で行います。

ほかの注意は [If something goes wrong](docs/if-something-goes-wrong.md)（英語）にあります。

## 文書

- [Install](docs/install.md) · [Commands](docs/commands.md) · [Configuration](docs/configuration.md)
- [Integrations: SwiftBar, Raycast, shell](docs/integrations.md)
- [If something goes wrong](docs/if-something-goes-wrong.md)
- [How it works](docs/how-it-works.md) · [Architecture](docs/architecture.md)
- [Moving from an earlier folder-swap script](docs/migration-from-legacy.md)
- [Privacy](PRIVACY.md) · [Security](SECURITY.md) · [Changelog](CHANGELOG.md) · [Contributing](CONTRIBUTING.md)

文書は英語です。

## ライセンス

MIT ライセンスです（[LICENSE](LICENSE)）。paritydesk は独立したプロジェクトで、Anthropic とは提携しておらず、承認や支援も受けていません。「Claude」は Anthropic の商標です。詳しくは [DISCLAIMER.md](DISCLAIMER.md) を見てください。
