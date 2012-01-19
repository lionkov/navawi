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
	MaxNoncommitSize = 64*1024*1024	// 10MB
	MaxCommitTime = 30000000000	// 30s
)

const (
	CBlack	= iota
	CWhite
)

type Block struct {
	sync.RWMutex		// protects Data
	Offset		uint64	// block offset, if odd not committed to disk

	// Data Layout on disk
//	magic		uint32
	Length		uint16
	blktype		uint16	// block type
	Data		[]byte
	raw		[]byte	// raw data, including the header

	// uncommitted blocks only
	bt		DataType
	ucprev, ucnext	*Block	// list of uncommitted blocks
	coffset		uint64	// BlockCache.coffset at the time the block is stored
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
	ucache		map[uint64] uint64	// mapping between uncommitted id that can still be used and the commited one
	lru, mru	*Block
	dtypes		map[uint16] DataType
	nullblk		*Block

	// uncommitted blocks
	ucoffset	uint64	// next odd integer to use as id for uncommitted blocks
	coffset		uint64	// (fake/odd) offset of the last uncommitted block that was committed
	ocoffset	uint64	// oldest fake offset that might be present in an uncommitted block content
	ucfirst, uclast	*Block
	ucsize		uint32	// size of the uncommitted data
	liveblocks	map[uint64] bool
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
	bc.ucache = make(map[uint64] uint64)
	bc.ucoffset = 1	// should be odd!
	bc.coffset = 1
	bc.ocoffset = 1
	bc.liveblocks = make(map[uint64] bool)
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

/*
		bc.Lock()
		n := 0
		for o, _ := range bc.cache {
			if o%2 == 1 {
				n++
			}
		}

		m := 0
		for b := bc.lru; b != nil; b = b.mru {
			m++
		}

		l := 0
		for b := bc.mru; b != nil; b = b.lru {
			l++
		}

		uf := 0
		for b := bc.ucfirst; b != nil; b = b.ucnext {
			uf++
		}
		ul := 0
		for b := bc.uclast; b != nil; b = b.ucprev {
			ul++
		}
		bc.Unlock()

		fmt.Printf("len(cache) %d uncommitted %d len(ucache) %d mrulen %d lrulen %d ufirst %d ulast %d\n", len(bc.cache), n, len(bc.ucache), m, l, uf, ul)
*/
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
		return errors.New(fmt.Sprintf("invalid block header: offset %d %x instead of %x", offset, magic, BTMagic))
	}

	b.Length, p = gint16(p)
	b.blktype, p = gint16(p)
	b.raw, err = bc.dc.ReadAll(offset, uint32(b.Length) + BTHeaderSize)
	if err!=nil {
		return err
	}

	b.Data = b.raw[BTHeaderSize:]
	return nil
}

// called with bc lock held
func (bc *BlockCache) lrucheck() {
	m := make(map[string] *Block)
	for b := bc.mru; b != nil; b = b.lru {
		s := fmt.Sprintf("%p", b)
		if m[s] != nil {
			fmt.Printf("b %p offset %d\n", b, b.Offset)
			var s *string
			*s = "boo"
		}
		m[s] = b
	}
}

// called with bc lock held
func (bc *BlockCache) lruadd(b *Block) {
	b.mru = nil
	b.lru = bc.mru
	if bc.mru!=nil {
		bc.mru.mru = b
	}

	bc.mru = b
	if bc.lru==nil {
		bc.lru = b
	}

//	bc.lrucheck()
}

// called with bc lock held
func (bc *BlockCache) lruremove(b *Block) {
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

	b.lru = nil
	b.mru = nil

//	bc.lrucheck()
}

// called with bc lock held
func (bc *BlockCache) lruupdate(b *Block) {
	bc.lruremove(b)
	bc.lruadd(b)
}

// make space for a new block in the cache
// called with bc lock held
func (bc *BlockCache) makeSpace() {
	if uint32(len(bc.cache)) >= bc.maxcount {
		db := bc.lru
		if db==nil {
			return
		}

		delete(bc.cache, db.Offset)
		bc.lruremove(db)
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

again:
	b := bc.cache[offset]
	if offset%2 == 1 {
		// uncommitted blocks are handled differently
		// 1. can be in the ucache
		// 2. if not, no reading from the disk
		// 3. not in the lru list
		if b==nil {
			o := bc.ucache[offset]
			if o != 0 {
				offset = o
				goto again
			}

			panic(fmt.Sprintf("uncommitted block %d not found", offset))
		}

		bc.Unlock()
		return b, err
	}

	if b!=nil && (b.lru!=nil || bc.lru==b) {
		bc.lruupdate(b)
	}
	bc.Unlock()

	if b == nil {
		b = new(Block)
		err = bc.readBlock(offset, b)
		if err!=nil {
			return nil, err
		}

		bc.Lock()
		b.coffset = bc.coffset
		bc.makeSpace()
		bc.lruadd(b)
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
	}

	return b, nil
}

func (bc *BlockCache) Ref(b *Block) {
	// should it do anything ???
}

func (bc *BlockCache) Unref(b *Block) {
	// should it do anything ???
}

// if specified, blkref should return the same list that Blocks() call for the
// block type
// called with bc lock held
func (bc *BlockCache) store(b *Block, live bool, blkref []uint64) error {
	if b.coffset == 0 {
		panic(fmt.Sprintf("block %d coffset 0\n", b.Offset))
	}

	p := pint32(BTMagic, b.raw)
	p = pint16(b.Length, p)
	p = pint16(b.blktype, p)

	if bc.err != nil {
		return bc.err
	}

//	b.coffset = bc.coffset
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
		bc.liveblocks[b.Offset] = true
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
	for offset, _ := range bc.liveblocks {
		b := bc.cache[offset]
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
	if b.color == CWhite || b.Offset%2 == 0 {
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
		// it is safe to access the data without locking, mark is called in the same thread as commit
		b.blkref = b.bt.Files(b.Data)
	}

	if b.blkref==nil {
		return
	}

	for bs, i := b.blkref, 0; i< len(bs); i++ {
		if bs[i]%2 == 0 {
			continue
		}

		// mark only uncommitted blocks
		bb := bc.cache[bs[i]]
		if bb==nil {
			o := bc.ucache[bs[i]]
			if o==0 {
				panic(fmt.Sprintf("block with id %d doesn't exist b.Offset %d bc.ocoffset %d b.coffset %d\n", bs[i], b.Offset, bc.ocoffset, b.coffset))
			}

			continue
		}
		bc.mark(bb)
	}
}

// called with bc lock held
func (bc *BlockCache) removeUncommitted(c1 uint64) {
	for offset, _ := range bc.ucache {
		if offset < c1 && !bc.liveblocks[offset] {
			delete(bc.ucache, offset)
		}
	}
}

func (bc *BlockCache) commit(force bool) error {
	var b *Block

	bc.Lock()
	bc.gc()
	if bc.ucsize==0 || (!force && bc.ucsize < bc.maxucsize) {
		if bc.ocoffset < bc.coffset {
			c1 := bc.coffset
			for b := bc.ucfirst; b != nil; b = b.ucnext {
				if c1 > b.coffset {
					c1 = b.coffset
				}
			}

			debug("BlockCache.commit: remove blocks from %d to %d len(cache) %d\n", bc.ocoffset, c1, len(bc.cache))
			bc.removeUncommitted(c1)
		}

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
	c1 := bc.coffset
	n := uint32(0)
	sb := bc.ucfirst
	for b = sb; b!=nil && n<count ; b = b.ucnext {
		m := uint32(b.Length)
		m += m%2
		if n+m+BTHeaderSize > count {
			break
		}

		debug("BlockCache.commit %d --> %d (%d)\n", b.Offset, offset, b.Length)
		bc.coffset = b.Offset
		bc.ucache[b.Offset] = offset
		if bc.liveblocks[b.Offset] {
			delete(bc.liveblocks, b.Offset)
			bc.liveblocks[offset] = true
		}

		delete(bc.cache, b.Offset)
		b.Offset = offset
		bc.cache[b.Offset] = b
		offset += uint64(m) + BTHeaderSize
		n += m + BTHeaderSize
		if c1 > b.coffset {
			c1 = b.coffset
		}
	}

	if b!=nil {
		b.ucprev = nil
		bc.ucfirst = b
		b.ucprev = nil
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
			// the only place where the content of the block can change
			bb.Lock()
			bb.bt.Commit(bb.Data)
			bb.coffset = bc.coffset
			bb.Unlock()
		}

		m := bb.Length
		m += m%2
		copy(p, bb.raw[0:bb.Length + BTHeaderSize])
		p = p[m+BTHeaderSize:]
	}

	err = sm.CommitFragment(frag, n)
	if err != nil {
		return err
	}

	// remove uncommitted blocks with odd offsets from the cache
	bc.Lock()

	// first make sure that c1 is the min of the rest of the coffset for the uncommitted blocks
	for b := bc.ucfirst; b != nil; b = b.ucnext {
		if c1 > b.coffset {
			c1 = b.coffset
		}
	}

	debug("BlockCache.commit: remove blocks from %d to %d len(cache) %d\n", bc.ocoffset, c1, len(bc.cache))
	bc.removeUncommitted(c1)
	bc.ocoffset = c1
	for bb, bbnext :=sb, sb.ucnext; bb!=b; bb = bbnext {
		bbnext = bb.ucnext

		// add to the LRU list
		bb.blkref = nil
		bc.makeSpace()
		bc.lruadd(bb)
		bb.ucprev = nil
		bb.ucnext = nil
	}
	bc.Unlock()

	return nil
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
	b.coffset = bc.coffset
	if bc.coffset==0 {
		panic("bc.coffset == 0")
	}

	bc.Lock()
	b.Offset = bc.ucoffset
	bc.ucoffset += 2
	bc.Unlock()

	return b
}

func (bc *BlockCache) AddLiveBlock(offset uint64) {
	bc.Lock()
//	debug("BlockCache.AddLiveBlock %p\n", b)
	bc.liveblocks[offset] = true
	bc.Unlock()
}

func (bc *BlockCache) RemoveLiveBlock(offset uint64) {
//	debug("Remove live block %p id %d ucid %d\n", b, b.Offset, b.ucid)
	bc.Lock()
	delete(bc.liveblocks, offset)
	delete(bc.ucache, offset)
	bc.Unlock()
}

func (bc *BlockCache) Stats() string {
	bc.Lock()
	s := fmt.Sprintf("cache entries: %d\n", len(bc.cache))
	n := 0
	for offset, _ := range(bc.cache) {
		if offset%2==0 {
			n++
		}
	}
	s = fmt.Sprintf("%scommitted entries: %d\n", s, n)

	n = 0
	for b := bc.mru; b != nil; b = b.lru {
		n++
	}
	s = fmt.Sprintf("%sMRU list size: %d\n", s, n)

	n = 0
	for b := bc.lru; b != nil; b = b.mru {
		n++
	}
	s = fmt.Sprintf("%sLRU list size: %d\n", s, n)
	bc.Unlock()

	return s
}

func Stats() string {
	return bc.Stats()
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
	if b==nil {
		return BTHole
	}

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
