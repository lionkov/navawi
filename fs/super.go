package main

import (
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"
	"gostor"
	"go9p.googlecode.com/hg/p"
)

type Super struct {
	// block layout: rootentry 0xFF nextpath[8]
	sync.Mutex
	id		uint64	// id of the super file
	root		*File
	nextpath	uint64
	blocksz		uint64
}

type btSuper int
var btsuper btSuper
var super Super

func NextPath() uint64 {
	return atomic.AddUint64(&super.nextpath, 1)
}

func ReadSuper() os.Error {
	var err os.Error

	super.id, err = gostor.Find(BTSuper)
	if err!=nil {
		return err
	}

	super.blocksz, err = gostor.Size(super.id)
	if err!=nil {
		return err
	}

	buf := make([]byte, super.blocksz)
	_, err = gostor.Read(super.id, 0, buf)
	if err != nil {
		return err
	}

	super.root = new(File)
	decodeFileInfo(buf, &super.root.FileInfo)
	super.root.Ref()
	super.nextpath, _ = gint64(buf[super.blocksz-8:])

	return nil
}

func UpdateSuper() os.Error {
	super.Lock()

	// need to make this more efficient...
	sz := sizeFileInfo(&super.root.FileInfo)
	data := make([]byte, sz + 8)
	encodeFileInfo(&super.root.FileInfo, data)
	pint64(super.nextpath, data[sz:])
	id, err := gostor.Write(super.id, 0, super.blocksz, len(data), data, BTSuper)
	if err!=nil {
		super.Unlock()
		return nil
	}

	super.id = id
	super.blocksz = uint64(len(data))
	super.Unlock()
	return nil
}

func CreateSuper() os.Error {
	super.id = 0
	super.blocksz = 0
	super.nextpath = 1
	root := new(File)
	root.Mode = p.DMDIR | 0777
	root.Uid = fs.user.Name()
	root.Uidnum = uint32(fs.user.Id())
	root.Muid = root.Uid
	root.Muidnum = root.Uidnum
	root.Gid = fs.group.Name()
	root.Gidnum = uint32(fs.group.Id())
	root.Name = ""
//	root.Ctime = time.Seconds()
	root.Mtime = uint32(time.Seconds())
	root.Root = 0
	super.root = root
	return UpdateSuper()
}

func (btSuper) Id() int {
	return BTSuper
}

// We write whole entries, so blocks should contain only whole entries
func (btSuper) Files(data []byte) []uint64 {
	bs := make([]uint64, 1)
	bs[0], _ = gint64(data[len(data) - 8:])
	return bs
}

func (btSuper) Commit(data []byte) bool {
	id, _ := gint64(data[len(data) - 8:])
	newid, err := gostor.Id(id)
	if err != nil {
		fmt.Printf("btSuper.Commit: Error %v\n", err)
		return false
	}

	pint64(newid, data[len(data) - 8:])
	return true
}
