package yamux

import (
	"fmt"
	"io"
	"os"
	"time"
)

// Config is used to tune the Yamux session
type Config struct {
	// AcceptBacklog is used to limit how many streams may be
	// waiting an accept.
	AcceptBacklog int

	// EnableKeepalive is used to do a period keep alive
	// messages using a ping.
	EnableKeepAlive bool

	// KeepAliveInterval is how often to perform the keep alive
	KeepAliveInterval time.Duration

	// ConnectionWriteTimeout is meant to be a "safety valve" timeout after
	// we which will suspect a problem with the underlying connection and
	// close it. This is only applied to writes, where's there's generally
	// an expectation that things will move along quickly.
	ConnectionWriteTimeout time.Duration

	// MaxStreamWindowSize is used to control the maximum
	// window size that we allow for a stream.
	MaxStreamWindowSize uint32

	// MaxIncomingStreams bounds peer-initiated stream objects from SYN
	// allocation through final removal. Zero preserves legacy unlimited
	// behavior (still subject to AcceptBacklog); -1 rejects every incoming SYN.
	MaxIncomingStreams int

	// InitialSessionWindowSize enables aggregate DATA flow control when it is
	// non-zero. Both peers must negotiate and configure the same values before
	// constructing the session. The window is shared by all streams in one
	// direction in addition to each stream's own receive window.
	InitialSessionWindowSize uint32

	// MaxSessionWindowSize is the largest aggregate receive window that may be
	// advertised through Session.GrowReceiveWindow. It also bounds aggregate
	// WINDOW_UPDATE credit accepted from the peer.
	MaxSessionWindowSize uint32

	// MaxDataFrameSize bounds DATA frame payloads in aggregate flow-control
	// mode. A smaller frame gives blocked streams regular opportunities to use
	// newly available session credit.
	MaxDataFrameSize uint32

	// InitialStreamAllocationWindowSize is the initial per-stream share of the
	// aggregate receive pool. It prevents one fragmented, stalled stream from
	// consuming all session chunks.
	InitialStreamAllocationWindowSize uint32

	// MaxStreamAllocationWindowSize bounds explicit per-stream allocation
	// growth.
	MaxStreamAllocationWindowSize uint32

	// StreamOpenTimeout is the maximum amount of time that a stream will
	// be allowed to remain in pending state while waiting for an ack from the peer.
	// Once the timeout is reached the session will be gracefully closed.
	// A zero value disables the StreamOpenTimeout allowing unbounded
	// blocking on OpenStream calls.
	StreamOpenTimeout time.Duration

	// StreamCloseTimeout is the maximum time that a stream will allowed to
	// be in a half-closed state when `Close` is called before forcibly
	// closing the connection. Forcibly closed connections will empty the
	// receive buffer, drop any future packets received for that stream,
	// and send a RST to the remote side.
	StreamCloseTimeout time.Duration

	// LogOutput is used to control the log destination. Either Logger or
	// LogOutput can be set, not both.
	LogOutput io.Writer

	// Logger is used to pass in the logger to be used. Either Logger or
	// LogOutput can be set, not both.
	Logger Logger
}

func (c *Config) Clone() *Config {
	c2 := *c
	return &c2
}

// DefaultConfig is used to return a default configuration
func DefaultConfig() *Config {
	return &Config{
		AcceptBacklog:          256,
		EnableKeepAlive:        true,
		KeepAliveInterval:      30 * time.Second,
		ConnectionWriteTimeout: 10 * time.Second,
		MaxStreamWindowSize:    initialStreamWindow,
		StreamCloseTimeout:     5 * time.Minute,
		StreamOpenTimeout:      75 * time.Second,
		LogOutput:              os.Stderr,
	}
}

// VerifyConfig is used to verify the sanity of configuration
func VerifyConfig(config *Config) error {
	if config.AcceptBacklog <= 0 {
		return fmt.Errorf("backlog must be positive")
	}
	if config.KeepAliveInterval <= 0 {
		return fmt.Errorf("keep-alive interval must be positive")
	}
	if config.MaxStreamWindowSize < initialStreamWindow {
		return fmt.Errorf("MaxStreamWindowSize must be larger than %d", initialStreamWindow)
	}
	if config.MaxIncomingStreams < -1 {
		return fmt.Errorf("MaxIncomingStreams must be -1, zero, or positive")
	}
	aggregateValues := config.InitialSessionWindowSize != 0 || config.MaxSessionWindowSize != 0 || config.MaxDataFrameSize != 0 || config.InitialStreamAllocationWindowSize != 0 || config.MaxStreamAllocationWindowSize != 0
	if aggregateValues {
		if config.InitialSessionWindowSize == 0 || config.MaxSessionWindowSize == 0 || config.MaxDataFrameSize == 0 || config.InitialStreamAllocationWindowSize == 0 || config.MaxStreamAllocationWindowSize == 0 {
			return fmt.Errorf("aggregate session window fields must all be non-zero")
		}
		for name, value := range map[string]uint32{
			"InitialSessionWindowSize":          config.InitialSessionWindowSize,
			"MaxSessionWindowSize":              config.MaxSessionWindowSize,
			"InitialStreamAllocationWindowSize": config.InitialStreamAllocationWindowSize,
			"MaxStreamAllocationWindowSize":     config.MaxStreamAllocationWindowSize,
		} {
			if value%AggregateWindowUnitSize != 0 {
				return fmt.Errorf("%s must be a multiple of %d", name, AggregateWindowUnitSize)
			}
		}
		if config.InitialSessionWindowSize > config.MaxSessionWindowSize {
			return fmt.Errorf("InitialSessionWindowSize must not exceed MaxSessionWindowSize")
		}
		if config.MaxSessionWindowSize > AggregateMaxSessionWindowSize {
			return fmt.Errorf("MaxSessionWindowSize must not exceed aggregate protocol ceiling %d", AggregateMaxSessionWindowSize)
		}
		if config.MaxDataFrameSize != AggregateMaxDataFrameSize {
			return fmt.Errorf("MaxDataFrameSize must equal %d in aggregate mode", AggregateMaxDataFrameSize)
		}
		if config.InitialStreamAllocationWindowSize > config.InitialSessionWindowSize/aggregateStreamWindowDivisor {
			return fmt.Errorf("InitialStreamAllocationWindowSize must not exceed one quarter of InitialSessionWindowSize")
		}
		if config.InitialStreamAllocationWindowSize > config.MaxStreamAllocationWindowSize {
			return fmt.Errorf("InitialStreamAllocationWindowSize must not exceed MaxStreamAllocationWindowSize")
		}
		if config.MaxStreamAllocationWindowSize > config.MaxSessionWindowSize/aggregateStreamWindowDivisor {
			return fmt.Errorf("MaxStreamAllocationWindowSize must not exceed one quarter of MaxSessionWindowSize")
		}
	}
	if config.LogOutput != nil && config.Logger != nil {
		return fmt.Errorf("both Logger and LogOutput may not be set, select one")
	} else if config.LogOutput == nil && config.Logger == nil {
		return fmt.Errorf("one of Logger or LogOutput must be set, select one")
	}
	return nil
}

// Server is used to initialize a new server-side connection.
// There must be at most one server-side connection. If a nil config is
// provided, the DefaultConfiguration will be used.
func Server(conn io.ReadWriteCloser, config *Config) (*Session, error) {
	if config == nil {
		config = DefaultConfig()
	}
	if err := VerifyConfig(config); err != nil {
		return nil, err
	}
	return newSession(config, conn, false), nil
}

// Client is used to initialize a new client-side connection.
// There must be at most one client-side connection.
func Client(conn io.ReadWriteCloser, config *Config) (*Session, error) {
	if config == nil {
		config = DefaultConfig()
	}

	if err := VerifyConfig(config); err != nil {
		return nil, err
	}
	return newSession(config, conn, true), nil
}
