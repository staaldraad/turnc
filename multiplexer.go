package turnc

import (
	"io"
	"io/ioutil"
	"net"

	"go.uber.org/zap"

	"gortc.io/stun"
	"gortc.io/turn"
)

// multiplexer de-multiplexes STUN, TURN and application data
// from one connection into separate ones.
type multiplexer struct {
	log      *zap.Logger
	capacity int
	conn     net.Conn

	stunL, stunR net.Conn
	turnL, turnR net.Conn
	dataL, dataR net.Conn

	sideChan io.PipeWriter
}

func newMultiplexer(conn net.Conn, log *zap.Logger, sideChan io.PipeWriter) *multiplexer {
	m := &multiplexer{conn: conn, capacity: 1500, log: log}
	m.stunL, m.stunR = net.Pipe()
	m.turnL, m.turnR = net.Pipe()
	m.dataL, m.dataR = net.Pipe()
	m.sideChan = sideChan
	go m.readUntilClosed()
	return m
}

func (m *multiplexer) discardData() {
	discardLogged(m.log, "mux: failed to discard dataL: %v", m.dataL)
}

func discardLogged(l *zap.Logger, msg string, r io.Reader) {
	_, err := io.Copy(ioutil.Discard, r)
	if err != nil {
		l.Error(msg, zap.Error(err))
	}
}

func closeLogged(l *zap.Logger, msg string, conn io.Closer) {
	if closeErr := conn.Close(); closeErr != nil {
		l.Error(msg, zap.Error(closeErr))
	}
}

func (m *multiplexer) close() {
	closeLogged(m.log, "mux: failed to close turnR: %v", m.turnR)
	closeLogged(m.log, "mux: failed to close stunR: %v", m.stunR)
	closeLogged(m.log, "mux: failed to close dataR: %v", m.dataR)
}

func (m *multiplexer) readUntilClosed() {
	buf := make([]byte, m.capacity)
	for {
		//fmt.Println("readloop")
		n, err := m.conn.Read(buf)
		m.log.Debug("mux: read", zap.Int("n", n), zap.Error(err))
		if err != nil {
			// End of cycle.
			// TODO: Handle timeouts and temporary errors.
			m.log.Info("connection closed")
			m.close()
			break
		}
		data := buf[:n]
		conn := m.dataR
		switch {
		case stun.IsMessage(data):
			//fmt.Printf("stun: %x\n", (data))
			m.log.Debug("mux: got STUN data")
			conn = m.stunR
		case turn.IsChannelData(data):
			//fmt.Printf("got turn: %s\n", string(data))
			// control channel has data that should go to data channel, but at this point it is a different TCP connection
			m.log.Debug("mux: got TURN data")
			conn = m.turnR

			m.sideChan.Write(data)

			//m.conn.Write(data)
		default:
			//fmt.Printf("app data %x\n", data)
			m.sideChan.Write(data)
			m.log.Debug("mux: got APP data")
		}
		_, err = conn.Write(data)
		if err != nil {
			m.log.Warn("failed to write", zap.Error(err))
		}
	}
}
