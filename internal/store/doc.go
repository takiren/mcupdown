// Package store はサーバーの「求める状態」を保存する。
//
// 保存するのはユーザーが指定した定義（ポート、バージョンなど）と desiredState だけで、
// 実際の状態（動いているか、health など）は持たない。実際の状態は常に systemd と
// podman に問い合わせる。quadlet の .container ファイルはここから生成する生成物。
//
// API の型（internal/api）やコンテナ操作（internal/engine）には依存しない。
// それらとの変換は daemon パッケージで行う。
package store
