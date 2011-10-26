package gostor

import (
	"fmt"
	"os"
	"hash/crc32"
	"sync"
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

func NewSegmentManager(dc *DiskCache, disk *os.File) (*SegmentManager, os.Error) {
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

func FindActiveSegment(dc *DiskCache) (offset uint64, err os.Error) {
	offset = 0
	for {
		var hdr, tr, p []byte
		var hmagic, tmagic, size uint32
		var next uint64

		// header
//		fmt.Printf("FindActiveSegment offset %v\n", offset)
		hdr, err = dc.Read(offset, SGHeaderSize)
		if err != nil {
			return 0, err
		}

		hmagic, p = gint32(hdr)
		if hmagic != SGHMagic {
			return 0, StrError(fmt.Sprintf("not a segment: header %x expected %x", hmagic, SGHMagic))
		}

		size, p = gint32(p)
		_, p = gint64(p)
		next, p = gint64(p)

//		fmt.Printf("FindActiveSegment offset %v size %v next %v ^0 %v\n", offset, size, next, ^uint64(0))
		// trailer
		tr, err = dc.Read(offset+uint64(size) - SGTrailerSize, SGTrailerSize)
		if err != nil {
			return 0, err
		}

//		fmt.Printf("FindActiveSegment offset %x data %v\n", offset+uint64(size) - SGTrailerSize, tr)
		tmagic, p = gint32(tr)
		if tmagic==SGUnsealed {
			break
		}

		offset = next
		if offset == ^uint64(0) {
			return 0, StrError("no active segment")
		}
	}

//	fmt.Printf("active segment: %v\n", offset)
	return offset, nil		
}

func FormatSegment(disk *os.File, offset uint64, size uint32, prev, next uint64) os.Error {
	b := make([]byte, SGHeaderSize + SGTrailerSize)

	// segment header
//	fmt.Printf("FormatSegment offset %v size %v prev %v next %v\n", offset, size, prev, next)
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

func FormatDisk(disk *os.File, disksz uint64, segsz uint32) os.Error {
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

//	fmt.Printf("Formatted %d segments\n", segnum)
	return nil
}

func ReadSegment(dc *DiskCache, offset uint64) (*Segment, os.Error) {
//	fmt.Printf("ReadSegment offset %v\n", offset)
	b, err := dc.Read(offset, SGHeaderSize + FGHeaderSize)
	if err!=nil {
		return nil, err
	}

	s := new(Segment)
	s.offset = offset
	m, p := gint32(b)
	if m!=SGHMagic {
		return nil, StrError(fmt.Sprintf("not a segment: header %x expected %x", m, SGHMagic))
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
			return nil, StrError(fmt.Sprintf("not a fragment: %x", m))
		case SGSealed, SGUnsealed:
			break L
		case FGMagic:
		}

		sz, _ := gint32(p)
		s.nextfrag += uint64(sz)
		p, err = dc.Read(s.nextfrag, FGHeaderSize)
	}

	return s, nil
}

// TODO: Fix
func (s *Segment) Check(disk *os.File) os.Error {
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
			return StrError("invalid fragment magic")
		}

		sz, p = gint32(p)
		if sz == 0 {
			break
		}

		_, p = gint32(p)
		csum, _ := gint32(p)
		_ = pint32(0, p)
		if csum != crc32.ChecksumIEEE(b[0:sz]) {
			return StrError("invalid checksum")
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

func (sm *SegmentManager) AllocFragment(minsz, datasz uint32) (*Fragment, os.Error) {
//	fmt.Printf("SegmentManager.AllocFragment minsz %d datasz %d\n", minsz, datasz)
	// add the space for the header
	minsz += FGHeaderSize
	datasz += FGHeaderSize
	frag := new(Fragment)

	sm.Lock()
again:
	s := sm.seg
	if s.frag!=nil {
		sm.Unlock()
		return nil, StrError("trying to allocate segment before the previous one is committed")
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
//	fmt.Printf("SegmentManager.AllocFragment datasz %d len(Data) %d\n", datasz, len(frag.Data))
	return frag, nil
}

// called with sm locked
func (sm *SegmentManager) nextSegment() os.Error {
	var tr [SGTrailerSize]byte

	trailer := tr[0:]
	s := sm.seg
	p := pint32(SGSealed, trailer[0:])
	_ = pint32(0, p)
	_, err := sm.disk.WriteAt(trailer[0:], int64(s.nextfrag))
	if err!=nil {
		return err
	}

	// same, at the end of the segment
	_, err = sm.disk.WriteAt(trailer[0:], int64(s.offset + uint64(s.size - SGTrailerSize)))
	if err!=nil {
		return err
	}

	sm.seg = nil
	if s.next == ^uint64(0) {
		return StrError("storage full")
	}

	sm.seg, err = ReadSegment(sm.dc, s.next)
	return err
}

func (sm *SegmentManager) CommitFragment(frag *Fragment, datasz uint32) os.Error {
	var err os.Error

	datasz += FGHeaderSize
	if datasz > frag.size {
		return StrError(fmt.Sprintf("data size exceeds initial allocation allocated %d requested %d", frag.size, datasz))
	}

//	fmt.Printf("SegmentManager.CommitFragment: %v\n", frag.raw[0:datasz])
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

	_, err = sm.disk.WriteAt(frag.raw, int64(frag.offset))
	sm.Lock()
	sm.seg.frag = nil
	sm.Unlock()
	return err
}

func fragmentFindBlock(btype uint16, buf []byte, offset uint64) (root uint64) {
	for len(buf) > 0 {
		bt, p := gint16(buf[4:])
		if bt==btype {
			root = offset
		}

		sz, _ := gint16(p)
		buf = buf[sz + BTHeaderSize:]
		offset += uint64(sz + BTHeaderSize)
	}

	return
}

func (s *Segment) findBlock(btype uint16) (root uint64, err os.Error) {
	var buf []byte

	fmt.Printf("Segment.findBlock: segment size %d\n", s.size)
	for offset:=uint64(SGHeaderSize); offset<uint64(s.size); {
		buf, err = sm.dc.Read(offset, FGHeaderSize)
		if err!=nil {
			return
		}

		sz, _ := gint32(buf[4:])
		fmt.Printf("Segment.findBlock: fragment size %d\n", sz)
		if sz==0 {
			return 0, os.ENOENT
		}

		fr := fragmentFindBlock(btype, buf[16:], offset)
		if fr != 0 {
			root = fr
		}

		offset += uint64(sz)
	}

	return
}

func (sm *SegmentManager) FindBlock(btype uint16) (root uint64, err os.Error) {
	s := sm.seg
	for s != nil {
		root, err = s.findBlock(btype)
		if root!=0 || err!=nil {
			break
		}

		if s.prev == ^uint64(0) {
			err = StrError("block not found")
			break
		}

		s, err = ReadSegment(sm.dc, s.prev)
		if err != nil {
			break
		}
	}

	return
}
