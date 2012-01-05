package gostor

import (
	"errors"
	"fmt"
)

const (
	BTEntrySize	= 16
	BTMaxLevels	= 8
)

type BTEntry struct {
	block	uint64	// block that describes the subtree this entry is root of (0 == hole)
	size	uint64	// size of all data that is defined by the subtree
}

type BTPath struct {
	levels	int
	entries	[BTMaxLevels] BPathEntry
}

type BPathEntry struct {
	*Block
	pos		uint64		// current entry in the block (if not leaf)
	entnum		int		// number of entries (if not leaf)
	start, end	uint64		// offset of the beginning of the entry
}

func search(base uint64, offset uint64, data []byte) (pos int, start, end uint64) {
	pos = 0
	start = base
	end = base
	nent := len(data) / BTEntrySize
	for pos=0; pos < nent-1; pos++ {
		sz, _ := gint64(data[pos*BTEntrySize+8:])
		end = start + sz
		if offset <= end {
//			debug("search: offset %d pos %d start %d end %d\n", offset, pos, start, end)
			return
		}

		start = end
	}

	// last element
	sz, _ := gint64(data[pos*BTEntrySize+8:])
	end = start + sz
//	debug("search: offset %d pos %d start %d end %d\n", offset, pos, start, end)
	return
}

func find(root uint64, offset uint64) (bp *BTPath, err error) {
	var b *Block
	var pos int
	var end uint64

	bp = new(BTPath)
	if root==0 {
		// empty file
//		bp.entries[0].pos = offset
		return bp, nil
	}

	b, err = bc.Get(root)
	if err!=nil {
		bc.Release(b)
		return nil, err
	}

	if !b.IsRoot() {
		return nil, errors.New("not root")
	}

//	debug("Root level %d %v\n", b.Level(), b.Data)
	start := uint64(0)
	bp.levels = b.Level() + 1
//	debug("levels: %v\n", bp.levels)
	for l := bp.levels - 1; l >= 0; l-- {
		e := &bp.entries[l]
		e.Block = b
		if l==0 {
//			debug("levels ------------------ %d\n", bp.levels)
			if bp.levels>1 {
				e.start = bp.entries[l+1].start
			} else {
				e.start = 0
			}

			if b!=nil {
				e.end = e.start + uint64(len(e.Data))
			} else {
				e.end = bp.entries[l+1].end
			}

			e.pos = offset - e.start
			if e.pos > e.end-e.start {
				e.pos = e.end - e.start
			}

//			if e.Block!=nil {
//				debug("********** pos %d len %d start %d end %d offset %d\n", e.pos, len(e.Data), e.start, e.end, offset)
//			} else {
//				debug("********** hole pos %d start %d end %d offset %d\n", e.pos, e.start, e.end, offset)
//			}
			break
		}

		e.entnum = len(b.Data) / BTEntrySize
		b.RLock()
		pos, start, end = search(start, offset, b.Data)
//		debug("find: level %d pos %d start %d end %d\n", l, pos, start, end)
		e.pos = uint64(pos)
		e.start = start
		e.end = end

		blk := uint64(0)
		if b!=nil {
			blk, _ = gint64(b.Data[pos*BTEntrySize:])
		}
		b.RUnlock()

		if blk==0 {
			b = nil
			continue
		}

		b, err = bc.Get(blk)
		if err!=nil {
			for i:=0; i<BTMaxLevels; i++ {
				if bp.entries[i].Block != nil {
					bc.Release(bp.entries[i].Block)
				}
			}

			return nil, err
		}
	}

	return
}

// Moves the specified BTPath to the next block on the specified level, returns false if end of file
// After the call, entries below the specified level are not up-to-date
func (bp *BTPath) next(level int) (bool, error) {
	var l int

	for l = level + 1; l < bp.levels; l++ {
		e := &bp.entries[l]

		if int(e.pos+1) < e.entnum {
			break
		}
	}

	if l >= bp.levels {
		// end of file
		return false, nil
	}

	e := &bp.entries[l]
	e.pos++
//	debug("BTPath.next l %d e.pos %d e.entnum %d len %d\n", l, e.pos, e.entnum, len(e.Data))
	e.RLock()
	blk, p := gint64(e.Data[e.pos*BTEntrySize:])
	sz, _ := gint64(p)
	e.RUnlock()
//	debug("BTPath.next blk %v sz %v\n", blk, sz)
	e.start += e.end
	e.end += e.start + sz
	for ; l > level; l-- {
		e := &bp.entries[l - 1]
		if blk==0 {
			// hole
			e.Block = nil
			e.pos = 0
			e.start = bp.entries[l].start
			e.end = e.start + sz
		} else {
			b, err := bc.Get(blk)
			if err!=nil {
				return false, err
			}

			bc.Release(e.Block)
			e.start = e.end
			e.end = e.start + sz
			e.pos = 0
			e.entnum = len(b.Data) / BTEntrySize
			if l>1 {
				b.RLock()
				blk, p = gint64(b.Data)
				sz, _ = gint64(p)
				b.RUnlock()
			}

			e.Block = b
		}

//		debug("BTPath.next level %d start %v end %v\n", l - 1, e.start, e.end)
	}

	return true, nil
}

// Size of the next data block, 0 if hole or the end of the file
// Doesn't change the specified BTPath
func (bp *BTPath) nextSize(level int) (int, error) {
	var l int

	for l = level + 1; l < bp.levels; l++ {
		e := &bp.entries[l]

		if int(e.pos+1) < e.entnum {
			break
		}
	}

	if l >= bp.levels {
		// end of file
		return 0, nil
	}

	e := &bp.entries[l]
	e.RLock()
	blk, p := gint64(e.Data[(e.pos+1)*BTEntrySize:])
	sz, _ := gint64(p)
	e.RUnlock()
//	debug("nextSize: level %d pos %d entnum %d sz %d\n", l, e.pos, e.entnum, sz)

	for ; l>level+1; l-- {
		b, err := bc.Get(blk)
		if err!=nil {
//			debug("nextSize: error %v\n", err)
			return 0, err
		}

		b.RLock()
		blk, p = gint64(b.Data)
		sz, _ = gint64(p)
		b.RUnlock()
		bc.Release(b)
	}

	if l > 0 {
		b, err := bc.Get(blk)
		if err!=nil {
			return 0, err
		}

		sz = uint64(len(b.Data))
		bc.Release(b)
	} else if blk==0 {
		sz = 0
	}

	return int(sz), nil
}

// Moves the specified BTPath to the previous data block, returns false if beginning of file.
// Entries below the specified level are not up-to-date after the call
func (bp *BTPath) prev(level int) (bool, error) {
	var l int

	// find the first useful entry
	for l = level + 1; l < bp.levels; l++ {
//		debug("prev: finding the first useful entry l %d pos %d\n", l, bp.entries[l].pos)
		if bp.entries[l].pos>0 {
			break
		}
	}

	if l >= bp.levels {
		// beginning of the file
//		debug("prev: 1 pos %d\n", bp.entries[level].pos)
		return false, nil
	}

	e := &bp.entries[l]
	e.pos--
	e.RLock()
	blk, p := gint64(e.Data[e.pos*BTEntrySize:])
	sz, _ := gint64(p)
	e.RUnlock()
	e.start -= sz
	e.end -= sz
//	debug("prev: l %d bp.levels %d pos %d blk %d sz %d\n", l, bp.levels, e.pos, blk, sz)

	for ; l > level; l-- {
		e := &bp.entries[l - 1]
//		opos := e.pos
		if blk==0 {
			// hole
			e.Block = nil
			e.pos = 0
			e.start = bp.entries[l].start
			e.end = e.start + sz
		} else {
			b, err := bc.Get(blk)
			if err!=nil {
				return false, err
			}

			bc.Release(e.Block)
			e.Block = b
			if l > 1 {
				e.pos = uint64(len(b.Data) / BTEntrySize - 1)
				b.RLock()
				blk, p = gint64(b.Data[e.pos*BTEntrySize:])
				sz, _ = gint64(p)
				b.RUnlock()
				e.start -= sz
				e.end -= sz
			} else {
				e.pos = 0
				e.start -= uint64(b.Length)
				e.end = e.start + uint64(b.Length)
			}
		}

//		debug("prev: level %d old pos %d pos %d\n", l - 1, opos, e.pos)
	}

//	debug("prev: 2 pos %d\n", bp.entries[level].pos)
	return true, nil
}

// Size of the previous data block. 0 if hole or beginning of file
// Doesn't change the specified BTPath
func (bp *BTPath) prevSize(level int) (int, error) {
	var l int

//	debug("prevSize: level %d\n", level)
	// find the first useful entry
	for l = level + 1; l < bp.levels; l++ {
		if bp.entries[l].pos>0 {
			break
		}
	}

	if l >= bp.levels {
		// beginning of the file
//		debug("prevSize: beginning of file\n")
		return 0, nil
	}

	e := &bp.entries[l]
	pos := e.pos - 1
	e.RLock()
	blk, p := gint64(e.Data[pos*BTEntrySize:])
	sz, _ := gint64(p)
	e.RUnlock()

//	debug("prevSize: l %d bp.levels %d pos %d blk %d sz %d\n", l, bp.levels, pos, blk, sz)
	for ; l > level+1; l-- {
		b, err := bc.Get(blk)
		if err!=nil {
//			debug("prevSize: error %v\n", err)
			return 0, err
		}

		if l > 1 {
			en := len(b.Data) / BTEntrySize
			b.RLock()
			blk, p = gint64(b.Data[(en - 1)*BTEntrySize:])
			sz, _ = gint64(p)
			b.RUnlock()
		}

		bc.Release(b)
	}

	if l > 0 {
		b, err := bc.Get(blk)
		if err!=nil {
			return 0, err
		}

		sz = uint64(len(b.Data))
		bc.Release(b)
	} else if blk==0 {
		sz = 0
	}
 
//	debug("prevSize blk %d\n", blk)
	return int(sz), nil
}

func (bp *BTPath) isStartOfFile(level int) bool {
	for level++; level < bp.levels; level++ {
//		debug("isStartOfFile level %d pos %d start %d end %d\n", level, bp.entries[level].pos, bp.entries[level].start, bp.entries[level].end)
		if bp.entries[level].pos != 0 {
			return false
		}
	}

	return true
}

func (bp *BTPath) isEndOfFile(level int) bool {
	if level==0 {
//		debug("isEndOfFile level %d pos %d start %d end %d\n", level, bp.entries[level].pos, bp.entries[level].start, bp.entries[level].end)
		if bp.entries[0].pos < bp.entries[0].end - bp.entries[0].start {
			return false
		}
	}

	for level++; level < bp.levels; level++ {
//		debug("isEndOfFile level %d pos %d start %d end %d\n", level, bp.entries[level].pos, bp.entries[level].start, bp.entries[level].end)
		if bp.entries[level].pos < bp.entries[level].end {
			return false
		}
	}

	return true
}

func (bp *BTPath) clone() *BTPath {
	nbp := new(BTPath)
	*nbp = *bp
	for i:= 0; i<bp.levels; i++ {
		e := &bp.entries[i]
		if e.Block!=nil {
			bc.Ref(e.Block)
		}
	}

	return nbp
}

func (bp *BTPath) seek(offset uint64) error {
	var l int
	var end uint64

	e := &bp.entries[0]
	if e.start<offset && e.end>offset {
		e.pos = offset - e.start
		return nil
	}

	for l=1; l < bp.levels; l++ {
		e = &bp.entries[l]
		if e.Block == nil {
			continue
		}

		if e.start<offset && e.end>offset {
			break
		}
	}

//	debug("BTPath.seek l %v bp.levels %v\n", l, bp.levels)
	base := uint64(0)
	if l < bp.levels {
		base = bp.entries[l].start
	}

	b := bp.entries[l-1].Block
	for l--; l >= 0; l-- {
		var pos int

		e = &bp.entries[l]
		e.Block = b
		if l==0 {
//			debug("BTPath.seek l 0 start %d pos %d\n", e.start, e.pos)
			if bp.levels>0 {
				e.start = bp.entries[l+1].start
				e.end = bp.entries[l+1].end
			} else {
				e.start = 0
				if e.Block != nil {
					e.end = e.start + uint64(e.Length)
				} else {
					e.end = 0
				}
			}

			e.pos = offset - e.start
//			if e.Block!=nil {
//				if e.pos > uint64(len(e.Data)) {
//					debug("######## pos %d len %d start %d end %d offset %d\n", e.pos, len(e.Data), e.start, e.end, offset)
//				}
//			} else {
//				debug("######## hole pos %d start %d end %d offset %d\n", e.pos, e.start, e.end, offset)
//			}

			break
		}

		if b==nil {
			e.pos = 0
			e.start = bp.entries[l+1].start
			continue
		}

		e.entnum = len(b.Data) / BTEntrySize
		pos, base, end = search(base, offset, e.Data)
//		debug("BTPath.seek pos %d base %d end %d\n", pos, base, end)
		e.pos = uint64(pos)
		e.start = base
		e.end = end
		if pos < e.entnum {
			b.RLock()
			blk, _ := gint64(b.Data[pos*BTEntrySize:])
			b.RUnlock()
			if blk!=0 {
				var err error

//				debug("BTPath.seek: %d get %d\n", b.Offset, blk)
				b, err = bc.Get(blk)
				if err!=nil {
					return err
				}
			} else {
				b = nil
			}
		} else {
			b = nil
		}

	}

	return nil
}

func (bp *BTPath) Release() {
	for i:=0; i<bp.levels; i++ {
		bc.Release(bp.entries[i].Block)
		bp.entries[i].Block = nil
	}
}

// returns the total size of the block.
// if level==0 returns the length of the block
// if level > 0 reads the sizes of all blocks and returns the total
func blockSize(b *Block) uint64 {
	if b.Level() == 0 {
		return uint64(len(b.Data))
	}

	total := uint64(0)
	b.RLock()
	for p:=b.Data; len(p) >= BTEntrySize; {
		var sz uint64

//		debug("blockSize: len(p) %d\n", len(p))
		p = p[8:]		// skip block address
		sz, p = gint64(p)
		total += sz
	}
	b.RUnlock()

//	debug("blockSize: %d %d %v\n", b.Offset, total, b.Data)
	return total
}

// copy data from the multiple []byte slices to a list of blocks of type ctype
func copyToBlocks(ds [][]byte, bs []*Block, datasz int, ctype uint16, maxblksz int) []*Block {
//	debug("copyToBlocks ds %v datasz %d\n", ds, datasz)
	bnum := datasz / maxblksz
	if datasz%maxblksz != 0 {
		bnum++
	}

	blks := make([]*Block, bnum)
	b := bs[0]
	if b != nil {
		b.RLock()
	}

	for di, bi, dpos, bpos := 0, 0, 0, 0; datasz > 0; {
		if ds[di] == nil || len(ds[di]) == 0 {
			if b != nil {
				b.RUnlock()
				b = nil
			}

			di++
			if di < len(bs) {
				b = bs[di]
				if b != nil {
					b.RLock()
				}
			} else {
				b = nil
			}

			continue
		}

		if blks[bi] == nil {
			sz := datasz
			if sz > maxblksz {
				sz = maxblksz
			}

			blks[bi] = bc.NewBlock(ctype, false, uint16(sz))
			bpos = 0
		}

		m := copy(blks[bi].Data[bpos:], ds[di][dpos:])
		bpos += m

		if b!=nil && b.coffset < blks[bi].coffset {
			blks[bi].coffset = b.coffset
		}

		if len(blks[bi].Data) <= bpos {
			bi++
		}

		dpos += m
		if len(ds[di]) <= dpos {
			if b != nil {
				b.RUnlock()
			}

			di++
			if di < len(bs) {
				b = bs[di]
				if b != nil {
					b.RLock()
				}
			} else {
				b = nil
			}

			dpos = 0
		}

		datasz -= m
	}

	if b != nil {
		b.RUnlock()
	}

	return blks
}

// Replace the content of (@start, @end) with region @count bytes long, with content @data
// The function creates a file with the changes and returns its ID. The id is subject to change.
// In order to get the final ID, the Commit function should be called.
// The Write function adds the newly created ID to the list of live files. When the file
// is no longer deemed as live, it should be removed using SetLive(..., false) function.
// If the file's ID is stored in another file, Discard won't affect its storage to the disk.
//
// Examples:
// 1) insert 100 bytes at offset 1000
//	write(, 1000, 1000, 100, data)	// len(data) == 100
// 2) insert 100 byte hole at offset 1000
//	write(, 1000, 1000, 100, nil)
// 3) delete 100 bytes at offset 1000
//	write(, 1000, 1100, 0, nil)
// 4) rewrite 100 bytes at offset 1000
//	write(, 1000, 1100, 100, data)	// len(data) == 100
//
func Write(root, start, end uint64, count int, data []byte, ctype uint16) (uint64, error) {
	var l int
	var newblocks []*Block
	var rootblk *Block


//	debug("Write root %v start %v end %v count %d\n", root, start, end, count)
	sbp, err := find(root, start)
	if err != nil {
		return 0, err
	}

	ebp := sbp.clone()
	err = ebp.seek(end)
	if err != nil {
		sbp.Release()
		ebp.Release()
		return 0, err
	}

//	debug("** sbp %v\n", sbp)
//	debug("** ebp %v\n", ebp)
	datalen := 0
	if data!=nil {
		datalen = len(data)
	}

	if count < datalen {
		datalen = count
	}

	rholesz := uint64(count - datalen)
	lholesz := uint64(0)
	if sbp.entries[0].end < start {
		// if the start is past end of file, put hole on the left
//		debug("Write: past end of file: start %d end %d start %d\n", sbp.entries[0].start, sbp.entries[0].end, start)
		lholesz = start - sbp.entries[0].end
		start = sbp.entries[0].end
	}

	if sbp.entries[0].Block == nil {
		// if the start is within a hole, recalculate the hole size
//		debug("Write: start within a hole start %d pos %d\n", sbp.entries[0].start, sbp.entries[0].pos)
		lholesz += start - sbp.entries[0].start
		sbp.entries[0].pos = 0
	}

	if ebp.entries[0].Block == nil && ebp.entries[0].end > end {
		// if the end is within a hole, recalculate the hole size
//		debug("Write: end within a hole end %d pos %d\n", ebp.entries[0].end, ebp.entries[0].pos)
		rholesz += ebp.entries[0].end - end
		ebp.entries[0].pos = ebp.entries[0].end - ebp.entries[0].start
	}

	// if we are inserting a hole, merge lhole, hole and rhole
//	debug("first lholesz %v rholesz %v\n", lholesz, rholesz)
	if data==nil {
		lholesz += rholesz
		count = 0
		rholesz = 0
	}

//	debug("second lholesz %v rholesz %v\n", lholesz, rholesz)
	maxblksz := Conf.Maxdatablocksz
	for l = 0; l < BTMaxLevels; l++ {
//		debug("Level %d -----------\n", l)
//		debug("sbp %v\n", sbp)
//		debug("ebp %v\n", ebp)
		se := &sbp.entries[l]
		ee := &ebp.entries[l]

		// calculate the total size of the data that will need new entries
		// this includes:
		//	- 0 - data block that is just before the currently overwritten block (if it is too small)
		//	- 1 - data from the block partially overwritten by the new data at the beginning
		//	- 2 - new data
		//	- 3 - data from the block partially overwritten by the new data at the end
		//	- 4 - data block that is just after the currently overwritten block (if it is too small and len(data) == count)
		datasz := 0
		ds := make([][]byte, 5)	// prev-block, left-overwritten, data, right-overwritten, next-block
		bs := make([]*Block, 5)
		ds[2] = data
		if data != nil {
			datasz = len(data)
		}

		lb := (*Block)(nil)
		lbuf := []byte(nil)
		if se.Block != nil && se.pos > 0 {
//			debug("partially overwritten at the beginning: level %d len(e.Data) %d e.start %v e.end %v pos %v\n", l, len(se.Data), se.start, se.end, se.pos)
			lb = se.Block
			if l==0 {
				lbuf = se.Data[0:se.pos]
			} else if se.pos > 0 {
				lbuf = se.Data[0:se.pos*BTEntrySize]
			}
		}
//		debug("lbuf %v\n", lbuf)

		// if no hole on the left, combine the left fragment with the data
		if lholesz==0 && se.Block!=nil && ctype==se.Type() {
			ds[1] = lbuf
			bs[1] = lb
			lbuf = nil
			lb = nil
			datasz += len(ds[1])
		}

		rb := (*Block)(nil)
		rbuf := []byte(nil)
		if ee.Block != nil {
//			debug("partially overwritten at the end: len(e.Data) %d e.start %v e.end %v end %v pos %v\n", len(ee.Data), ee.start, ee.end, end, ee.pos)
			rb = ee.Block
			if l==0 {
				if ee.pos < uint64(len(ee.Data)) {
					rbuf = ee.Data[ee.pos:]
				}
			} else {
				pos := (ee.pos + 1) * BTEntrySize
				if pos < uint64(len(ee.Data)) {
					rbuf = ee.Data[pos:]
				}
			}
		}
//		debug("rbuf %v\n", rbuf)

		// if no hole on the right, combine the right fragment with the data
//		if ee.Block!=nil {
//			debug("level %d rholesz %d ee.Block %p ctype %x ee.Type %x\n", l, rholesz, ee.Block, ctype, ee.Type())
//		} else {
//			debug("level %d rholesz %d ee.Block %p\n", l, rholesz, ee.Block)
//		}
		if rholesz == 0 && ee.Block!=nil && ctype==ee.Type() {
			ds[3] = rbuf
			bs[3] = rb
			rbuf = nil
			rb = nil
			datasz += len(ds[3])
		}

		if lholesz==0 {
			ssz, err := sbp.prevSize(l)
			if err!=nil {
				return 0, err
			}

//			debug("prevSize: %d\n", ssz)
			if ssz>0 && ssz<maxblksz {
				sbp.prev(l)
				if l==0 && sbp.entries[l].Block!=nil && sbp.entries[l].Type()!=ctype {
					// different content type, can't merge
//					debug("can't merge with prev\n")
					sbp.next(l)
				} else {
					ds[0] = sbp.entries[l].Data
					bs[0] = sbp.entries[l].Block
					datasz += len(ds[0])
					start = sbp.entries[l].start
				}
				se = &sbp.entries[l]
			}
		}

		if rholesz==0 {
			esz, err := ebp.nextSize(l)
			if err!=nil {
				return 0, err
			}

//			debug("nextSize: %d\n", esz)
			if esz>0 && esz<maxblksz {
				ebp.next(l)
				if l==0 && ebp.entries[l].Type()!=ctype {
					// different content type, can't merge
//					debug("can't merge with next\n")
					ebp.prev(l)
				} else {
					ds[4] = ebp.entries[l].Data
					bs[4] = ebp.entries[l].Block
					datasz += len(ds[4])
					end = ebp.entries[l].start + uint64(len(ds[4]))
				}
				ee = &ebp.entries[l]
			}
		}

//		debug("Write: ds %v\n", ds)
		blks := copyToBlocks(ds, bs, datasz, ctype, maxblksz)
		bnum := len(blks)
		lblk := (*Block)(nil)
		if lbuf!=nil {
			lblk = bc.NewBlock(se.Type(), false, uint16(len(lbuf)))
			lb.RLock()
			copy(lblk.Data, lbuf)
			lb.RUnlock()
			bnum++
//			debug("lblk %p\n", lblk)
		}

		if lholesz>0 {
			bnum++
		}

		if rholesz>0 {
			bnum++
		}

		rblk := (*Block)(nil)
		if rbuf!=nil {
			rblk = bc.NewBlock(ee.Type(), false, uint16(len(rbuf)))
			rb.RLock()
			copy(rblk.Data, rbuf)
			rb.RUnlock()
			bnum++
//			debug("rblk %p %v\n", rblk, rblk.Data)
		}

		sof := sbp.isStartOfFile(l)
		eof := ebp.isEndOfFile(l)
//		debug("bnum %d sof %v eof %v\n", bnum, sof, eof)
		if bnum==1 && sof && eof && len(blks) == 1 {
			rootblk = blks[0]
			break
		} else if bnum==0 && sof && eof {
			return 0, nil
		}

		data = make([]byte, bnum * BTEntrySize)
		p := data
		if lblk != nil {
			// left unmerged data
			newblocks = append(newblocks, lblk)
//			debug("Store block %v type %x %v\n", lblk.Offset, lblk.Type(), lblk.Data)
			p = pint64(lblk.Offset, p)
			p = pint64(blockSize(lblk), p)
		}

		if lholesz > 0 {
			// left hole
			p = pint64(0, p)
			p = pint64(lholesz, p)
			lholesz = 0
		}

		newblocks = append(newblocks, blks...)
		for i:=0; i < len(blks); i++ {
//			debug("Store block %v type %x %v\n", blks[i].Offset, blks[i].Type(), blks[i].Data)
			p = pint64(blks[i].Offset, p)
			p = pint64(blockSize(blks[i]), p)
		}

		if rholesz > 0 {
			// right hole
			p = pint64(0, p)
			p = pint64(rholesz, p)
			rholesz = 0
		}

		if rblk != nil {
			// left unmerged data
			newblocks = append(newblocks, rblk)
//			debug("Store block %v type %x %v\n", rblk.Offset, rblk.Type(), rblk.Data)
			p = pint64(rblk.Offset, p)
			p = pint64(blockSize(rblk), p)
		}

		maxblksz = Conf.Maxblocksz
		ctype = BTNode1 + uint16(l)
	}

	// now store all the blocks...
	if rootblk == nil {
		return 0, errors.New("file is too big")
	}

	rootblk.SetRoot(true)
	newblocks = append(newblocks, rootblk)
//	debug("Store root block %v %p %v\n", rootblk.Offset, rootblk.Data, rootblk.Data)
	err = bc.StoreBlocks(newblocks)
	return rootblk.Offset, err
}

func Read(root, offset uint64, data []byte) (int, error, uint16) {
//	debug("Read root %v offset %v count %v\n", root, offset, len(data))
	bp, err := find(root, offset)
	if err!=nil || bp.isEndOfFile(0) {
		return 0, err, BTHole
	}

	count := 0
	e := &bp.entries[0]
	ctype := uint16(BTHole)
	if e.Block != nil {
		ctype = e.Type()
	}

	for len(data) > 0 {
//		debug("Read: entry start %v end %v pos %v b %p\n", e.start, e.end, e.pos, e.Block)
		n := len(data)
		if n > int(e.end - e.start - uint64(e.pos)) {
			n = int(e.end - e.start - uint64(e.pos))
		}

		if n>0 {
			if e.Block==nil {
//				debug("Hole for %d bytes\n", n)
				for i:=0; i < n; i++ {
					data[i] = 0
				}
				ctype = BTHole
			} else {
//				debug("Data for %d bytes start %d end %d pos %d len %d\n", n, e.start, e.end, e.pos, len(e.Data))
				if int(e.pos)+n > len(e.Data) {
					return 0, errors.New(fmt.Sprintf("short block offset %d pos %d n %d len %d start %d\n", e.Offset, e.pos, n, len(e.Data), e.start)), BTHole
				}

				copy(data, e.Data[e.pos:e.pos+uint64(n)])
			}

			count += n
			if n==len(data) {
				break
			}

			data = data[n:]
		}

		moved, err := bp.next(0)
		if err!=nil {
			return 0, err, BTUnknown
		}

		if !moved {
			break
		}

		if n==0 {
			continue
		}

		if e.Block==nil {
			if ctype != BTHole {
				break
			}
		} else if (e.Type() != ctype) {
			break
		}
	}

//	debug("Read: got %d bytes type %x\n", count, ctype)
	return count, nil, ctype
}

func Dump(root uint64, showlevels bool) (string, error) {
	s := fmt.Sprintf("Dump file %d", root)
	bp, err := find(root, 0)
	if err!=nil {
		s = fmt.Sprintf("%s\n\tError: %s\n", s, err)
		return s, err
	}

	if showlevels {
		s = fmt.Sprintf("%s levels %d:\nLevel 0\n", s, bp.levels)
	} else {
		s = fmt.Sprintf("%s:\n", s)
	}

	offset := uint64(0)
	for {
		e := &bp.entries[0]
		n := e.end - e.start - uint64(e.pos)
		if e.Block == nil {
			s = fmt.Sprintf("%s\t%06d hole %d\n", s, offset, n)
		} else {
			s = fmt.Sprintf("%s\t%06d data %d blk %d\n", s, offset, n, e.Offset)
		}

		offset += n
		moved, err := bp.next(0)
		if err!=nil {
			s = fmt.Sprintf("%s\tError: %s\n", s, err)
			return s, err
		}

		if !moved {
			break
		}
	}

	s = fmt.Sprintf("%s\t%06d EOF\n", s, offset)

	if !showlevels {
		return s, nil
	}

	lvl := bp.levels
	for l := 1; l < lvl; l++ {
		bp, err := find(root, 0)
		if err!=nil {
			s = fmt.Sprintf("%s\tError: %s\n", s, err)
			return s, err
		}
			
		e := &bp.entries[l]
		s = fmt.Sprintf("%sLevel %d\n", s, l)
		for {
			s = fmt.Sprintf("%s\t%06d %d\n", s, e.start, len(e.Data))
			moved, err := bp.next(l)
			if err!=nil {
				s = fmt.Sprintf("%s\tError: %s\n", s, err)
				return s, err
			}

			if !moved {
				break
			}
		}
	}

	return s, nil
}

type btdata int
type btnode int

func (btdata) Id() uint16 {
	return BTData
}

func (btdata) Files([]byte) []uint64 {
	return nil
}

func (btdata) Commit([]byte) bool {
	return true
}

func (bn btnode) Id() uint16 {
	return uint16(bn)
}

func (btnode) Files(data []byte) []uint64 {
	bnum := len(data) / BTEntrySize
	blks := make([]uint64, bnum)
	for i :=0; i < bnum; i++ {
		blks[i], _ = gint64(data[i*BTEntrySize:(i+1)*BTEntrySize])
	}

	return blks
}

func (btnode) Commit(data []byte) bool {
	n := len(data) / BTEntrySize
	for i := 0; i < n; i++ {
		ent := data[i*BTEntrySize:(i+1)*BTEntrySize]
		bid, _ := gint64(ent)
		if (bid%2) == 1 {
			cb, err := bc.Get(bid)
			if err!=nil {
//				debug("error while committing: %v\n", err)
				return false
			}

			if !cb.IsCommitted() {
				return false
			}

//			debug("btnode.Commit replace %d with %d\n", bid, cb.Offset)
			pint64(cb.Offset, ent)
		}
	}

	return true
}

var bdata btdata
var bnodes [7]btnode

func initBtree() {
	bc.RegisterDataType(bdata)
	for i:=0; i<len(bnodes); i++ {
		bnodes[i] = btnode(0x28b1+i)
		bc.RegisterDataType(bnodes[i])
	}
}

func (bp *BTPath) String() string {
	s := ""
	for l := 0; l<bp.levels; l++ {
		e := &bp.entries[l]
		s = fmt.Sprintf("%s(l%d p%d s%d e%d n%d b%p) ", s, l, e.pos, e.start, e.end, e.entnum, e.Block)
	}

	return s
}
