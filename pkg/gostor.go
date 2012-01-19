package gostor

import (
	"errors"
	"fmt"
	"os"
)

type conf struct {
	Maxblocksz	int
	Maxdatablocksz	int
	Segmentsz	uint32
	Dcacheblksz	uint32
	Dcachesz	uint32
	Bcachesz	uint32
}

// global variables
var dc *DiskCache
var bc *BlockCache
var sm *SegmentManager

var Conf = conf {
	Maxblocksz:	 8192,
	Maxdatablocksz:	65536 - 8192,
	Segmentsz:	16*1024*1024,
	Dcacheblksz:	8192,
	Dcachesz:	16*1024,
	Bcachesz:	16*1024,
}

func Init(diskname string) error {
	var err error
	var d *os.File

	d, err = os.OpenFile(diskname, os.O_RDWR, 0666)
	if err!=nil {
		return err
	}

	dc = NewDiskCache(d, Conf.Dcacheblksz, Conf.Dcachesz)

	sm, err = NewSegmentManager(dc, d)
	if err != nil {
		return err
	}

	bc, err = NewBlockCache(dc, d, Conf.Bcachesz)
	if err!=nil {
		d.Close()
		return err
	}

	initBtree()
	return nil
}

// Return the up-to-date ID for the specified root
func Id(root uint64) (uint64, error) {
	b, err := bc.Get(root)
	if err!=nil {
		return 0, err
	}

	return b.Offset, nil
}

// Commit the specified root to disk. The file needs to be
// still "live"
func Sync(root uint64) error {
//	debug("Sync %v\n", root)
	if root%2==0 && root!=0 {
		return nil
	}

	b, err := bc.Get(root)
	if err!=nil {
		return err
	}

//	debug("blk %p type %x\n", b, b.blktype)
	if !b.IsRoot() {
		return errors.New("not root")
	}

	bc.Commit(b)
//	debug("Sync old %v new %v\n", root, b.Offset)
	return nil
}

// Forget the file with ID root. If the file is not referenced by other files
// it will be garbage collected (eventually)
func Forget(root uint64) error {
//	debug("Forget %d\n", root)
	b, err := bc.Get(root)
	if err!=nil {
		return err
	}

	if !b.IsRoot() {
		return errors.New("not root")
	}

	bc.RemoveLiveBlock(b.Offset)
	return nil
}

// Find the latest file that contains data of the specified type
// Current implementation allows only short files (with root block at
// level 0) to be found
func Find(btype uint16) (uint64, error) {

	btype |= BTRoot
	root := uint64(0)

	// first search the uncommitted blocks
	bc.Lock()
	for b := bc.uclast; b != nil; b = b.ucprev {
		if b.blktype == btype {
			root = b.Offset
			break
		}
	}
	bc.Unlock()

	if root!=0 {
		return root, nil
	}

	// search the committed blocks
	return sm.FindBlock(btype)
}

func Size(root uint64) (uint64, error) {
	b, err := bc.Get(root)
	if err!= nil {
		return 0, err
	}

	sz := uint64(0)
	blktype := b.Type()
	if blktype<BTNode1 || blktype>BTNode7 {
		sz = uint64(len(b.Data))
	} else {
		nent := len(b.Data)/BTEntrySize
//		debug("Size: nent %d %v\n", nent, b.Data)
		for i:=0; i<nent; i++ {
			n, _ := gint64(b.Data[i*BTEntrySize+8:])
			sz += n
		}
	}

//	debug("Size %d %d\n", root, sz)
	return sz, nil
}

var debugFlag bool

func SetDebug(on bool) {
	debugFlag = on
}

func debug(f string, a ...interface{}) {
	if debugFlag {
		fmt.Printf(f, a...)
	}
}
