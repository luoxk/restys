package http3

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/textproto"
	"sync"
	"time"

	"github.com/quic-go/qpack"
	"github.com/quic-go/quic-go"

	"github.com/luoxk/restys/internal/dump"
	"github.com/luoxk/restys/internal/quic-go/quicvarint"
	"github.com/luoxk/restys/internal/transport"
)

const (
	// MethodGet0RTT allows a GET request to be sent using 0-RTT.
	// Note that 0-RTT doesn't provide replay protection and should only be used for idempotent requests.
	MethodGet0RTT = "GET_0RTT"
	// MethodHead0RTT allows a HEAD request to be sent using 0-RTT.
	// Note that 0-RTT doesn't provide replay protection and should only be used for idempotent requests.
	MethodHead0RTT = "HEAD_0RTT"
)

const (
	defaultMaxResponseHeaderBytes = 10 * 1 << 20 // 10 MB
)

var defaultQuicConfig = &quic.Config{
	MaxIncomingStreams: -1, // don't allow the server to create bidirectional streams
	KeepAlivePeriod:    10 * time.Second,
}

// SingleDestinationRoundTripper is an HTTP/3 client doing requests to a single remote server.
type SingleDestinationRoundTripper struct {
	*transport.Options

	Connection quic.Connection

	// Enable support for HTTP/3 datagrams (RFC 9297).
	// If a QUICConfig is set, datagram support also needs to be enabled on the QUIC layer by setting EnableDatagrams.
	EnableDatagrams bool

	// Additional HTTP/3 settings.
	// It is invalid to specify any settings defined by RFC 9114 (HTTP/3) and RFC 9297 (HTTP Datagrams).
	AdditionalSettings map[uint64]uint64
	StreamHijacker     func(FrameType, quic.ConnectionTracingID, quic.Stream, error) (hijacked bool, err error)
	UniStreamHijacker  func(ServerStreamType, quic.ConnectionTracingID, quic.ReceiveStream, error) (hijacked bool)

	initOnce      sync.Once
	hconn         *connection
	requestWriter *requestWriter
	decoder       *qpack.Decoder
}

var _ http.RoundTripper = &SingleDestinationRoundTripper{}

func (c *SingleDestinationRoundTripper) Start() Connection {
	c.initOnce.Do(func() { c.init() })
	return c.hconn
}

func (c *SingleDestinationRoundTripper) init() {
	c.decoder = qpack.NewDecoder(func(hf qpack.HeaderField) {})
	c.requestWriter = newRequestWriter()
	c.hconn = newConnection(
		c.Connection.Context(),
		c.Connection,
		c.EnableDatagrams,
		PerspectiveClient,
		0,
		c.Options,
	)
	// send the SETTINGs frame, using 0-RTT data, if possible
	go func() {
		if err := c.setupConn(c.hconn); err != nil {
			if c.Debugf != nil {
				c.Debugf("Setting up connection failed: %s", err.Error())
			}
			c.hconn.CloseWithError(quic.ApplicationErrorCode(ErrCodeInternalError), "")
		}
	}()
	if c.StreamHijacker != nil {
		go c.handleBidirectionalStreams()
	}
	go c.hconn.HandleUnidirectionalStreams(c.UniStreamHijacker)
}

func (c *SingleDestinationRoundTripper) setupConn(conn *connection) error {
	// open the control stream
	str, err := conn.OpenUniStream()
	if err != nil {
		return err
	}
	b := make([]byte, 0, 64)
	b = quicvarint.Append(b, streamTypeControlStream)
	// send the SETTINGS frame
	b = (&settingsFrame{Datagram: c.EnableDatagrams, Other: c.AdditionalSettings}).Append(b)
	_, err = str.Write(b)
	return err
}

func (c *SingleDestinationRoundTripper) handleBidirectionalStreams() {
	for {
		str, err := c.hconn.AcceptStream(context.Background())
		if err != nil {
			if c.Debugf != nil {
				c.Debugf("accepting bidirectional stream failed: %s", err.Error())
			}
			return
		}
		fp := &frameParser{
			r:    str,
			conn: c.hconn,
			unknownFrameHandler: func(ft FrameType, e error) (processed bool, err error) {
				id := c.hconn.Context().Value(quic.ConnectionTracingKey).(quic.ConnectionTracingID)
				return c.StreamHijacker(ft, id, str, e)
			},
		}
		go func() {
			if _, err := fp.ParseNext(); err == errHijacked {
				return
			}
			if err != nil {
				if c.Debugf != nil {
					c.Debugf("error handling stream: %s", err.Error())
				}
			}
			c.hconn.CloseWithError(quic.ApplicationErrorCode(ErrCodeFrameUnexpected), "received HTTP/3 frame on bidirectional stream")
		}()
	}
}

func (c *SingleDestinationRoundTripper) maxHeaderBytes() uint64 {
	if c.MaxResponseHeaderBytes <= 0 {
		return defaultMaxResponseHeaderBytes
	}
	return uint64(c.MaxResponseHeaderBytes)
}

// RoundTrip executes a request and returns a response
func (c *SingleDestinationRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	c.initOnce.Do(func() { c.init() })

	rsp, err := c.roundTrip(req)
	if err != nil && req.Context().Err() != nil {
		// if the context was canceled, return the context cancellation error
		err = req.Context().Err()
	}
	return rsp, err
}

func (c *SingleDestinationRoundTripper) roundTrip(req *http.Request) (*http.Response, error) {
	// Immediately send out this request, if this is a 0-RTT request.
	switch req.Method {
	case MethodGet0RTT:
		// don't modify the original request
		reqCopy := *req
		req = &reqCopy
		req.Method = http.MethodGet
	case MethodHead0RTT:
		// don't modify the original request
		reqCopy := *req
		req = &reqCopy
		req.Method = http.MethodHead
	default:
		// wait for the handshake to complete
		earlyConn, ok := c.Connection.(quic.EarlyConnection)
		if ok {
			select {
			case <-earlyConn.HandshakeComplete():
			case <-req.Context().Done():
				return nil, req.Context().Err()
			}
		}
	}

	// It is only possible to send an Extended CONNECT request once the SETTINGS were received.
	// See section 3 of RFC 8441.
	if isExtendedConnectRequest(req) {
		connCtx := c.Connection.Context()
		// wait for the server's SETTINGS frame to arrive
		select {
		case <-c.hconn.ReceivedSettings():
		case <-connCtx.Done():
			return nil, context.Cause(connCtx)
		}
		if !c.hconn.Settings().EnableExtendedConnect {
			return nil, errors.New("http3: server didn't enable Extended CONNECT")
		}
	}

	reqDone := make(chan struct{})
	str, err := c.hconn.openRequestStream(req.Context(), c.requestWriter, reqDone, c.DisableCompression, c.maxHeaderBytes())
	if err != nil {
		return nil, err
	}

	// Request Cancellation:
	// This go routine keeps running even after RoundTripOpt() returns.
	// It is shut down when the application is done processing the body.
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-req.Context().Done():
			str.CancelWrite(quic.StreamErrorCode(ErrCodeRequestCanceled))
			str.CancelRead(quic.StreamErrorCode(ErrCodeRequestCanceled))
		case <-reqDone:
		}
	}()

	rsp, err := c.doRequest(req, str)
	if err != nil { // if any error occurred
		close(reqDone)
		<-done
		return nil, maybeReplaceError(err)
	}
	return rsp, maybeReplaceError(err)
}

func (c *SingleDestinationRoundTripper) OpenRequestStream(ctx context.Context) (RequestStream, error) {
	c.initOnce.Do(func() { c.init() })

	return c.hconn.openRequestStream(ctx, c.requestWriter, nil, c.DisableCompression, c.maxHeaderBytes())
}

// cancelingReader reads from the io.Reader.
// It cancels writing on the stream if any error other than io.EOF occurs.
type cancelingReader struct {
	r   io.Reader
	str Stream
}

func (r *cancelingReader) Read(b []byte) (int, error) {
	n, err := r.r.Read(b)
	if err != nil && err != io.EOF {
		r.str.CancelWrite(quic.StreamErrorCode(ErrCodeRequestCanceled))
	}
	return n, err
}

func (c *SingleDestinationRoundTripper) sendRequestBody(str Stream, body io.ReadCloser, dumps []*dump.Dumper) error {
	defer body.Close()
	buf := make([]byte, bodyCopyBufferSize)
	sr := &cancelingReader{str: str, r: body}
	var w io.Writer = str
	if len(dumps) > 0 {
		for _, d := range dumps {
			w = io.MultiWriter(w, d.RequestBodyOutput())
		}
	}
	writeTail := func() {
		for _, d := range dumps {
			d.Output().Write([]byte("\r\n\r\n"))
		}
	}
	written, err := io.CopyBuffer(w, sr, buf)
	if len(dumps) > 0 && err == nil && written > 0 {
		writeTail()
	}

	return err
}

func (c *SingleDestinationRoundTripper) doRequest(req *http.Request, str *requestStream) (*http.Response, error) {
	if err := str.SendRequestHeader(req); err != nil {
		return nil, err
	}
	if req.Body == nil {
		str.Close()
	} else {
		// send the request body asynchronously
		go func() {
			dumps := dump.GetDumpers(req.Context(), c.Dump)
			if err := c.sendRequestBody(str, req.Body, dumps); err != nil {
				if c.Debugf != nil {
					c.Debugf("error writing request: %s", err.Error())
				}
			}
			str.Close()
		}()
	}

	// copy from net/http: support 1xx responses
	trace := httptrace.ContextClientTrace(req.Context())
	num1xx := 0               // number of informational 1xx headers received
	const max1xxResponses = 5 // arbitrary bound on number of informational responses

	var res *http.Response
	for {
		var err error
		res, err = str.ReadResponse()
		if err != nil {
			return nil, err
		}
		resCode := res.StatusCode
		is1xx := 100 <= resCode && resCode <= 199
		// treat 101 as a terminal status, see https://github.com/golang/go/issues/26161
		is1xxNonTerminal := is1xx && resCode != http.StatusSwitchingProtocols
		if is1xxNonTerminal {
			num1xx++
			if num1xx > max1xxResponses {
				return nil, errors.New("http: too many 1xx informational responses")
			}
			if trace != nil && trace.Got1xxResponse != nil {
				if err := trace.Got1xxResponse(resCode, textproto.MIMEHeader(res.Header)); err != nil {
					return nil, err
				}
			}
			continue
		}
		break
	}
	connState := c.hconn.ConnectionState().TLS
	res.TLS = &connState
	res.Request = req
	return res, nil
}
