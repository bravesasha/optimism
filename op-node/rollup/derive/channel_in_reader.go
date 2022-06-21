package derive

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"io"

	"github.com/ethereum/go-ethereum/rlp"

	"github.com/ethereum-optimism/optimism/op-node/eth"
)

// zlib returns an io.ReadCloser but explicitly documents it is also a zlib.Resetter, and we want to use it as such.
type zlibReader interface {
	io.ReadCloser
	zlib.Resetter
}

type ChannelInReader struct {
	ready    bool
	r        *bytes.Reader
	readZlib zlibReader
	readRLP  *rlp.Stream

	l1Origin       eth.L1BlockRef
	originComplete bool
	data           []byte
}

// NewChannelInReader creates a ChannelInReader, which should be Reset(origin) before use.
func NewChannelInReader() *ChannelInReader {
	return &ChannelInReader{}
}

func (cr *ChannelInReader) AddOrigin(origin eth.L1BlockRef) error {
	if cr.l1Origin.Hash != origin.ParentHash {
		return fmt.Errorf("next origin %s does not build on top of current origin %s, but on %s", origin.ID(), cr.l1Origin.ID(), origin.ParentID())
	}
	cr.l1Origin = origin
	cr.originComplete = false
	return nil
}

func (cr *ChannelInReader) EndOrigin() {
	cr.originComplete = true
}

func (cr *ChannelInReader) OriginDone() bool {
	return cr.originComplete
}

func (cr *ChannelInReader) WriteChannel(data []byte) {
	cr.data = data
	cr.ready = false
}

// ReadBatch returns a decoded rollup batch, or an error:
// - io.EOF, if the ChannelInReader source needs more data, to be provided with NextL1Origin() and ResetChannel().
// - any other error (e.g. invalid compression or batch data):
//   the caller should ChannelInReader.NextChannel() before continuing reading the next batch.
//
// It's up to the caller to check CurrentL1Origin() before reading more information.
// The CurrentL1Origin() does not change until the first ReadBatch() after the old source has been completely exhausted.
func (cr *ChannelInReader) ReadBatch(dest *BatchData) error {
	// The channel reader may not be initialized yet,
	// and initializing involves reading (zlib header data), so we do that now.
	if !cr.ready {
		if cr.data == nil {
			return io.EOF
		}
		if cr.r == nil {
			cr.r = bytes.NewReader(cr.data)
		} else {
			cr.r.Reset(cr.data)
		}
		if cr.readZlib == nil {
			// creating a new zlib reader involves resetting it, which reads data, which may error
			zr, err := zlib.NewReader(cr.r)
			if err != nil {
				return err
			}
			cr.readZlib = zr.(zlibReader)
		} else {
			err := cr.readZlib.Reset(cr.r, nil)
			if err != nil {
				return err
			}
		}
		if cr.readRLP == nil {
			cr.readRLP = rlp.NewStream(cr.readZlib, 10_000_000)
		} else {
			cr.readRLP.Reset(cr.readZlib, 10_000_000)
		}
		cr.ready = true
	}
	return cr.readRLP.Decode(dest)
}

// NextChannel forces the next read to continue with the next channel,
// resetting any decoding/decompression state to a fresh start.
func (cr *ChannelInReader) NextChannel() {
	cr.ready = false
	cr.data = nil
}

func (cr *ChannelInReader) Reset(origin eth.L1BlockRef) {
	cr.ready = false
	cr.l1Origin = origin
}

// CurrentL1Origin returns the L1 block that encodes the data that is currently being read.
// Batches should be filtered based on this source.
// Note that the source might not be canonical anymore by the time the data is processed.
func (cr *ChannelInReader) CurrentL1Origin() eth.L1BlockRef {
	return cr.l1Origin
}
