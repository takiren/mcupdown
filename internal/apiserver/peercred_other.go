//go:build !linux

package apiserver

import "net"

// 本番は Linux だけ。macOS などでは開発用として認可を行わない。
const peerCredSupported = false

func peerUID(net.Conn) (uint32, bool) { return 0, false }
