package main

// Minimal streaming ZIP writer (method "store", no compression) with ZIP64
// support. Writes entries with data descriptors so nothing is buffered:
// memory use is constant regardless of archive size, and CPU cost is just CRC32.

import (
	"bufio"
	"encoding/binary"
	"errors"
	"hash"
	"hash/crc32"
	"io"
	"time"
)

const u32max = 0xffffffff

type zipEntry struct {
	name    string
	crc     uint32
	size    uint64
	offset  uint64
	dosTime uint16
	dosDate uint16
	isDir   bool
}

type zipStream struct {
	w       *bufio.Writer
	cw      *countWriter
	entries []*zipEntry
	cur     *zipEntry
	crc     hash.Hash32
	written uint64
	closed  bool
}

type countWriter struct {
	w io.Writer
	n uint64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += uint64(n)
	return n, err
}

func newZipStream(w io.Writer) *zipStream {
	cw := &countWriter{w: w}
	return &zipStream{cw: cw, w: bufio.NewWriterSize(cw, 64<<10)}
}

func (z *zipStream) offset() uint64 { return z.cw.n + uint64(z.w.Buffered()) }

func dosDateTime(t time.Time) (uint16, uint16) {
	t = t.Local()
	if t.Year() < 1980 {
		t = time.Date(1980, 1, 1, 0, 0, 0, 0, time.Local)
	}
	d := uint16((t.Year()-1980)<<9 | int(t.Month())<<5 | t.Day())
	tm := uint16(t.Hour()<<11 | t.Minute()<<5 | t.Second()/2)
	return tm, d
}

type le []byte

func (b *le) u16(v uint16) { *b = binary.LittleEndian.AppendUint16(*b, v) }
func (b *le) u32(v uint32) { *b = binary.LittleEndian.AppendUint32(*b, v) }
func (b *le) u64(v uint64) { *b = binary.LittleEndian.AppendUint64(*b, v) }

// Create starts a new entry; for directories name must end in "/".
func (z *zipStream) Create(name string, mod time.Time, isDir bool) (io.Writer, error) {
	if z.closed {
		return nil, errors.New("zip closed")
	}
	if err := z.finishEntry(); err != nil {
		return nil, err
	}
	e := &zipEntry{name: name, offset: z.offset(), isDir: isDir}
	e.dosTime, e.dosDate = dosDateTime(mod)
	var b le
	b.u32(0x04034b50)
	b.u16(45)              // version needed (zip64)
	b.u16(0x0008 | 0x0800) // data descriptor + UTF-8 names
	b.u16(0)               // store
	b.u16(e.dosTime)
	b.u16(e.dosDate)
	b.u32(0) // crc (in descriptor)
	b.u32(0) // sizes (in descriptor)
	b.u32(0)
	b.u16(uint16(len(name)))
	b.u16(0)
	b = append(b, name...)
	if _, err := z.w.Write(b); err != nil {
		return nil, err
	}
	z.cur = e
	z.entries = append(z.entries, e)
	z.crc = crc32.NewIEEE()
	z.written = 0
	return z, nil
}

// Write implements io.Writer for the current entry.
func (z *zipStream) Write(p []byte) (int, error) {
	if z.cur == nil || z.cur.isDir {
		return 0, errors.New("no file entry open")
	}
	n, err := z.w.Write(p)
	z.crc.Write(p[:n])
	z.written += uint64(n)
	return n, err
}

func (z *zipStream) finishEntry() error {
	e := z.cur
	if e == nil {
		return nil
	}
	z.cur = nil
	e.size = z.written
	if !e.isDir {
		e.crc = z.crc.Sum32()
	}
	var b le
	b.u32(0x08074b50)
	b.u32(e.crc)
	if e.size >= u32max {
		b.u64(e.size)
		b.u64(e.size)
	} else {
		b.u32(uint32(e.size))
		b.u32(uint32(e.size))
	}
	_, err := z.w.Write(b)
	return err
}

func (z *zipStream) Close() error {
	if z.closed {
		return nil
	}
	z.closed = true
	if err := z.finishEntry(); err != nil {
		return err
	}
	cdStart := z.offset()
	for _, e := range z.entries {
		needZ64 := e.size >= u32max || e.offset >= u32max
		var extra le
		if needZ64 {
			extra.u16(0x0001)
			extra.u16(24)
			extra.u64(e.size)
			extra.u64(e.size)
			extra.u64(e.offset)
		}
		var b le
		b.u32(0x02014b50)
		b.u16(3<<8 | 45) // made by unix, v4.5
		b.u16(45)
		b.u16(0x0008 | 0x0800)
		b.u16(0)
		b.u16(e.dosTime)
		b.u16(e.dosDate)
		b.u32(e.crc)
		if needZ64 {
			b.u32(u32max)
			b.u32(u32max)
		} else {
			b.u32(uint32(e.size))
			b.u32(uint32(e.size))
		}
		b.u16(uint16(len(e.name)))
		b.u16(uint16(len(extra)))
		b.u16(0) // comment
		b.u16(0) // disk
		b.u16(0) // internal attrs
		if e.isDir {
			b.u32((0o40755 << 16) | 0x10)
		} else {
			b.u32(0o100644 << 16)
		}
		if needZ64 {
			b.u32(u32max)
		} else {
			b.u32(uint32(e.offset))
		}
		b = append(b, e.name...)
		b = append(b, extra...)
		if _, err := z.w.Write(b); err != nil {
			return err
		}
	}
	cdEnd := z.offset()
	cdSize := cdEnd - cdStart
	n := uint64(len(z.entries))
	var b le
	if n >= 0xffff || cdSize >= u32max || cdStart >= u32max {
		// zip64 end of central directory record + locator
		b.u32(0x06064b50)
		b.u64(44)
		b.u16(45)
		b.u16(45)
		b.u32(0)
		b.u32(0)
		b.u64(n)
		b.u64(n)
		b.u64(cdSize)
		b.u64(cdStart)
		b.u32(0x07064b50)
		b.u32(0)
		b.u64(cdEnd)
		b.u32(1)
	}
	b.u32(0x06054b50)
	b.u16(0)
	b.u16(0)
	b.u16(uint16(min(n, 0xffff)))
	b.u16(uint16(min(n, 0xffff)))
	b.u32(uint32(min(cdSize, u32max)))
	b.u32(uint32(min(cdStart, u32max)))
	b.u16(0)
	if _, err := z.w.Write(b); err != nil {
		return err
	}
	return z.w.Flush()
}
