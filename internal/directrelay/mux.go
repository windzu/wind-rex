package directrelay

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

const (
	frameOpen       byte = 1
	frameData       byte = 2
	frameCloseWrite byte = 3
	frameReset      byte = 4
	frameWindow     byte = 5

	frameHeaderLen = 5
	maxFrameData   = 32 << 10
	initialWindow  = 512 << 10
	windowRefresh  = 128 << 10

	muxPingInterval = 15 * time.Second
	muxPingTimeout  = 8 * time.Second
	muxWriteTimeout = 15 * time.Second
)

type muxAddr string

func (a muxAddr) Network() string { return "windrex-relay" }
func (a muxAddr) String() string  { return string(a) }

// Mux multiplexes independent byte streams over a single WebSocket connection.
// It implements net.Listener for the accepting side and Open for the dialing side.
type Mux struct {
	ws         *websocket.Conn
	localAddr  net.Addr
	remoteAddr net.Addr

	ctx    context.Context
	cancel context.CancelFunc

	nextID atomic.Uint32

	writeMu sync.Mutex

	mu      sync.Mutex
	streams map[uint32]*muxStream
	closed  bool

	acceptCh chan *muxStream
	done     chan struct{}
	once     sync.Once
}

// NewMux wraps an established WebSocket connection in a stream multiplexer.
func NewMux(ws *websocket.Conn, local, remote string) *Mux {
	ws.SetReadLimit(1 << 20)
	ctx, cancel := context.WithCancel(context.Background())
	m := &Mux{
		ws:         ws,
		localAddr:  muxAddr(local),
		remoteAddr: muxAddr(remote),
		ctx:        ctx,
		cancel:     cancel,
		streams:    map[uint32]*muxStream{},
		acceptCh:   make(chan *muxStream, 64),
		done:       make(chan struct{}),
	}
	go m.readLoop()
	go m.pingLoop()
	return m
}

// Done closes when the multiplexer shuts down.
func (m *Mux) Done() <-chan struct{} { return m.done }

// Addr satisfies net.Listener.
func (m *Mux) Addr() net.Addr { return m.localAddr }

// Accept waits for and returns the next incoming stream as a net.Conn.
func (m *Mux) Accept() (net.Conn, error) {
	select {
	case s := <-m.acceptCh:
		return s, nil
	case <-m.done:
		return nil, net.ErrClosed
	}
}

// Open opens a new multiplexed stream to the peer with zero extra round-trips.
func (m *Mux) Open(ctx context.Context) (net.Conn, error) {
	id := m.nextID.Add(1)
	s := newMuxStream(m, id)
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, net.ErrClosed
	}
	m.streams[id] = s
	m.mu.Unlock()

	if err := m.sendFrame(ctx, frameOpen, id, nil); err != nil {
		m.removeStream(id)
		return nil, err
	}
	return s, nil
}

// Close shuts down the multiplexer and all open streams.
func (m *Mux) Close() error {
	var err error
	m.once.Do(func() {
		m.mu.Lock()
		m.closed = true
		streams := make([]*muxStream, 0, len(m.streams))
		for _, s := range m.streams {
			streams = append(streams, s)
		}
		m.streams = map[uint32]*muxStream{}
		m.mu.Unlock()

		for _, s := range streams {
			s.abort()
		}
		m.cancel()
		err = m.ws.Close(websocket.StatusNormalClosure, "closed")
		close(m.done)
	})
	return err
}

func (m *Mux) removeStream(id uint32) {
	m.mu.Lock()
	delete(m.streams, id)
	m.mu.Unlock()
}

func (m *Mux) sendFrame(ctx context.Context, typ byte, id uint32, payload []byte) error {
	buf := make([]byte, frameHeaderLen+len(payload))
	buf[0] = typ
	binary.BigEndian.PutUint32(buf[1:5], id)
	copy(buf[5:], payload)

	m.writeMu.Lock()
	defer m.writeMu.Unlock()

	wctx, cancel := context.WithTimeout(ctx, muxWriteTimeout)
	defer cancel()
	err := m.ws.Write(wctx, websocket.MessageBinary, buf)
	if err != nil {
		go m.Close()
	}
	return err
}

func (m *Mux) readLoop() {
	defer m.Close()
	for {
		typ, data, err := m.ws.Read(m.ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageBinary || len(data) < frameHeaderLen {
			return
		}
		ft := data[0]
		id := binary.BigEndian.Uint32(data[1:5])
		payload := data[5:]

		switch ft {
		case frameOpen:
			s := newMuxStream(m, id)
			m.mu.Lock()
			if m.closed {
				m.mu.Unlock()
				return
			}
			m.streams[id] = s
			m.mu.Unlock()
			select {
			case m.acceptCh <- s:
			case <-m.done:
				return
			}
		case frameData:
			m.mu.Lock()
			s := m.streams[id]
			m.mu.Unlock()
			if s != nil {
				s.pushData(payload)
			}
		case frameCloseWrite:
			m.mu.Lock()
			s := m.streams[id]
			m.mu.Unlock()
			if s != nil {
				s.remoteCloseWrite()
			}
		case frameReset:
			m.mu.Lock()
			s := m.streams[id]
			delete(m.streams, id)
			m.mu.Unlock()
			if s != nil {
				s.abort()
			}
		case frameWindow:
			if len(payload) != 4 {
				return
			}
			delta := int64(binary.BigEndian.Uint32(payload))
			m.mu.Lock()
			s := m.streams[id]
			m.mu.Unlock()
			if s != nil {
				s.addSendWindow(delta)
			}
		default:
			return
		}
	}
}

func (m *Mux) pingLoop() {
	ticker := time.NewTicker(muxPingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-m.done:
			return
		case <-ticker.C:
			pctx, cancel := context.WithTimeout(m.ctx, muxPingTimeout)
			err := m.ws.Ping(pctx)
			cancel()
			if err != nil {
				m.Close()
				return
			}
		}
	}
}

type muxStream struct {
	mux *Mux
	id  uint32

	mu            sync.Mutex
	readCond      *sync.Cond
	writeCond     *sync.Cond
	recvBuf       []byte
	consumed      int
	remoteEOF     bool
	localClosed   bool
	writeClosed   bool
	aborted       bool
	sendWindow    int64
	readDeadline  time.Time
	writeDeadline time.Time
	readTimer     *time.Timer
	writeTimer    *time.Timer
}

func newMuxStream(m *Mux, id uint32) *muxStream {
	s := &muxStream{
		mux:        m,
		id:         id,
		sendWindow: initialWindow,
	}
	s.readCond = sync.NewCond(&s.mu)
	s.writeCond = sync.NewCond(&s.mu)
	return s
}

func (s *muxStream) pushData(p []byte) {
	if len(p) == 0 {
		return
	}
	s.mu.Lock()
	if s.localClosed || s.aborted {
		s.mu.Unlock()
		return
	}
	s.recvBuf = append(s.recvBuf, p...)
	s.readCond.Broadcast()
	s.mu.Unlock()
}

func (s *muxStream) remoteCloseWrite() {
	s.mu.Lock()
	s.remoteEOF = true
	s.readCond.Broadcast()
	s.mu.Unlock()
}

func (s *muxStream) addSendWindow(delta int64) {
	s.mu.Lock()
	s.sendWindow += delta
	s.writeCond.Broadcast()
	s.mu.Unlock()
}

func (s *muxStream) abort() {
	s.mu.Lock()
	s.aborted = true
	s.remoteEOF = true
	if s.readTimer != nil {
		s.readTimer.Stop()
	}
	if s.writeTimer != nil {
		s.writeTimer.Stop()
	}
	s.readCond.Broadcast()
	s.writeCond.Broadcast()
	s.mu.Unlock()
}

func (s *muxStream) Read(p []byte) (int, error) {
	s.mu.Lock()
	for len(s.recvBuf) == 0 {
		if s.localClosed || s.aborted {
			s.mu.Unlock()
			return 0, io.EOF
		}
		if s.remoteEOF {
			s.mu.Unlock()
			return 0, io.EOF
		}
		if !s.readDeadline.IsZero() && !time.Now().Before(s.readDeadline) {
			s.mu.Unlock()
			return 0, deadlineError{}
		}
		s.readCond.Wait()
	}
	n := copy(p, s.recvBuf)
	if n == len(s.recvBuf) {
		s.recvBuf = s.recvBuf[:0]
	} else {
		s.recvBuf = s.recvBuf[n:]
	}
	s.consumed += n
	var credit uint32
	if s.consumed >= windowRefresh && !s.remoteEOF && !s.aborted && !s.localClosed {
		credit = uint32(s.consumed)
		s.consumed = 0
	}
	s.mu.Unlock()

	if credit > 0 {
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], credit)
		_ = s.mux.sendFrame(s.mux.ctx, frameWindow, s.id, b[:])
	}
	return n, nil
}

func (s *muxStream) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		s.mu.Lock()
		for s.sendWindow <= 0 {
			if s.localClosed || s.writeClosed || s.aborted {
				s.mu.Unlock()
				return written, net.ErrClosed
			}
			if !s.writeDeadline.IsZero() && !time.Now().Before(s.writeDeadline) {
				s.mu.Unlock()
				return written, deadlineError{}
			}
			s.writeCond.Wait()
		}
		if s.localClosed || s.writeClosed || s.aborted {
			s.mu.Unlock()
			return written, net.ErrClosed
		}
		chunk := min(len(p), maxFrameData)
		if int64(chunk) > s.sendWindow {
			chunk = int(s.sendWindow)
		}
		s.sendWindow -= int64(chunk)
		deadline := s.writeDeadline
		s.mu.Unlock()

		ctx := s.mux.ctx
		var cancel context.CancelFunc
		if !deadline.IsZero() {
			ctx, cancel = context.WithDeadline(ctx, deadline)
		}
		err := s.mux.sendFrame(ctx, frameData, s.id, p[:chunk])
		if cancel != nil {
			cancel()
		}
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return written, deadlineError{}
			}
			return written, err
		}
		written += chunk
		p = p[chunk:]
	}
	return written, nil
}

func (s *muxStream) CloseWrite() error {
	s.mu.Lock()
	if s.writeClosed || s.localClosed || s.aborted {
		s.mu.Unlock()
		return nil
	}
	s.writeClosed = true
	s.writeCond.Broadcast()
	s.mu.Unlock()
	return s.mux.sendFrame(s.mux.ctx, frameCloseWrite, s.id, nil)
}

func (s *muxStream) Close() error {
	s.mu.Lock()
	if s.localClosed {
		s.mu.Unlock()
		return nil
	}
	s.localClosed = true
	s.writeClosed = true
	aborted := s.aborted
	if s.readTimer != nil {
		s.readTimer.Stop()
	}
	if s.writeTimer != nil {
		s.writeTimer.Stop()
	}
	s.readCond.Broadcast()
	s.writeCond.Broadcast()
	s.mu.Unlock()

	s.mux.removeStream(s.id)
	if !aborted {
		_ = s.mux.sendFrame(s.mux.ctx, frameReset, s.id, nil)
	}
	return nil
}

func (s *muxStream) LocalAddr() net.Addr  { return s.mux.localAddr }
func (s *muxStream) RemoteAddr() net.Addr { return s.mux.remoteAddr }

func (s *muxStream) SetDeadline(t time.Time) error {
	_ = s.SetReadDeadline(t)
	_ = s.SetWriteDeadline(t)
	return nil
}

func (s *muxStream) SetReadDeadline(t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readDeadline = t
	if s.readTimer != nil {
		s.readTimer.Stop()
		s.readTimer = nil
	}
	if !t.IsZero() {
		d := time.Until(t)
		if d <= 0 {
			s.readCond.Broadcast()
		} else {
			s.readTimer = time.AfterFunc(d, func() {
				s.mu.Lock()
				s.readCond.Broadcast()
				s.mu.Unlock()
			})
		}
	}
	return nil
}

func (s *muxStream) SetWriteDeadline(t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writeDeadline = t
	if s.writeTimer != nil {
		s.writeTimer.Stop()
		s.writeTimer = nil
	}
	if !t.IsZero() {
		d := time.Until(t)
		if d <= 0 {
			s.writeCond.Broadcast()
		} else {
			s.writeTimer = time.AfterFunc(d, func() {
				s.mu.Lock()
				s.writeCond.Broadcast()
				s.mu.Unlock()
			})
		}
	}
	return nil
}

type deadlineError struct{}

func (deadlineError) Error() string   { return "i/o timeout" }
func (deadlineError) Timeout() bool   { return true }
func (deadlineError) Temporary() bool { return true }
