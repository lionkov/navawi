package main

import (
	"fmt"
	"go9p.googlecode.com/hg/p"
	"gostor"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

type File struct {
	sync.Mutex
	FileInfo
	err   error
	wlock sync.Mutex
	dir   *File
	ref   int32

	next, prev *File            // next sibling
	children   map[string]*File // list of children, if directory
}

func (dir *File) Find(name string) (*File, error) {
	var fi *FileInfo

	dir.wlock.Lock()
	f := dir.children[name]
	if f != nil {
		f.Ref()
		dir.wlock.Unlock()
		return f, f.err
	}

	f = new(File)
	f.dir = dir
	if dir.children == nil {
		dir.children = make(map[string]*File)
	}

	dir.children[name] = f
	fi, f.err = FindEntry(dir.Root, name)
	if f.err == nil {
		f.FileInfo = *fi
	}

	f.Ref()
	dir.wlock.Unlock()
	return f, f.err
}

func (f *File) Remove() error {
	dir := f.dir

	dir.wlock.Lock()
	if f.err != nil {
		dir.Unlock()
		return f.err
	}

	newroot, err := RemoveEntry(dir.Root, &f.FileInfo)
	if err != nil {
		dir.Unlock()
		return err
	}
	gostor.Forget(dir.Root)
	dir.Root = newroot

	// remove the file from the children map
	delete(dir.children, f.Name)

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

func (dir *File) Create(name string, mode uint32, uid p.User, gid p.Group, ext string) (*File, error) {
	path := NextPath()
	dir.wlock.Lock()
	f := dir.children[name]
	if f != nil {
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
	f.Qid.Type = uint8(mode >> 24)
	f.Path = path
	f.Version = 1
	f.Mode = mode
	f.Mtime = uint32(time.Seconds())
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
	f.dir = dir
	f.Ref()

	newroot, err := CreateEntry(dir.Root, &f.FileInfo)
	f.err = err
	if dir.children == nil {
		dir.children = make(map[string]*File)
	}

	dir.children[name] = f
	if err != nil {
		// we got an error, return it
		dir.wlock.Unlock()
		return f, f.err
	}

	// create succeeded, update the offsets
	gostor.Forget(dir.Root)
	dir.Root = newroot
	for _, c := range dir.children {
		if c != f && c.entryOffset >= f.entryOffset {
			c.entryOffset += f.entrySize
		}
	}
	dir.wlock.Unlock()
	return f, dir.update()
}

func (f *File) Ref() {
	fmt.Printf("File.Ref %p\n", f)
	if f == nil {
		return
	}

	atomic.AddInt32(&f.ref, 1)
}

func (f *File) Unref() {
	fmt.Printf("File.Unref %p\n", f)
	if f == nil {
		return
	}

	if atomic.AddInt32(&f.ref, -1) > 0 {
		return
	}

	fmt.Printf("File.Unref %p destroyed\n", f)
	gostor.Forget(f.Root)
}

func (f *File) Read(offset uint64, buf []byte) (int, error) {
	f.Lock()
	root := f.Root
	f.Unlock()

	count, err := gostor.Read(root, offset, buf)
	fmt.Printf("File.Read count %d error %v\n", count, err)
	return count, err
}

func (f *File) Write(offset uint64, buf []byte) (int, error) {
	f.wlock.Lock()
	newroot, err := gostor.Write(f.Root, offset, offset+uint64(len(buf)), len(buf), buf, gostor.BTData)
	if err != nil {
		f.wlock.Unlock()
		return 0, err
	}
	gostor.Forget(f.Root)
	f.Root = newroot
	f.Length, err = gostor.Size(f.Root)
	fmt.Printf("File.Write new size %d\n", f.Length)
	f.wlock.Unlock()
	if err!=nil {
		return 0, err
	}

	err = f.update()

	return len(buf), err
}

func (f *File) Wstat(fi *FileInfo) error {
	var newdir uint64
	var err error

	if fi.Length != f.Length {
		if fi.Length > f.Length {
			newroot, err := gostor.Write(f.Root, f.Length, f.Length, int(fi.Length-f.Length), nil, gostor.BTData)
			if err != nil {
				return err
			}
			fi.Root = newroot
		} else {
			newroot, err := gostor.Write(f.Root, fi.Length, f.Length, 0, nil, gostor.BTData)
			if err != nil {
				return err
			}

			gostor.Forget(fi.Root)
			fi.Root = newroot
		}
	}

	f.dir.wlock.Lock()
	if fi.Name != f.Name {
		newdir, err = RenameEntry(f.dir.Root, fi)
		if err == nil {
			delete(f.dir.children, f.Name)
			f.dir.children[fi.Name] = f
		}
	} else {
		newdir, err = ModifyEntry(f.dir.Root, fi)
	}

	if err == nil {
		gostor.Forget(f.dir.Root)
		f.dir.Root = newdir
	}
	f.dir.wlock.Unlock()

	if err != nil {
		return err
	}

	err = f.dir.update()
	if err != nil {
		return err
	}

	f.wlock.Lock()
	f.FileInfo = *fi
	f.wlock.Unlock()
	return nil
}

// updates the file entry up to the super
func (f *File) update() error {
	for d := f.dir; d != nil; f, d = d, d.dir {
		d.wlock.Lock()
		fmt.Printf("File %s new root %d\n", f.Name, f.Root)
		newroot, err := ModifyEntry(d.Root, &f.FileInfo)
		if err != nil {
			d.wlock.Unlock()
			return err
		}

		gostor.Forget(d.Root)
		d.Root = newroot
		d.wlock.Unlock()
	}

	return UpdateSuper()
}
