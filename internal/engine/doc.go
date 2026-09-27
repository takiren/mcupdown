// Package engine はコンテナの操作を担う。
//
// ライフサイクルは quadlet の .container ファイルと systemd --user（D-Bus）で扱い、
// pull / inspect / logs / exec は podman の Docker 互換 API で扱う。
// デーモンはこのパッケージのインターフェースだけに依存し、ユニットテストではフェイクを使う。
package engine
