// Package sfu is a selective forwarding unit on Pion: every participant sends
// their media to the server once, and the server forwards each track to
// everyone else in the room.
//
// It is transport-neutral. The server never decides how a participant hears
// from it; it calls a Signaler, and the caller (the REF resource) delivers the
// SDP and ICE over the realtime hub. Participants send their first offer with
// Join and answer the server's later offers with Answer.
//
// A room lives in one process: media cannot be split across nodes without
// cascading SFUs. In a cluster, route every request for a room to one node
// (hash the room id at the load balancer); the call state itself is in SQL and
// is shared by all nodes.
package sfu

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
)

// Errors.
var (
	ErrRoomFull = errors.New("sfu: the room is full")
	ErrNoPeer   = errors.New("sfu: no such participant")
	ErrClosed   = errors.New("sfu: server closed")
)

// Signaler delivers the server's signaling to a participant.
type Signaler interface {
	// Offer sends a server-initiated offer (the room's tracks changed). The
	// participant must reply through Server.Answer.
	Offer(user, room string, offer webrtc.SessionDescription) error
	// Candidate sends a server ICE candidate.
	Candidate(user, room string, c webrtc.ICECandidateInit) error
}

// Config tunes the server.
type Config struct {
	ICEServers []webrtc.ICEServer
	// UDPPortMin and UDPPortMax confine media to a range, so a firewall can
	// allow it. Zero leaves the choice to the OS.
	UDPPortMin, UDPPortMax uint16
	// PublicIPs are advertised as host candidates (NAT 1:1), for a server
	// behind a static NAT. Empty for a server with a public address.
	PublicIPs []string
	// MaxPeers is the most participants in one room. Default 50.
	MaxPeers int
	// MaxRooms bounds rooms on this node. Default 1000.
	MaxRooms int
	// PLIInterval is how often a keyframe is requested from each video
	// publisher, so late joiners see a picture. Default 3s.
	PLIInterval time.Duration
	// Loopback includes loopback candidates; for tests only.
	Loopback bool
}

// Server is the SFU.
type Server struct {
	cfg Config
	api *webrtc.API
	sig Signaler

	mu     sync.Mutex
	rooms  map[string]*room
	closed bool
}

// New returns an SFU that signals through sig.
func New(cfg Config, sig Signaler) (*Server, error) {
	if cfg.MaxPeers <= 0 {
		cfg.MaxPeers = 50
	}
	if cfg.MaxRooms <= 0 {
		cfg.MaxRooms = 1000
	}
	if cfg.PLIInterval <= 0 {
		cfg.PLIInterval = 3 * time.Second
	}
	me := &webrtc.MediaEngine{}
	if err := me.RegisterDefaultCodecs(); err != nil {
		return nil, err
	}
	ir := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(me, ir); err != nil {
		return nil, err
	}
	se := webrtc.SettingEngine{}
	if cfg.UDPPortMax != 0 {
		if err := se.SetEphemeralUDPPortRange(cfg.UDPPortMin, cfg.UDPPortMax); err != nil {
			return nil, err
		}
	}
	if len(cfg.PublicIPs) > 0 {
		se.SetNAT1To1IPs(cfg.PublicIPs, webrtc.ICECandidateTypeHost)
	}
	if cfg.Loopback {
		se.SetIncludeLoopbackCandidate(true)
	}
	se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4, webrtc.NetworkTypeUDP6})
	api := webrtc.NewAPI(webrtc.WithMediaEngine(me), webrtc.WithInterceptorRegistry(ir), webrtc.WithSettingEngine(se))
	return &Server{cfg: cfg, api: api, sig: sig, rooms: map[string]*room{}}, nil
}

// published is one inbound track and the local track that forwards it.
type published struct {
	id    string
	owner string
	local *webrtc.TrackLocalStaticRTP
	kind  webrtc.RTPCodecType
}

type room struct {
	id    string
	mu    sync.Mutex
	peers map[string]*peer
	pubs  map[string]*published
}

type peer struct {
	user string
	room *room
	pc   *webrtc.PeerConnection
	srv  *Server

	nmu     sync.Mutex // serializes renegotiation
	senders map[string]*webrtc.RTPSender
	dirty   bool
	closing bool
	done    chan struct{}
	once    sync.Once
}

// Stats is a snapshot of the server.
type Stats struct {
	Rooms int `json:"rooms"`
	Peers int `json:"peers"`
}

// Stats counts rooms and participants.
func (s *Server) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Stats{Rooms: len(s.rooms)}
	for _, r := range s.rooms {
		r.mu.Lock()
		st.Peers += len(r.peers)
		r.mu.Unlock()
	}
	return st
}

// Peers lists the users in a room.
func (s *Server) Peers(roomID string) []string {
	s.mu.Lock()
	r := s.rooms[roomID]
	s.mu.Unlock()
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.peers))
	for u := range r.peers {
		out = append(out, u)
	}
	return out
}

// Close ends every room.
func (s *Server) Close() error {
	s.mu.Lock()
	s.closed = true
	var all []*peer
	for _, r := range s.rooms {
		r.mu.Lock()
		for _, p := range r.peers {
			all = append(all, p)
		}
		r.mu.Unlock()
	}
	s.mu.Unlock()
	for _, p := range all {
		p.close()
	}
	return nil
}

// Join adds user to the room with their first offer and returns the answer.
// Joining again replaces the earlier connection (a reload or a second tab).
func (s *Server) Join(ctx context.Context, roomID, user string, offer webrtc.SessionDescription) (webrtc.SessionDescription, error) {
	if offer.Type != webrtc.SDPTypeOffer {
		return webrtc.SessionDescription{}, errors.New("sfu: Join needs an offer")
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return webrtc.SessionDescription{}, ErrClosed
	}
	r := s.rooms[roomID]
	if r == nil {
		if len(s.rooms) >= s.cfg.MaxRooms {
			s.mu.Unlock()
			return webrtc.SessionDescription{}, ErrRoomFull
		}
		r = &room{id: roomID, peers: map[string]*peer{}, pubs: map[string]*published{}}
		s.rooms[roomID] = r
	}
	s.mu.Unlock()

	r.mu.Lock()
	old := r.peers[user]
	if old == nil && len(r.peers) >= s.cfg.MaxPeers {
		r.mu.Unlock()
		return webrtc.SessionDescription{}, ErrRoomFull
	}
	r.mu.Unlock()
	if old != nil {
		old.close()
	}

	pc, err := s.api.NewPeerConnection(webrtc.Configuration{ICEServers: s.cfg.ICEServers})
	if err != nil {
		return webrtc.SessionDescription{}, err
	}
	p := &peer{user: user, room: r, pc: pc, srv: s, senders: map[string]*webrtc.RTPSender{}, done: make(chan struct{})}

	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c != nil {
			_ = s.sig.Candidate(user, roomID, c.ToJSON())
		}
	})
	pc.OnConnectionStateChange(func(st webrtc.PeerConnectionState) {
		if st == webrtc.PeerConnectionStateFailed || st == webrtc.PeerConnectionStateClosed {
			p.close()
		}
	})
	pc.OnTrack(func(remote *webrtc.TrackRemote, _ *webrtc.RTPReceiver) { p.onTrack(remote) })

	if err := pc.SetRemoteDescription(offer); err != nil {
		_ = pc.Close()
		return webrtc.SessionDescription{}, fmt.Errorf("sfu: offer: %w", err)
	}
	answer, err := pc.CreateAnswer(nil)
	if err == nil {
		err = pc.SetLocalDescription(answer) // candidates trickle through the Signaler
	}
	if err != nil {
		_ = pc.Close()
		return webrtc.SessionDescription{}, fmt.Errorf("sfu: answer: %w", err)
	}

	r.mu.Lock()
	r.peers[user] = p
	r.mu.Unlock()
	// Existing tracks reach the newcomer by a server offer, which the newcomer
	// answers like any other renegotiation.
	go p.renegotiate()
	return *pc.LocalDescription(), nil
}

func (s *Server) find(roomID, user string) *peer {
	s.mu.Lock()
	r := s.rooms[roomID]
	s.mu.Unlock()
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.peers[user]
}

// Answer applies a participant's answer to a server offer.
func (s *Server) Answer(roomID, user string, answer webrtc.SessionDescription) error {
	p := s.find(roomID, user)
	if p == nil {
		return ErrNoPeer
	}
	if answer.Type != webrtc.SDPTypeAnswer {
		return errors.New("sfu: Answer needs an answer")
	}
	p.nmu.Lock()
	err := p.pc.SetRemoteDescription(answer)
	again := p.dirty
	p.nmu.Unlock()
	if err != nil {
		return fmt.Errorf("sfu: answer: %w", err)
	}
	if again {
		go p.renegotiate()
	}
	return nil
}

// Candidate adds a participant's ICE candidate.
func (s *Server) Candidate(roomID, user string, c webrtc.ICECandidateInit) error {
	p := s.find(roomID, user)
	if p == nil {
		return ErrNoPeer
	}
	return p.pc.AddICECandidate(c)
}

// Leave removes a participant and the tracks they published.
func (s *Server) Leave(roomID, user string) {
	if p := s.find(roomID, user); p != nil {
		p.close()
	}
}

// onTrack forwards a track a participant publishes to the rest of the room.
func (p *peer) onTrack(remote *webrtc.TrackRemote) {
	id := p.user + ":" + remote.ID()
	local, err := webrtc.NewTrackLocalStaticRTP(remote.Codec().RTPCodecCapability, id, p.user)
	if err != nil {
		return
	}
	pub := &published{id: id, owner: p.user, local: local, kind: remote.Kind()}
	r := p.room
	r.mu.Lock()
	r.pubs[id] = pub
	others := r.othersLocked(p.user)
	r.mu.Unlock()
	for _, o := range others {
		go o.renegotiate()
	}

	if remote.Kind() == webrtc.RTPCodecTypeVideo {
		go func() { // ask for keyframes so a late joiner sees a picture
			t := time.NewTicker(p.srv.cfg.PLIInterval)
			defer t.Stop()
			for {
				select {
				case <-p.done:
					return
				case <-t.C:
					if p.pc.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: uint32(remote.SSRC())}}) != nil {
						return
					}
				}
			}
		}()
	}
	for { // forward until the publisher goes away
		pkt, _, err := remote.ReadRTP()
		if err != nil {
			break
		}
		// A write fails while nobody is subscribed yet; the packet is simply
		// dropped, and forwarding carries on for whoever joins next.
		_ = local.WriteRTP(pkt)
	}
	r.mu.Lock()
	delete(r.pubs, id)
	others = r.othersLocked(p.user)
	r.mu.Unlock()
	for _, o := range others {
		go o.renegotiate()
	}
}

func (r *room) othersLocked(except string) []*peer {
	out := make([]*peer, 0, len(r.peers))
	for u, p := range r.peers {
		if u != except {
			out = append(out, p)
		}
	}
	return out
}

// renegotiate brings p's outgoing tracks in line with the room and, if
// anything changed, sends the participant an offer.
func (p *peer) renegotiate() {
	offer, ok := p.prepareOffer()
	if !ok {
		return
	}
	// Sent without holding the lock: a Signaler may answer synchronously, and
	// Answer takes the same lock.
	if err := p.srv.sig.Offer(p.user, p.room.id, offer); err != nil {
		p.nmu.Lock()
		p.dirty = true
		p.nmu.Unlock()
	}
}

func (p *peer) prepareOffer() (webrtc.SessionDescription, bool) {
	p.nmu.Lock()
	defer p.nmu.Unlock()
	if p.closing {
		return webrtc.SessionDescription{}, false
	}
	if p.pc.SignalingState() != webrtc.SignalingStateStable {
		p.dirty = true // an offer is already out; revisit when it is answered
		return webrtc.SessionDescription{}, false
	}
	p.dirty = false

	r := p.room
	r.mu.Lock()
	want := make(map[string]*published, len(r.pubs))
	for id, pub := range r.pubs {
		if pub.owner != p.user {
			want[id] = pub
		}
	}
	r.mu.Unlock()

	changed := false
	for id, sender := range p.senders {
		if _, ok := want[id]; !ok {
			_ = p.pc.RemoveTrack(sender)
			delete(p.senders, id)
			changed = true
		}
	}
	for id, pub := range want {
		if _, ok := p.senders[id]; ok {
			continue
		}
		sender, err := p.pc.AddTrack(pub.local)
		if err != nil {
			continue
		}
		p.senders[id] = sender
		changed = true
		go func() { // RTCP must be read for the interceptors (NACK, reports) to work
			buf := make([]byte, 1500)
			for {
				if _, _, err := sender.Read(buf); err != nil {
					return
				}
			}
		}()
	}
	if !changed {
		return webrtc.SessionDescription{}, false
	}
	offer, err := p.pc.CreateOffer(nil)
	if err != nil {
		return webrtc.SessionDescription{}, false
	}
	if err := p.pc.SetLocalDescription(offer); err != nil {
		return webrtc.SessionDescription{}, false
	}
	return *p.pc.LocalDescription(), true
}

// close removes the peer and its tracks from the room.
func (p *peer) close() {
	p.once.Do(func() {
		p.nmu.Lock()
		p.closing = true
		p.nmu.Unlock()
		close(p.done)
		_ = p.pc.Close()

		r := p.room
		r.mu.Lock()
		if r.peers[p.user] == p {
			delete(r.peers, p.user)
		}
		for id, pub := range r.pubs {
			if pub.owner == p.user {
				delete(r.pubs, id)
			}
		}
		others := r.othersLocked(p.user)
		empty := len(r.peers) == 0
		r.mu.Unlock()
		for _, o := range others {
			go o.renegotiate()
		}
		if empty {
			p.srv.mu.Lock()
			r.mu.Lock()
			if len(r.peers) == 0 && p.srv.rooms[r.id] == r {
				delete(p.srv.rooms, r.id)
			}
			r.mu.Unlock()
			p.srv.mu.Unlock()
		}
	})
}
