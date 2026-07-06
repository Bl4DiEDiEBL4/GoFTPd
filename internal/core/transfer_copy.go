package core

import (
	"io"
	"sync"
)

const transferCopyBufferSize = 1024 * 1024

var transferCopyBufferPool = sync.Pool{
	New: func() interface{} {
		buf := make([]byte, transferCopyBufferSize)
		return &buf
	},
}

func copyTransferData(dst io.Writer, src io.Reader) (int64, error) {
	if trackingDst, ok := dst.(*bandwidthTrackingConn); ok {
		return trackingDst.ReadFrom(src)
	}
	if trackingSrc, ok := src.(*bandwidthTrackingConn); ok {
		return trackingSrc.WriteTo(dst)
	}
	return copyTransferDataBuffered(dst, src)
}

func copyTransferDataBuffered(dst io.Writer, src io.Reader) (int64, error) {
	bufPtr := transferCopyBufferPool.Get().(*[]byte)
	defer transferCopyBufferPool.Put(bufPtr)
	return io.CopyBuffer(dst, src, *bufPtr)
}

func copyTransferDataLoop(dst io.Writer, src io.Reader) (int64, error) {
	bufPtr := transferCopyBufferPool.Get().(*[]byte)
	defer transferCopyBufferPool.Put(bufPtr)
	buf := *bufPtr
	var written int64
	for {
		nr, er := src.Read(buf)
		if nr > 0 {
			nw, ew := dst.Write(buf[:nr])
			if nw > 0 {
				written += int64(nw)
			}
			if ew != nil {
				return written, ew
			}
			if nw != nr {
				return written, io.ErrShortWrite
			}
		}
		if er != nil {
			if er == io.EOF {
				return written, nil
			}
			return written, er
		}
	}
}
