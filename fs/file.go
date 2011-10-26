package main

import (
	"os"
	"sync"
	"sync/atomic"
	"time"
	"gostor"
	"go9p.googlecode.com/hg/p"
)

type File struct {
	sync.Mutex
	FileInfo
	err		os.Error
	wlock		sync.Mutex
	dir		*File
	ref		int32

	diroffset 	uint64
	next, prev	*File	// next sibling
	children	map[string]*File	// list of children, if directory
}

func (dir *File) Find(name string) (*File, os.Error) {
	var fi *FileInfo

	dir.wlock.Lock()
	f := dir.children[name]
	if f!=nil {
		f.Ref()
		dir.wlock.Unlock()
		return f, f.err
	}

	f = new(File)
	f.dir = dir
	if dir.children==nil {
		dir.children = make(map[string]*File)
	}

	dir.children[name] = f
	fi, f.err = FindEntry(dir.Root, name)
	if f.err==nil {
		f.FileInfo = *fi
	}

	f.Ref()
	dir.wlock.Unlock()
	return f, f.err
}
	
func (f *File) Remove() os.Error {
	dir := f.dir

	dir.wlock.Lock()
	if f.err!=nil {
		dir.Unlock()
		return f.err
	}

	newroot, err := RemoveEntry(dir.Root, &f.FileInfo)
	if err != nil {
		dir.Unlock()
		return err
	}
	dir.Root = newroot

	// remove the file from the children map
	dir.children[f.Name] = nil, false

	// update offsets for all files
	offset := f.entryOffset
	size := f.entrySize
	for _, c := range dir.children {
		if c.entryOffset > offset {
			c.entryOffset -= size
		}
	}
	dir.wlock.Unlock()

	return dir.update()
}

func (dir *File) Create(name string, mode uint32, uid p.User, gid p.Group, ext string) (*File, os.Error) {
	path := NextPath()
	dir.wlock.Lock()
	f := dir.children[name]
	if f!=nil {
		if f.err == nil || f.err != os.ENOENT {
			err := f.err
			dir.wlock.Unlock()
			if err == nil {
				err = os.EEXIST
			}

			return nil, err
		}
	}

	f = new(File)
	f.Qid.Type = uint8(mode>>24)
	f.Path = path
	f.Version = 1
	f.Mode = mode
	f.Mtime = uint32(time.Seconds()>>32 / 1000000000)
	f.Length = 0
	f.Name = name
	f.Uid = uid.Name()
	f.Gid = gid.Name()
	f.Muid = uid.Name()
	f.Ext = ext
	f.Uidnum = uint32(uid.Id())
	f.Gidnum = uint32(gid.Id())
	f.Muidnum = uint32(uid.Id())
	f.Root = 0
	f.Ref()

	newroot, err := CreateEntry(dir.Root, &f.FileInfo)
	f.err = err
	if dir.children==nil {
		dir.children=make(map[string]*File)
	}

	dir.children[name] = f
	if err != nil {
		// we got an error, return it
		dir.wlock.Unlock()
		return f, f.err
	}

	// create succeeded, update the offsets
	dir.Root = newroot
	for _, c := range dir.children {
		if c!=f && c.entryOffset >= f.entryOffset {
			c.entryOffset += f.entrySize
		}
	}
	dir.wlock.Unlock()
	return f, dir.update()
}

func (f *File) Ref() {
	atomic.AddInt32(&f.ref, 1)
}

func (f *File) Unref() {
	if atomic.AddInt32(&f.ref, -1) > 0 {
		return
	}

	gostor.Forget(f.Root)
}

func (f *File) Read(offset uint64, buf []byte) (int, os.Error) {
	f.Lock()
	root := f.Root
	f.Unlock()

	return gostor.Read(root, offset, buf)
}

func (f *File) Write(offset uint64, buf []byte) (int, os.Error) {
	f.wlock.Lock()
	newroot, err := gostor.Write(f.Root, offset, offset+uint64(len(buf)), len(buf), buf, gostor.BTData)
	if err!=nil {
		f.wlock.Unlock()
		return 0, err
	}
	f.Root = newroot
	f.wlock.Unlock()
	err = f.update()
	
	return len(buf), err
}

func (f *File) Wstat(fi *FileInfo) os.Error {
	var newdir uint64
	var err os.Error

	if fi.Length != f.Length {
		if fi.Length > f.Length {
			newroot, err := gostor.Write(f.Root, f.Length, f.Length, int(fi.Length - f.Length), nil, gostor.BTData)
			if err!= nil {
				return err
			}
			fi.Root = newroot
		} else {
			newroot, err := gostor.Write(f.Root, fi.Length, f.Length, 0, nil, gostor.BTData)
			if err!=nil {
				return err
			}

			fi.Root = newroot
		}
	}

	f.dir.wlock.Lock()
	if fi.Name != f.Name {
		newdir, err = RenameEntry(f.dir.Root, fi)
		if err==nil {
			f.dir.children[f.Name] = nil, false
			f.dir.children[fi.Name] = f
		}
	} else {
		newdir, err = ModifyEntry(f.dir.Root, fi)
	}

	if err==nil {
		f.dir.Root = newdir
	}
	f.dir.wlock.Unlock()

	if err!=nil {
		return err
	}
		
	f.wlock.Lock()
	f.FileInfo = *fi
	f.wlock.Unlock()
	return nil
}

// updates the file entry up to the super
func (f *File) update() os.Error {
	for d := f.dir; d != nil; f, d = d, d.dir {
		d.wlock.Lock()
		newroot, err := ModifyEntry(d.Root, &f.FileInfo)
		if err != nil {
			d.wlock.Unlock()
			return err
		}

		d.Root = newroot
		d.wlock.Unlock()
	}

	return UpdateSuper()
}
