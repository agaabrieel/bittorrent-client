package client

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"time"
)

type TrackerClient interface {
	Announce(ctx context.Context, req AnnounceRequest) (AnnounceResponse, error)
}

type HTTPTrackerClient struct {
	Client *http.Client
}

type UDPTrackerClient struct {
	Dialer *net.Dialer
}

type Tracker struct {
	url      url.URL
	interval time.Duration
	tier     uint8
}

type AnnounceRequest struct {
	InfoHash   [20]byte
	PeerID     [20]byte
	Port       uint16
	Uploaded   uint64
	Downloaded uint64
	Left       uint64
	Event      string // "started", "stopped", "completed"
}

type AnnounceResponse struct {
	Interval int    // Seconds to wait between announces
	Peers    []Peer // List of peers
	// ... other fields
}
