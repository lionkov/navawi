package gostor

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

const (
	BTMagic		= 0x3d86bfe0		// standard block
	BTHeaderSize	= 16
)

const (	// Predefined Block Types
	BTRoot		= 0x8000
	BTData		= 0x0000	// data, no block references
	BTUnknown	= 0x7F7F	// unknown type
	BTHole		= 0x7FFF	// not saved on disk

	// b-tree nodes
	BTNode1		= 0x28b1	// level 1 (children are leafs)
	BTNode2		= 0x28b2	// level 2 (chldren are level 1)
	BTNode3		= 0x28b3	// level 3
	BTNode4		= 0x28b4	// level 4
	BTNode5		= 0x28b5	// level 5
	BTNode6		= 0x28b6	// level 6
	BTNode7		= 0x28b7	// level 7
)

const (
	MaxNoncommitSize = 10*1024*1024	// 10MB
//	MaxCommitTime = 3000000000	// 3s
	MaxCommitTime = 30000000000	// 30s
)

const (
	CBlack	= iota
	CWhite
)

type Block struct {
	Offset		uint64	// block offset, if odd not committed to disk

	// Data Layout on disk
//	magic		uint32
	Length		uint16
	blktype		uint16	// block type
	Data		[]byte
	raw		[]byte	// raw data, including the header
	ucid		uint64	// uncommitted id, if any

	// uncommitted blocks only
	bt		DataType
	ucprev, ucnext	*Block	// list of uncommitted blocks
	coffset	uint64		// BlockCache.coffset at the time the block is stored
	blkref		[]uint64// list of IDs of blocks referenced by the block
	color		int	// for garbage collection

	// block cache stuff
	lru, mru	*Block
}

type BlockCache struct {
	sync.Mutex
	err		error
	dc		*DiskCache
	maxcount	uint32
	maxucsize	uint32
	cache		map[uint64] *Block
	lru, mru	*Block
	dtypes		map[uint16] DataType
	nullblk		*Block

	// uncommitted blocks
	ucoffset	uint64	// next odd integer to use as id for uncommitted blocks
	coffset		uint64	// (fake/odd) offset of the last uncommitted block that was committed
	ocoffset	uint64	// oldest fake offset that might be present in an uncommitted block content
	ucfirst, uclast	*Block
	ucsize		uint32	// size of the uncommitted data
	liveblocks	map[uint64] *Block
	lastcommit	int64	// time of last commit
	commitchan	chan chan bool
}

type DataType interface {
	Id() uint16
	Files(data []byte) []uint64
	Commit(data []byte) bool
}

func NewBlockCache(dc *DiskCache, disk *os.File, maxcount uint32) (*BlockCache, error) {
	bc := new(BlockCache)
	bc.dc = dc
	bc.maxcount = maxcount
	bc.cache = make(map[uint64] *Block)
	bc.ucoffset = 1	// should be odd!
	bc.liveblocks = make(map[uint64] *Block)
	bc.maxucsize = MaxNoncommitSize
	bc.commitchan = make(chan chan bool)
	bc.dtypes = make(map[uint16] DataType)

	nblk := new(Block)
	nblk.Offset = 0
	nblk.blktype = BTData
	nblk.Length = 0
	nblk.Data = make([]byte, 0)
	bc.nullblk = nblk

	go bc.commitproc()
	return bc, nil
}

func (bc *BlockCache) commitproc() {
	var c chan bool
	var err error

	tick := time.Tick(MaxCommitTime)
	for {
		c = nil
		select {
		case _ = <- tick:
			err = bc.commit(true)

		case c = <- bc.commitchan:
			err = bc.commit(c!=nil)
		}

		if err != nil {
			bc.Lock()
			if bc.err == nil {
				bc.err = err
			}
			bc.Unlock()
			if c!=nil {
				c<-false
			}
			return
		}

		if c!=nil {
			c<-true
		}
	}		
}

func (bc *BlockCache) RegisterDataType(bt DataType) bool {
	id := bt.Id()
	if id >= BTRoot {
		return false
	}

	bc.Lock()
	defer bc.Unlock()
	if bc.dtypes[id]!=nil {
		return false
	}

	bc.dtypes[id] = bt
	return true
}

func RegisterDataType(bt DataType) bool {
	return bc.RegisterDataType(bt)
}

// reads a block from the disk and parses the header
func (bc *BlockCache) readBlock(offset uint64, b *Block) error {
	debug("BlockCache.readBlock offset %d\n", offset)
	b.Offset = offset
	hdr, err := bc.dc.Read(offset, BTHeaderSize)
	if err!=nil {
		return err
	}

//	debug("BlockCache.readBlock: offset %v header %v\n", offset, hdr)
	magic, p := gint32(hdr)
	if magic != BTMagic {
//		var str *string
//		*str = "boo"
		return errors.New(fmt.Sprintf("invalid block header: offset %d", offset))
	}

	b.Length, p = gint16(p)
	b.blktype, p = gint16(p)
	debug("BlockCache.readBlock: offset %v length %v type %v\n", offset, b.Length, b.blktype)
	b.raw, err = bc.dc.ReadAll(offset, uint32(b.Length) + BTHeaderSize)
	if err!=nil {
		return err
	}

	b.Data = b.raw[BTHeaderSize:]
	return nil
}

// make space for a new block in the cache
// called with bc lock held
func (bc *BlockCache) makeSpace() {
	if uint32(len(bc.cache)) >= bc.maxcount {
		db := bc.lru
		delete(bc.cache, db.Offset)
		if db.mru != nil {
			db.mru.lru = nil
		} else {
			db.mru = nil
		}

		db.lru = db.mru
	}
}

func (bc *BlockCache) Get(offset uint64) (*Block, error) {
	var err error

	if offset==0 {
		return bc.nullblk, nil
	}

	err = nil
	bc.Lock()
	if bc.err != nil {
		bc.Unlock()
		return nil, bc.err
	}

	b := bc.cache[offset]
	if offset%2 == 1 {
		// uncommitted blocks are handled differently
		// 1. no reading from the disk
		// 2. not in the lru list
		if b==nil {
			debug("**** uncommitted block %d not found\n", offset)
			err = errors.New(fmt.Sprintf("uncommitted block %d not found", offset))

			var s *string
			*s = "boo"
		}

		bc.Unlock()
		return b, err
	}

	if b!=nil {
		// move to the top of the LRU list
		if b.lru!=nil {
			b.lru.mru = b.mru
		} else {
			bc.lru = b.mru
		}

		if b.mru!=nil {
			b.mru.lru = b.lru
		} else {
			bc.mru = b.lru
		}

		b.lru = bc.mru
		b.mru = nil
		bc.mru = b
	}
	bc.Unlock()

	if b == nil {
		b = new(Block)
		err = bc.readBlock(offset, b)
		if err!=nil {
			return nil, err
		}

		bc.Lock()
		bc.makeSpace()
		b.lru = bc.mru
		b.mru = nil
		bc.mru = b
		bc.cache[offset] = b
		bc.Unlock()
	} else if b.Data == nil {
		// the block is in the cache, but hasn't been read from the disk
		// nothing bad should happen even if the block is read simultaneously
		// from more than one goroutine
		err = bc.readBlock(offset, b)
		if err!=nil {
			return nil, err
		}

		bc.Lock()
		bc.makeSpace()
		b.lru = bc.mru
		bc.mru = b
		if bc.lru == nil {
			bc.lru = b
		}
		bc.Unlock()
	}

	return b, nil
}

func (bc *BlockCache) Ref(b *Block) {
	// should it do anything ???
}

func (bc *BlockCache) Release(b *Block) {
	// should it do anything ???
}

// if specified, blkref should return the same list that Blocks() call for the
// block type
// called with bc lock held
func (bc *BlockCache) store(b *Block, live bool, blkref []uint64) error {
	p := pint32(BTMagic, b.raw)
	p = pint16(b.Length, p)
	p = pint16(b.blktype, p)

	if bc.err != nil {
		return bc.err
	}

	b.coffset = bc.coffset
	bc.cache[b.Offset] = b
	b.ucprev = bc.uclast
	b.ucnext = nil
	if bc.ucfirst == nil {
		bc.ucfirst = b
	} else {
		bc.uclast.ucnext = b
	}

	bc.uclast = b
	bc.ucsize += uint32(b.Length + b.Length%2) + BTHeaderSize
	b.blkref = blkref
	if live {
		bc.liveblocks[b.Offset] = b
	}

	return nil
}

func (bc *BlockCache) Store(b *Block, live bool, blkref []uint64) error {
	bc.Lock()
	defer bc.Unlock()
	err := bc.store(b, live, blkref)
	bc.checkCommit()
	return err
}

// Store all blocks from the array atomically, making the root ones live
func (bc *BlockCache) StoreBlocks(bs []*Block) error {
	bc.Lock()
	defer bc.Unlock()
	for _, b := range bs {
		err := bc.store(b, b.IsRoot(), nil)
		if err!=nil {
			return err
		}
	}

	bc.checkCommit()
	return nil
}

func (bc *BlockCache) Commit(b *Block) error {
	c := make(chan bool)

	for b==nil || b.Offset%2!=0 {
		bc.commitchan <- c
		ok := <-c
		if !ok {
			return bc.err
		}

		if b==nil {
			break
		}
	}

	return nil
}

// called with bc lock held
func (bc *BlockCache) checkCommit() {
	if bc.ucsize > bc.maxucsize {
		// force commit
		select {
		default:
		case bc.commitchan <- nil:
		}
	}
}

// called with bc lock held
func (bc *BlockCache) gc() {
	// mark
	for _, b := range bc.liveblocks {
//		debug("BlockCache.gc live block %p\n", b)
		bc.mark(b)
	}

	// sweep
	for b := bc.ucfirst; b != nil; {
		bnext := b.ucnext
		if b.color == CBlack {
			// not referenced

//			debug("remove garbage from cache block %d %p prev %p next %p ucfirst %p uclast %p\n", b.Offset, b, b.ucprev, b.ucnext, bc.ucfirst, bc.uclast)
			// remove from the list of uncommitted blocks
			if b.ucnext!=nil {
				b.ucnext.ucprev = b.ucprev
			} else {
				bc.uclast = b.ucprev
			}

			if b.ucprev!=nil {
				b.ucprev.ucnext = bnext
			} else {
				bc.ucfirst = bnext
			}

			// remove from the cache
			delete(bc.cache, b.Offset)

			bc.ucsize -= uint32(b.Length + b.Length%2 + BTHeaderSize)
		}

		b.color = CBlack
		b = bnext
	}
}

// called with bc lock held
func (bc *BlockCache) mark(b *Block) {
	if b.color == CWhite {
		return
	}

	b.color = CWhite
	btype := b.Type()
	if btype==BTData {
		return
	}

	if b.bt==nil {
		b.bt = bc.dtypes[btype]
	}

	if b.blkref==nil {
//		debug("BlockCache.mark %d\n", b.Offset)
		b.blkref = b.bt.Files(b.Data)
	}

	if b.blkref==nil {
		return
	}

	for bs, i := b.blkref, 0; i< len(bs); i++ {
		if bs[i]%2 == 1 {
			// mark only uncommitted blocks
			bb := bc.cache[bs[i]]
			if bb==nil {
				debug("block with id %d doesn't exist\n", bs[i])
			}
			bc.mark(bc.cache[bs[i]])
		}
	}
}

func (bc *BlockCache) commit(force bool) error {
	var b *Block

	bc.Lock()
	bc.gc()
	if bc.ucsize==0 || (!force && bc.ucsize < bc.maxucsize) {
		bc.Unlock()
		return nil
	}

//	debug("BlockCache.commit ucsize %d ucfirst %p\n", bc.ucsize, bc.ucfirst)
	frag, err := sm.AllocFragment(uint32(bc.ucfirst.Length + BTHeaderSize), bc.ucsize)
	if err!=nil {
		bc.Unlock()
		return err
	}

//	debug("BlockCache.commit frag size %v len(Data) %v\n", frag.size, len(frag.Data))
	offset := frag.DataOffset
	count := uint32(len(frag.Data))
	c1 := bc.ocoffset
	n := uint32(0)
	sb := bc.ucfirst
	for b = sb; b!=nil && n<count ; b = b.ucnext {
		m := uint32(b.Length)
		m += m%2
		if n+m > count {
			break
		}

		debug("BlockCache.commit %d --> %d\n", b.Offset, offset)
		bc.coffset = b.Offset
		b.ucid = b.Offset
		b.Offset = offset
		bc.cache[b.Offset] = b
		offset += uint64(m) + BTHeaderSize
		n += m + BTHeaderSize
		c1 = b.coffset

		// add to the LRU list
		bc.makeSpace()
		b.lru = bc.mru
		bc.mru = b
		if bc.lru == nil {
			bc.lru = b
		}
	}

	if b!=nil {
		b.ucprev = nil
		bc.ucfirst = b
	} else {
		bc.ucfirst = nil
	}

	if bc.ucfirst == nil {
		bc.uclast = nil
	}
//	debug("BlockCache.commit ucsize %v n %v\n", bc.ucsize, n)
	bc.ucsize -= n
	bc.Unlock()

	if n==0 {
		return nil
	}

	// now call the commit callbacks (if any) and copy the data
	buf := frag.Data
	p := buf
	for bb:=sb; bb!=b; bb = bb.ucnext {
		if bb.bt != nil {
			bb.bt.Commit(bb.Data)
		}

		m := bb.Length
		m += m%2
		copy(p, bb.raw[0:bb.Length + BTHeaderSize])
//		debug("BlockCache.commit offset %v data %v\n", bb.Offset, bb.raw[0:bb.Length+BTHeaderSize])
//		debug("BlockCache.commit offset %v block %p count %v remaining %v\n", bb.Offset, bb, m+BTHeaderSize, len(p))
		if int(m+BTHeaderSize) > len(p) {
			fmt.Printf("BlockCache.commit: m+BTHeaderSize %d len(p) %d\n", m+BTHeaderSize, len(p))
		}

		p = p[m+BTHeaderSize:]
	}

	// remove uncommitted blocks with odd offsets from the cache
	bc.Lock()
//	debug("BlockCache.commit remove blocks from %d to %d\n", bc.ocoffset, c1)
	for bc.ocoffset < c1 {
		if bc.liveblocks[bc.ocoffset]==nil {
//			debug("BlockCache.commit remove block %d from cache\n", c0)
			delete(bc.cache, bc.ocoffset)
		}

		bc.ocoffset += 2
	}
	bc.Unlock()

	return sm.CommitFragment(frag, n)
}

func (bc *BlockCache) NewBlock(blktype uint16, root bool, length uint16) *Block {
	if bc.dtypes[blktype] == nil {
		return nil
	}

	b := new(Block)
	b.raw = make([]byte, length + BTHeaderSize)
	b.Length = length
	b.blktype = blktype
	if root {
		b.blktype |= BTRoot
	}

	b.Data = b.raw[BTHeaderSize:]
	bc.Lock()
	b.Offset = bc.ucoffset
	bc.ucoffset += 2
	bc.Unlock()

	return b
}

func (bc *BlockCache) AddLiveBlock(b *Block) {
	bc.Lock()
//	debug("BlockCache.AddLiveBlock %p\n", b)
	bc.liveblocks[b.Offset] = b
	bc.Unlock()
}

func (bc *BlockCache) RemoveLiveBlock(b *Block) {
//	debug("Remove live block %p id %d ucid %d\n", b, b.Offset, b.ucid)
	bc.Lock()
	delete(bc.liveblocks, b.Offset)
	if b.ucid==0 {
		bc.Unlock()
		return
	}

	// check all uncommitted blocks for references to ucid
	bs := make([]*Block, 0, 16)
	for ub := bc.ucfirst; ub != nil; ub = ub.ucnext {
		btype := ub.Type()
		if btype==BTData {
			continue
		}

		if ub.bt==nil {
			ub.bt = bc.dtypes[btype]
		}

		if ub.blkref==nil {
			ub.blkref = ub.bt.Files(ub.Data)
		}

		if ub.blkref==nil {
			continue
		}

		for _, bid := range ub.blkref {
			if bid == b.ucid {
				bs = append(bs, ub)
				break
			}
		}
	}
	b.ucid = 0
	bc.Unlock()

	// is that safe? should be, even if commit is running, it will do the same thing
	for _, ub := range bs {
		// replace with the new ID
		ub.bt.Commit(ub.Data)
	}

	bc.Lock()
	delete(bc.liveblocks, b.ucid)
	delete(bc.cache, b.ucid)
	bc.Unlock()
}

func (b *Block) SetRoot(root bool) {
	if root {
		b.blktype |= BTRoot
	} else {
		b.blktype &= ^uint16(BTRoot)
	}
}

func (b *Block) IsRoot() bool {
	return (b.blktype & BTRoot) != 0
}

func (b *Block) Type() uint16 {
	return b.blktype & ^uint16(BTRoot)
}

func (b *Block) Level() int {
	blktype := b.blktype & 0x7FFF

	if blktype<BTNode1 || blktype>BTNode7 {
		return 0
	}

	return int(blktype - BTNode1) + 1
}

func (b *Block) IsCommitted() bool {
	return b.Offset%2 == 0
}
