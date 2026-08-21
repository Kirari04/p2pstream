package server

import (
	"net/http/httputil"
	"sync"
)

const reverseProxyCopyBufferSize = 32 * 1024

type reverseProxyCopyBuffer [reverseProxyCopyBufferSize]byte

type reverseProxyBufferPool struct {
	pool sync.Pool
}

func newReverseProxyBufferPool() *reverseProxyBufferPool {
	return &reverseProxyBufferPool{
		pool: sync.Pool{
			New: func() any {
				return new(reverseProxyCopyBuffer)
			},
		},
	}
}

func (p *reverseProxyBufferPool) Get() []byte {
	if p == nil {
		return make([]byte, reverseProxyCopyBufferSize)
	}
	buf, _ := p.pool.Get().(*reverseProxyCopyBuffer)
	if buf == nil {
		return make([]byte, reverseProxyCopyBufferSize)
	}
	return buf[:]
}

func (p *reverseProxyBufferPool) Put(b []byte) {
	if p == nil || cap(b) != reverseProxyCopyBufferSize {
		return
	}
	b = b[:reverseProxyCopyBufferSize]
	p.pool.Put((*reverseProxyCopyBuffer)(b))
}

var defaultReverseProxyBufferPool httputil.BufferPool = newReverseProxyBufferPool()

func (a *App) reverseProxyBufferPool() httputil.BufferPool {
	if a != nil && a.reverseProxyBuffers != nil {
		return a.reverseProxyBuffers
	}
	return defaultReverseProxyBufferPool
}
