package navawi

import (
	"errors"
	"fmt"
	"os"
	"hash/crc32"
	"sync"
	"syscall"
)

const (
	SGHMagic	= 0x48d2a89d
	SGTMagic	= 0x59c1b98c

	SGHeaderSize	= 32
	SGTrailerSize	= 8
	SGSealed	= 0xd60e5e36
	SGUnsealed	= 0x0cd81675
	SGActive	= 0x2f858a21
	FGMagic 	= 0x5f3329ab
	FGHeaderSize	= 16
)

type Segment struct {
	// Header
	// magic	uint32
	size		uint32
	prev		uint64		// address of the previous segment on disk
	next		uint64		// address of the next segment on disk
	// reserved	uint64

	// Trailer
	// magic	uint32		// SGSealed or SGUnsealed
	// reserved	uint32

	offset		uint64
	frag		*Fragment	// uncommitted fragment
	nextfrag	uint64		// offset of the next fragment
}

type Fragment struct {
	// magic	uint32
	size		uint32
	flags		uint32
	// checksum	uint32		// CRC32

	offset		uint64
	DataOffset	uint64
	raw		[]byte		// the whole fragment, including header and trailer
	Data		[]byte		// data part of the fragment
}

type SegmentManager struct {
	sync.Mutex
	dc	*DiskCache	// for reading
	disk	*os.File	// for writing
	seg	*Segment	// currently active segment

	trailer	[]byte
}

func NewSegmentManager(dc *DiskCache, disk *os.File) (*SegmentManager, error) {
	sm := new(SegmentManager)
	sm.dc = dc
	sm.disk = disk
	sm.trailer = make([]byte, SGTrailerSize)

	offset, err := FindActiveSegment(dc)
	if err!=nil {
		return nil, err
	}

	sm.seg, err = ReadSegment(dc, offset)
	if err!=nil {
		return nil, err
	}

	return sm, nil
}

func FindActiveSegment(dc *DiskCache) (offset uint64, err error) {
	offset = 0
	for {
		var hdr, tr, p []byte
		var hmagic, tmagic, size uint32
		var next uint64

		// header
//		debug("FindActiveSegment offset %v\n", offset)
		hdr, err = dc.ReadAll(offset, SGHeaderSize)
		if err != nil {
			return 0, err
		}

		hmagic, p = gint32(hdr)
		if hmagic != SGHMagic {
			return 0, errors.New(fmt.Sprintf("not a segment: header %x expected %x", hmagic, SGHMagic))
		}

		size, p = gint32(p)
		_, p = gint64(p)
		next, p = gint64(p)

//		debug("FindActiveSegment offset %v size %v next %v ^0 %v\n", offset, size, next, ^uint64(0))
		// trailer
		tr, err = dc.ReadAll(offset+uint64(size) - SGTrailerSize, SGTrailerSize)
		if err != nil {
			return 0, err
		}

//		debug("FindActiveSegment offset %x data %v\n", offset+uint64(size) - SGTrailerSize, tr)
		tmagic, p = gint32(tr)
		if tmagic==SGUnsealed {
			break
		}

		offset = next
		if offset == ^uint64(0) {
			return 0, errors.New("no active segment")
		}
	}

//	debug("active segment: %v\n", offset)
	return offset, nil		
}

func FormatSegment(disk *os.File, offset uint64, size uint32, prev, next uint64) error {
	b := make([]byte, SGHeaderSize + SGTrailerSize)

	// segment header
//	debug("FormatSegment offset %v size %v prev %v next %v\n", offset, size, prev, next)
	p := pint32(SGHMagic, b)
	p = pint32(size, p)
	p = pint64(prev, p)
	p = pint64(next, p)
	p = pint64(0, p)	// reserved

	// segment trailer
	p = pint32(SGUnsealed, p)
	_ = pint32(0, p)	// reserved

	_, err := disk.WriteAt(b, int64(offset))
	if err!=nil {
		return err
	}

	// write segment trailer at the end of the segment too
	_, err = disk.WriteAt(b[SGHeaderSize:], int64(offset+uint64(size)-SGTrailerSize))
	return err
}

func FormatDisk(disk *os.File, disksz uint64, segsz uint32) error {
	var prev, offset uint64

	segnum := 0
	for prev, offset = ^uint64(0), 0; offset < disksz; offset += uint64(segsz) {
		next := offset + uint64(segsz)
		if next >= disksz {
			next = ^uint64(0)
		}

		err := FormatSegment(disk, offset, segsz, prev, next)
		if err!=nil {
			return err
		}

		prev = offset
		segnum++
	}

//	debug("Formatted %d segments\n", segnum)
	return nil
}

func ReadSegment(dc *DiskCache, offset uint64) (*Segment, error) {
//	debug("ReadSegment offset %v\n", offset)
	b, err := dc.ReadAll(offset, SGHeaderSize + FGHeaderSize)
	if err!=nil {
		return nil, err
	}

	s := new(Segment)
	s.offset = offset
	m, p := gint32(b)
	if m!=SGHMagic {
		return nil, errors.New(fmt.Sprintf("not a segment: header %x expected %x", m, SGHMagic))
	}

	s.size, p = gint32(p)
	s.prev, p = gint64(p)
	s.next, p = gint64(p)
	_, p = gint64(p)

	s.nextfrag = offset + SGHeaderSize
L:
	for s.nextfrag < offset + uint64(s.size) {
		m, p = gint32(p)
		switch m {
		default:
			return nil, errors.New(fmt.Sprintf("not a fragment: %x", m))
		case SGSealed, SGUnsealed:
			break L
		case FGMagic:
		}

		sz, _ := gint32(p)
		s.nextfrag += uint64(sz)
		p, err = dc.ReadAll(s.nextfrag, FGHeaderSize)
	}

	return s, nil
}

// TODO: Fix
func (s *Segment) Check(disk *os.File) error {
	b := make([]byte, int64(s.size - SGHeaderSize))
	_, err := disk.ReadAt(b, int64(s.offset + SGHeaderSize))
	if err!=nil {
		return err
	}

	// check the consistency of all fragments
	for len(b) > 0 {
		var sz uint32

		m, p := gint32(b)
		if m!=FGMagic {
			return errors.New("invalid fragment magic")
		}

		sz, p = gint32(p)
		if sz == 0 {
			break
		}

		_, p = gint32(p)
		csum, _ := gint32(p)
		_ = pint32(0, p)
		if csum != crc32.ChecksumIEEE(b[0:sz]) {
			return errors.New("invalid checksum")
		}

		b = b[sz:]
	}

	return nil
}

func (s *Segment) freeSpace() uint32 {
	return s.size - uint32(s.nextfrag - s.offset) - SGTrailerSize
}

func (s *Segment) isFull() bool {
	return s.freeSpace() == 0
}

func (sm *SegmentManager) AllocFragment(minsz, datasz uint32) (*Fragment, error) {
//	debug("SegmentManager.AllocFragment minsz %d datasz %d\n", minsz, datasz)
	// add the space for the header
	minsz += FGHeaderSize
	datasz += FGHeaderSize
	frag := new(Fragment)

	sm.Lock()
again:
	s := sm.seg
	if s.frag!=nil {
		sm.Unlock()
		return nil, errors.New("trying to allocate segment before the previous one is committed")
	}

	free := s.freeSpace()
	if free < minsz {
		err := sm.nextSegment()
		if err!=nil {
			sm.Unlock()
			return nil, err
		}

		goto again
	}

	if datasz > free {
		datasz = free
	}

	offset := s.nextfrag
	s.nextfrag += uint64(datasz)
	s.frag = frag
	sm.Unlock()

	frag.offset = offset
	frag.DataOffset = offset + FGHeaderSize
	frag.size = datasz
	frag.raw = make([]byte, datasz + SGTrailerSize)
	frag.Data = frag.raw[FGHeaderSize:datasz]
//	debug("SegmentManager.AllocFragment datasz %d len(Data) %d\n", datasz, len(frag.Data))
	return frag, nil
}

// called with sm locked
func (sm *SegmentManager) nextSegment() error {
	var tr [SGTrailerSize]byte

	trailer := tr[0:]
	s := sm.seg
	p := pint32(SGSealed, trailer[0:])
	_ = pint32(0, p)
	_, err := sm.disk.WriteAt(trailer[0:], int64(s.nextfrag))
	if err!=nil {
		return err
	}
	sm.dc.Discard(s.nextfrag, s.nextfrag+SGTrailerSize)

	// same, at the end of the segment
	_, err = sm.disk.WriteAt(trailer[0:], int64(s.offset + uint64(s.size - SGTrailerSize)))
	if err!=nil {
		return err
	}
	sm.dc.Discard(s.offset + uint64(s.size - SGTrailerSize), s.offset + uint64(s.size))

	sm.seg = nil
	if s.next == ^uint64(0) {
		debug("** storage full\n")
		return errors.New("storage full")
	}

	sm.seg, err = ReadSegment(sm.dc, s.next)
	return err
}

func (sm *SegmentManager) CommitFragment(frag *Fragment, datasz uint32) error {
	var err error

	datasz += FGHeaderSize
	if datasz > frag.size {
		return errors.New(fmt.Sprintf("data size exceeds initial allocation allocated %d requested %d", frag.size, datasz))
	}

//	debug("SegmentManager.CommitFragment: %v\n", frag.raw[0:datasz])
	frag.size = datasz
	frag.raw = frag.raw[0:datasz+SGTrailerSize]

	// write the header
	p := pint32(FGMagic, frag.raw)
	p = pint32(frag.size, p)
	p = pint32(0, p)
	_ = pint32(0, p)
	_ = pint32(crc32.ChecksumIEEE(frag.raw[0:frag.size]), p)

	// write the trailer
	p = pint32(SGUnsealed, frag.raw[frag.size:])
	_ = pint32(0, p)

	debug("WriteAt %d count %d\n", frag.offset, len(frag.raw))
	_, err = sm.disk.WriteAt(frag.raw, int64(frag.offset))
	sm.dc.Discard(frag.offset, frag.offset+uint64(len(frag.raw)))
	sm.Lock()
	sm.seg.frag = nil
	sm.Unlock()
	return err
}

func fragmentFindBlock(btype uint16, buf []byte, offset uint64) (root uint64, err error) {
	for len(buf) >= BTHeaderSize {
		m, _ := gint32(buf)
		if m!=BTMagic {
			err = errors.New(fmt.Sprintf("invalid block magic at offset %d expected %x got %x", offset, BTMagic, m))
			return
		}

		sz, p := gint16(buf[4:])
		if sz==0 {
			debug("fragmentFindBlock: sz 0 buf %v\n", buf)
			break
		}

		bt, _ := gint16(p)
		debug("fragmentFindBlock: offset %d sz %d type %x looking for %x\n", offset, sz, bt, btype)
		if bt==btype {
			root = offset
		}

		sz += sz%2 + BTHeaderSize
		
		buf = buf[sz:]
		offset += uint64(sz)
		debug("fragmentFindBlock: remaining %d\n", len(buf))
	}

//	debug("block root %d\n", root)
	return
}

func (s *Segment) findBlock(btype uint16) (root uint64, err error) {
	var buf []byte
	var fr uint64

	debug("Segment.findBlock: segment size %d\n", s.size)
	for offset:=uint64(SGHeaderSize); offset<uint64(s.size); {
		buf, err = sm.dc.ReadAll(offset, FGHeaderSize)
		if err!=nil {
			return
		}

		m, _ := gint32(buf)
		if m==SGSealed || m==SGUnsealed {
			break
		}

		if m!=FGMagic {
			return 0, errors.New(fmt.Sprintf("invalid fragment magic at offset %d expected %x got %x", offset, FGMagic, m))
		}

		sz, _ := gint32(buf[4:])
		if sz==0 {
			break
		}

		debug("Segment.findBlock: fragment size %d header %v\n", sz, buf)
		buf, err = sm.dc.ReadAll(offset+FGHeaderSize, sz-FGHeaderSize)
		if err!=nil {
			return
		}

		fr, err = fragmentFindBlock(btype, buf, offset+FGHeaderSize)
		if err!=nil {
			return
		}

		if fr != 0 {
			root = fr
		}

		offset += uint64(sz)
	}

	if root==0 {
		err = syscall.ENOENT
	}

	return
}

func (sm *SegmentManager) FindBlock(btype uint16) (root uint64, err error) {
	s := sm.seg
	for s != nil {
		root, err = s.findBlock(btype)
		if root!=0 || err!=nil {
			break
		}

		if s.prev == ^uint64(0) {
			err = errors.New("block not found")
			break
		}

		s, err = ReadSegment(sm.dc, s.prev)
		if err != nil {
			break
		}
	}

	if err!=nil {
		debug("SegmentManager.FindBlock err %s\n", err)
	}

	return
}
