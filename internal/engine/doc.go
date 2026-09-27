// Package engine はコンテナエンジンの操作を担う。
//
// デーモンは Engine インターフェースだけに依存する。実装は Docker Engine API を
// 使うもの（docker と、podman の Docker 互換ソケットの両方に対応する）と、
// ユニットテスト用のフェイクを用意する。
package engine
