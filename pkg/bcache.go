package gostor

import (
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
	BTUnknown	= 0x7FFF	// unknown type

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
	blktype		uint16	// block type
	Length		uint16
	Data		[]byte
	raw		[]byte	// raw data, including the header
	ucid		uint64	// uncommitted id, if any

	// uncommitted blocks only
	bt		BlockType
	ucprev, ucnext	*Block	// list of uncommitted blocks
	coffset	uint64		// BlockCache.coffset at the time the block is stored
	blkref		[]uint64// list of IDs of blocks referenced by the block
	color		int	// for garbage collection

	// block cache stuff
	lru, mru	*Block
}

type BlockCache struct {
	sync.Mutex
	err		os.Error
	dc		*DiskCache
	sm		*SegmentManager
	maxcount	uint32
	maxucsize	uint32
	cache		map[uint64] *Block
	lru, mru	*Block
	blktypes	map[uint16] BlockType

	// uncommitted blocks
	ucoffset	uint64	// next odd integer to use as id for uncommitted blocks
	coffset		uint64	// (fake/odd) offset of the last uncommitted block that was committed
	ucfirst, uclast	*Block
	ucsize		uint32	// size of the uncommitted data
	liveblocks	map[uint64] *Block
	lastcommit	int64	// time of last commit
	commitchan	chan chan bool
}

type BlockType interface {
	Id() uint16
	Blocks(b *Block) []uint64
	Commit(b *Block) bool
}

type StrError string

func (s StrError) String() string {
	return string(s)
}

func NewBlockCache(dc *DiskCache, disk *os.File, maxcount uint32) (*BlockCache, os.Error) {
	var err os.Error

	bc := new(BlockCache)
	bc.dc = dc
	bc.maxcount = maxcount
	bc.cache = make(map[uint64] *Block)
	bc.ucoffset = 1	// should be odd!
	bc.liveblocks = make(map[uint64] *Block)
	bc.maxucsize = MaxNoncommitSize
	bc.commitchan = make(chan chan bool)
	bc.blktypes = make(map[uint16] BlockType)

	bc.sm, err = NewSegmentManager(dc, disk)
	if err!=nil {
		return nil, err
	}

	go bc.commitproc()
	return bc, nil
}

func (bc *BlockCache) commitproc() {
	var c chan bool

	tick := time.Tick(MaxCommitTime)
	for {
		c = nil
		select {
		case _ = <- tick:
		case c = <- bc.commitchan:
			err :=bc.commit()
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
}

func (bc *BlockCache) RegisterBlockType(bt BlockType) bool {
	id := bt.Id()
	if id >= BTRoot {
		return false
	}

	bc.Lock()
	defer bc.Unlock()
	if bc.blktypes[id]!=nil {
		return false
	}

	bc.blktypes[id] = bt
	return true
}

// reads a block from the disk and parses the header
func (bc *BlockCache) readBlock(offset uint64, b *Block) os.Error {
	b.Offset = offset
	hdr, err := bc.dc.Read(offset, BTHeaderSize)
	if err!=nil {
		return err
	}

//	fmt.Printf("BlockCache.readBlock: offset %v header %v\n", offset, hdr)
	magic, p := gint32(hdr)
	if magic != BTMagic {
		return StrError("invalid block header")
	}

	b.Length, p = gint16(p)
	b.blktype, p = gint16(p)
//	fmt.Printf("BlockCache.readBlock: offset %v length %v type %v\n", offset, b.Length, b.blktype)
	b.raw, err = bc.dc.Read(offset, uint32(b.Length) + BTHeaderSize)
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
		bc.cache[db.Offset] = nil, false
		if db.mru != nil {
			db.mru.lru = nil
		} else {
			db.mru = nil
		}

		db.lru = db.mru
	}
}

func (bc *BlockCache) Get(offset uint64) (*Block, os.Error) {
	var err os.Error

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
			err = StrError(fmt.Sprintf("uncommitted block %d not found", offset))
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
func (bc *BlockCache) Store(b *Block, live bool, blkref []uint64) os.Error {
	p := pint32(BTMagic, b.raw)
	p = pint16(b.Length, p)
	p = pint16(b.blktype, p)

	bc.Lock()
	if bc.err != nil {
		bc.Unlock()
		return bc.err
	}

	b.Offset = bc.ucoffset
	bc.ucoffset += 2
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

	bc.checkCommit()
	bc.Unlock()

	return nil
}

func (bc *BlockCache) Commit(b *Block) os.Error {
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
		bc.commitchan <- nil
	}
}

// called with bc lock held
func (bc *BlockCache) gc() {
	// mark
	for _, b := range bc.liveblocks {
//		fmt.Printf("BlockCache.gc live block %p\n", b)
		bc.mark(b)
	}

	// sweep
	for b := bc.ucfirst; b != nil; {
		bnext := b.ucnext
		if b.color == CBlack {
			// not referenced

			// remove from the list of uncommitted blocks
			if b.ucnext!=nil {
				b.ucnext.ucprev = b.ucprev
			} else {
				bc.uclast = b
			}

			if b.ucprev!=nil {
				b.ucprev.ucnext = bnext
			} else {
				bc.ucfirst = bnext
			}

			// remove from the cache
			bc.cache[b.Offset] = nil, false

			bc.ucsize -= uint32(b.Length + BTHeaderSize)
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
		b.bt = bc.blktypes[btype]
		if b.blkref==nil {
			b.blkref = b.bt.Blocks(b)
		}
	}

	if b.blkref==nil {
		return
	}

	for bs, i := b.blkref, 0; i< len(bs); i++ {
		if bs[i]%2 == 1 {
			// mark only uncommitted blocks
			bc.mark(bc.cache[bs[i]])
		}
	}
}

func (bc *BlockCache) commit() os.Error {
	var b *Block

	bc.Lock()
	bc.gc()
	if bc.ucsize==0 {
		bc.Unlock()
		return nil
	}

	frag, err := bc.sm.AllocFragment(uint32(bc.ucfirst.Length + BTHeaderSize), bc.ucsize)
	if err!=nil {
		bc.Unlock()
		return err
	}

//	fmt.Printf("BlockCache.commit frag size %v len(Data) %v\n", frag.size, len(frag.Data))
	offset := frag.DataOffset
	count := uint32(len(frag.Data))
	c0 := bc.coffset
	c1 := c0
	n := uint32(0)
	sb := bc.ucfirst
	for b = sb; b!=nil && n<count ; b = b.ucnext {
		m := uint32(b.Length)
		m += m%2
		if n+m > count {
			break
		}

//		fmt.Printf("BlockCache.commit block %p offset %v type %v length %v\n", b, offset, b.blktype, b.Length)
		bc.coffset = b.Offset
		b.ucid = b.Offset
		b.Offset = offset
		bc.cache[b.Offset] = b
		offset += uint64(m) + BTHeaderSize
		n += m + BTHeaderSize
		c0 = b.coffset

		// add to the LRU list
		bc.makeSpace()
		b.lru = bc.mru
		bc.mru = b
		if bc.lru == nil {
			bc.lru = b
		}
	}

	if b!=nil {
		bc.ucfirst = b.ucnext
	} else {
		bc.ucfirst = nil
	}

	if bc.ucfirst == nil {
		bc.uclast = nil
	}
//	fmt.Printf("BlockCache.commit ucsize %v n %v\n", bc.ucsize, n)
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
			bb.bt.Commit(bb)
		}

		m := bb.Length
		m += m%2
		copy(p, bb.raw[0:bb.Length + BTHeaderSize])
//		fmt.Printf("BlockCache.commit offset %v data %v\n", bb.Offset, bb.raw[0:bb.Length+BTHeaderSize])
//		fmt.Printf("BlockCache.commit offset %v block %p count %v remaining %v\n", bb.Offset, bb, m+BTHeaderSize, len(p))
		p = p[m+BTHeaderSize:]
	}

	// remove uncommitted blocks with odd offsets from the cache
	bc.Lock()
	for c0 < c1 {
		if bc.liveblocks[c0]!=nil {
			bc.cache[c0] = nil, false
		}

		c0 += 2
	}
	bc.Unlock()

	return bc.sm.CommitFragment(frag, n)
}

func (bc *BlockCache) NewBlock(blktype uint16, root bool, length uint16) *Block {
	if bc.blktypes[blktype] == nil {
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
	b.Offset = 0xFFFFFFFFFFFFFFFF
	return b
}

func (bc *BlockCache) AddLiveBlock(b *Block) {
	bc.Lock()
//	fmt.Printf("BlockCache.AddLiveBlock %p %v\n", b, b.Offset)
	bc.liveblocks[b.Offset] = b
	bc.Unlock()
}

func (bc *BlockCache) RemoveLiveBlock(b *Block) {
	bc.Lock()
	bc.liveblocks[b.Offset] = nil, false
	if b.ucid!=0 {
		bc.liveblocks[b.ucid] = nil, false
		bc.cache[b.ucid] = nil, false
	}
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

func (b *Block) IsStored() bool {
	return b.Offset != 0xFFFFFFFFFFFFFFFF
}

func (b *Block) IsCommitted() bool {
	return b.Offset%2 == 0
}
