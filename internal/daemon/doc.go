// Package daemon は mcctld の中核のロジックを担う。
//
// Store（求める状態）と engine（実際の状態）をつなぎ、create / up / down / rm /
// status の各操作、サーバーごとのロック、最後の操作の記録、起動時同期を行う。
// HTTP のハンドラはこのパッケージを呼ぶだけにする。
package daemon
