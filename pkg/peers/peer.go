package peer

import "net"

type PeerManager struct {
}

type Peer struct {
	Id   [20]byte
	Ip   net.IP
	Port uint16
}
