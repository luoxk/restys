// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package http2

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
)

// TODO: use singleflight for dialing and addConnCalls?
type clientConnPool2 struct {
	t *Http2Transport

	mu sync.Mutex // TODO: maybe switch to RWMutex
	// TODO: add support for sharing conns based on cert names
	// (e.g. share conn for googleapis.com and appspot.com)
	conns        map[string][]*http2ClientConn // key is host:port
	dialing      map[string]*dialCall2         // currently in-flight dials
	keys         map[*http2ClientConn][]string
	addConnCalls map[string]*addConnCall2 // in-flight addConnIfNeeded calls
}

type dialCall2 struct {
	_ incomparable
	p *clientConnPool2
	// the context associated with the request
	// that created this dialCall
	ctx  context.Context
	done chan struct{}    // closed when done
	res  *http2ClientConn // valid after done is closed
	err  error            // valid after done is closed
}

// run in its own goroutine.
func (c *dialCall2) dial(ctx context.Context, addr string) {
	const singleUse = false // shared conn
	c.res, c.err = c.p.t.dialClientConn(ctx, addr, singleUse)

	c.p.mu.Lock()
	delete(c.p.dialing, addr)
	if c.err == nil {
		c.p.addConnLocked(addr, c.res)
	}
	c.p.mu.Unlock()

	close(c.done)
}

func (p *clientConnPool2) GetClientConn(req *http.Request, addr string, dialOnMiss bool) (*http2ClientConn, error) {
	// TODO(dneil): Dial a new connection when t.DisableKeepAlives is set?
	if isConnectionCloseRequest(req) && dialOnMiss {
		// It gets its own connection.
		traceGetConn(req, addr)
		const singleUse = true
		cc, err := p.t.dialClientConn(req.Context(), addr, singleUse)
		if err != nil {
			return nil, err
		}
		return cc, nil
	}
	for {
		p.mu.Lock()
		for _, cc := range p.conns[addr] {
			if cc.ReserveNewRequest() {
				// When a connection is presented to us by the net/http package,
				// the GetConn hook has already been called.
				// Don't call it a second time here.
				if !cc.getConnCalled {
					traceGetConn(req, addr)
				}
				cc.getConnCalled = false
				p.mu.Unlock()
				return cc, nil
			}
		}
		if !dialOnMiss {
			p.mu.Unlock()
			return nil, ErrNoCachedConn
		}
		traceGetConn(req, addr)
		call := p.getStartDialLocked(req.Context(), addr)
		p.mu.Unlock()
		<-call.done
		if shouldRetryDial2(call, req) {
			continue
		}
		cc, err := call.res, call.err
		if err != nil {
			return nil, err
		}
		if cc.ReserveNewRequest() {
			return cc, nil
		}
	}
}

func shouldRetryDial2(call *dialCall2, req *http.Request) bool {
	if call.err == nil {
		// No error, no need to retry
		return false
	}
	if call.ctx == req.Context() {
		// If the call has the same context as the request, the dial
		// should not be retried, since any cancellation will have come
		// from this request.
		return false
	}
	if !errors.Is(call.err, context.Canceled) && !errors.Is(call.err, context.DeadlineExceeded) {
		// If the call error is not because of a context cancellation or a deadline expiry,
		// the dial should not be retried.
		return false
	}
	// Only retry if the error is a context cancellation error or deadline expiry
	// and the context associated with the call was canceled or expired.
	return call.ctx.Err() != nil
}

// requires p.mu is held.
func (p *clientConnPool2) getStartDialLocked(ctx context.Context, addr string) *dialCall2 {
	if call, ok := p.dialing[addr]; ok {
		// A dial is already in-flight. Don't start another.
		return call
	}
	call := &dialCall2{p: p, done: make(chan struct{}), ctx: ctx}
	if p.dialing == nil {
		p.dialing = make(map[string]*dialCall2)
	}
	p.dialing[addr] = call
	go call.dial(call.ctx, addr)
	return call
}

func (p *clientConnPool2) AddConnIfNeeded(key string, t *Http2Transport, c net.Conn) (used bool, err error) {
	p.mu.Lock()
	for _, cc := range p.conns[key] {
		if cc.CanTakeNewRequest() {
			p.mu.Unlock()
			return false, nil
		}
	}
	call, dup := p.addConnCalls[key]
	if !dup {
		if p.addConnCalls == nil {
			p.addConnCalls = make(map[string]*addConnCall2)
		}
		call = &addConnCall2{
			p:    p,
			done: make(chan struct{}),
		}
		p.addConnCalls[key] = call
		go call.run(t, key, c)
	}
	p.mu.Unlock()

	<-call.done
	if call.err != nil {
		return false, call.err
	}
	return !dup, nil
}

type addConnCall2 struct {
	_    incomparable
	p    *clientConnPool2
	done chan struct{} // closed when done
	err  error
}

func (c *addConnCall2) run(t *Http2Transport, key string, tc net.Conn) {
	cc, err := t.NewClientConn(tc)

	p := c.p
	p.mu.Lock()
	if err != nil {
		c.err = err
	} else {
		cc.getConnCalled = true // already called by the net/http package
		p.addConnLocked(key, cc)
	}
	delete(p.addConnCalls, key)
	p.mu.Unlock()
	close(c.done)
}

// p.mu must be held
func (p *clientConnPool2) addConnLocked(key string, cc *http2ClientConn) {
	for _, v := range p.conns[key] {
		if v == cc {
			return
		}
	}
	if p.conns == nil {
		p.conns = make(map[string][]*http2ClientConn)
	}
	if p.keys == nil {
		p.keys = make(map[*http2ClientConn][]string)
	}
	p.conns[key] = append(p.conns[key], cc)
	p.keys[cc] = append(p.keys[cc], key)
}

func (p *clientConnPool2) MarkDead(cc *http2ClientConn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, key := range p.keys[cc] {
		vv, ok := p.conns[key]
		if !ok {
			continue
		}
		newList := filterOutClientConn2(vv, cc)
		if len(newList) > 0 {
			p.conns[key] = newList
		} else {
			delete(p.conns, key)
		}
	}
	delete(p.keys, cc)
}

func (p *clientConnPool2) CloseIdleConnections() {
	p.mu.Lock()
	defer p.mu.Unlock()
	// TODO: don't close a cc if it was just added to the pool
	// milliseconds ago and has never been used. There's currently
	// a small race window with the HTTP/1 Transport's integration
	// where it can add an idle conn just before using it, and
	// somebody else can concurrently call CloseIdleConns and
	// break some caller's RoundTrip.
	for _, vv := range p.conns {
		for _, cc := range vv {
			cc.closeIfIdle()
		}
	}
}

func filterOutClientConn2(in []*http2ClientConn, exclude *http2ClientConn) []*http2ClientConn {
	out := in[:0]
	for _, v := range in {
		if v != exclude {
			out = append(out, v)
		}
	}
	// If we filtered it out, zero out the last item to prevent
	// the GC from seeing it.
	if len(in) != len(out) {
		in[len(in)-1] = nil
	}
	return out
}

// shouldRetryDial reports whether the current request should
// retry dialing after the call finished unsuccessfully, for example
// if the dial was canceled because of a context cancellation or
// deadline expiry.
