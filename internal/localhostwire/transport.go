package localhostwire

import (
	"context"
	"net/http"
)

// LocalHTTP serves the shared controller handlers inside the elected local runtime.
type LocalHTTP struct {
	API, Dashboard http.Handler
	// Shell serves the dashboard's static pages to a TCP browser that holds no session.
	Shell http.Handler
}

// LocalPeerIdentity comes from the transport's kernel credentials, never request input.
type LocalPeerIdentity struct {
	GOOS string
	UID  *uint32
	SID  string
}

// LocalDoctorFinding makes incompatible stores, version skew and removed unit verbs actionable.
type LocalDoctorFinding struct {
	Code, Unit, Message, Hint string
}

type localPeerKey struct{}

// LocalPeer reads the identity verified by the local transport or dashboard login.
func LocalPeer(ctx context.Context) (LocalPeerIdentity, bool) {
	peer, ok := ctx.Value(localPeerKey{}).(LocalPeerIdentity)
	return peer.Clone(), ok
}

// WithLocalPeer attaches a verified transport identity to a request.
func WithLocalPeer(ctx context.Context, peer LocalPeerIdentity) context.Context {
	return context.WithValue(ctx, localPeerKey{}, peer.Clone())
}

// Clone keeps a request from mutating another request's verified UID.
func (peer LocalPeerIdentity) Clone() LocalPeerIdentity {
	if peer.UID != nil {
		uid := *peer.UID
		peer.UID = &uid
	}
	return peer
}
