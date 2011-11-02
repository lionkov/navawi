package main

import (
	"bytes"
	"fmt"
	"os"
	"gostor"
	"go9p.googlecode.com/hg/p"
)

const (
	DirEntrySize = 70
	BTDirEntry = 0x1234
	BTSuper = 0x5678
)

type FileInfo struct {
	p.Dir

	Root		uint64	// id of the file
	entryOffset	uint64	// offset in the parent file
	entrySize	uint64	// size of the entry
}

// Directory Entry Layout
//	0xFF
//	Fxbits[3]
//	Qid[13]
//	Mode[4]
//	Mtime[4]
//	Length[8]
//	Name[n]
//	Uid[n]
//	Gid[n]
//	Muid[n]
//	Ext[n]
//	Uidnum[4]
//	Gidnum[4]
//	Muidnum[4]
//	Root[8]

var startMarker = []byte { 0xFF }

// read forward starting from the specified offset until we reach the start marker
// of the next entry
func readUntilStart(dir, offset uint64, buf []byte) ([]byte, os.Error) {
	var pos int

	for {
		if buf==nil {
			buf = make([]byte, DirEntrySize*2)
			pos = 0
		} else {
			tbuf := make([]byte, len(buf) + DirEntrySize*2)
			pos = copy(tbuf, buf)
			buf = tbuf
		}

		n, err := gostor.Read(dir, offset+uint64(pos), buf[pos:])
		if err!=nil {
			return nil, err
		}

		if n==0 {
			return nil, os.EOF
		}

		buf = buf[0:pos+n]
		if m := bytes.Index(buf[pos:], startMarker); m >= 0 {
			return buf[0:m+4], nil
		}
	}

	// can't happen
	return nil, nil
}

func readBackUntilStart(dir, offset uint64, buf []byte) (uint64, []byte, os.Error) {
	for {
		size := DirEntrySize*2
		if offset-uint64(size) < 0 {
			size = int(offset)
		}

		if buf==nil {
			buf = make([]byte, size)
		} else {
			tbuf := make([]byte, len(buf) + size)
			copy(tbuf[size:], buf)
		}

		n, err := gostor.Read(dir, offset - uint64(size), buf[0:size])
		if err!=nil {
			return 0, nil, err
		}

		if n!=size {
			return 0, nil, os.NewError("short read")
		}

		offset -= uint64(size)
		if m := bytes.LastIndex(buf[0:size], startMarker); m >= 0 {
			return offset+uint64(m), buf[m:], nil
		}
	}

	// can't happen
	return 0, nil, nil
}

// Find DirEntry at the specified offset. The offset doesn't need to point to
// the beginning of the entry, the function will find and return the offset
// of the beginning, as well as the content of the entry.
func findEntry(dir, offset uint64) (uint64, []byte, os.Error) {
	var pos int
	var buf []byte

//	fmt.Printf("findEntry offset %d\n", offset)
	// read forward until we find 0xFF
	for {
		if buf==nil {
			buf = make([]byte, DirEntrySize*2)
			pos = 0
		} else {
			tbuf := make([]byte, len(buf) + DirEntrySize*2)
			pos = copy(tbuf, buf)
			buf = tbuf
		}

		n, err := gostor.Read(dir, offset+uint64(pos), buf[pos:])
		if err!=nil {
			return 0, nil, err
		}

		buf = buf[0:pos+n]
		if n==0 {
			break
//			return 0, nil, os.EOF
		}

		if m := bytes.IndexByte(buf[pos:], 0xFF); m >= 0 {
			buf = buf[0:m]
			break
		}
	}

//	fmt.Printf("findEntry: read forward %d bytes\n", len(buf))
	// read backward until we find 0xFF
L:
	for {
		size := DirEntrySize*2
		if int64(offset)-int64(size) < 0 {
			size = int(offset)
		}

		tbuf := make([]byte, len(buf) + size)
		copy(tbuf[size:], buf)
		buf = tbuf
		n, err := gostor.Read(dir, offset - uint64(size), buf[0:size])
		if err!=nil {
			return 0, nil, err
		}

		if n!=size {
			return 0, nil, os.NewError("short read")
		}

		for i:=size-1; i>=0; i-- {
			offset--
			if buf[i] == 0xFF {
				buf = buf[i:]
				break L
			}
		}
	}

//	fmt.Printf("findEntry: read backward to offset %d len %d\n", offset, len(buf))
	return offset, buf, nil
}

// Encode file entry by replacing all 0xFF and 0xFE bytes with 0xFE, remembering
// the last bit for each occurance in the first four bytes of the array. First
// byte is always 0xFF
func encodeEntry(data []byte) {
	bits := 0
	nbits := 0
	for i:=len(data) - 1; i > 3; i-- {
		switch data[i] {
		case 0xFF:
			data[i] = 0xFE
			bits = (bits<<1) | 1
			nbits++

		case 0xFE:
			bits = bits<<1
			nbits++
		}
	}

	if nbits>24 {
		fmt.Printf("too many 0xFx bytes while encoding\n")
	}

	data[0] = 0xFF
	data[1] = uint8(bits)
	data[2] = uint8(bits >> 8)
	data[3] = uint8(bits >> 16)
}

// Opposite of encodeEntry
// Decode file entry by restoring the occurances of 0xFF and 0xFE, using the data
// in the first four bytes of the array. First byte should always be 0xFF
func decodeEntry(data []byte) {
	fmt.Printf("decodeEntry len %d\n", len(data))
	bits := uint32(data[1]) | (uint32(data[2]) << 8) | (uint32(data[3]) << 16)
	for i:=0; i < len(data); i++ {
		if data[i] == 0xFE {
			if bits&1 == 1 {
				data[i] = 0xFF
			}

			bits = bits>>1
		}
	}
}

func entryName(data []byte) string {
	sz, _ := gint16(data[33:])
	return string(data[35:35+sz])
}

// Does binary search in the directory file, looking for the specified name
// If found, returns the offset of the entry as well as the content
// If not found, returns the offset where the entry with that name should be
// inserted and nil as entry content.
func bsearchEntry(dir uint64, name string) (uint64, []byte, os.Error) {
	var b, t, n uint64
	var offset uint64
	var entry []byte

	n = 0
	b = 0
	t, err := gostor.Size(dir)
	if err!=nil {
		return 0, nil, err
	}

	for b < t {
		n = (b + t) / 2
		offset, entry, err = findEntry(dir, n)
		if err!=nil {
			return 0, nil, err
		}

		decodeEntry(entry)
		ename := entryName(entry)
		if ename==name {
			return offset, entry, nil
		} else if ename < name {
			b = offset + uint64(len(entry))
		} else {
			t = offset
		}
	}

	return offset, nil, nil
}

func pslice(slice, buf []byte) []byte {
	p := pint16(uint16(len(slice)), buf)
	copy(p, slice)

	return p[len(slice):]
}

func sizeFileInfo(fi *FileInfo) uint32 {
	name := []byte(fi.Name)
	uid := []byte(fi.Uid)
	gid := []byte(fi.Gid)
	muid := []byte(fi.Muid)
	ext := []byte(fi.Ext)
	sz := 4 + 		// marker + Fxbits
		13 +		// Qid[13]
		4 + 		// Mode[4]
		4 +		// Mtime[4]
		8 + 		// Length[8]
		2 + len(name) +	// Name[n]
		2 + len(uid) +	// Uid[n]
		2 + len(gid) + 	// Gid[n]
		2 + len(muid) +	// Muid[n]
		2 + len(ext) +	// Ext[n]
		4 + 		// Uidnum[4]
		4 + 		// Gidnum[4]
		4 + 		// Muidnum[4]
		8		// Root[8]

	return uint32(sz)
}

func encodeFileInfo(fi *FileInfo, buf []byte) ([]byte, int) {
	sz := sizeFileInfo(fi)
	name := []byte(fi.Name)
	uid := []byte(fi.Uid)
	gid := []byte(fi.Gid)
	muid := []byte(fi.Muid)
	ext := []byte(fi.Ext)

	if buf==nil {
		buf = make([]byte, sz)
	}

	p := pint8(fi.Qid.Type, buf[4:])
	p = pint32(fi.Version, p)
	p = pint64(fi.Path, p)
	p = pint32(fi.Mode, p)
	p = pint32(fi.Mtime, p)
	p = pint64(fi.Length, p)
	p = pslice(name, p)
	p = pslice(uid, p)
	p = pslice(gid, p)
	p = pslice(muid, p)
	p = pslice(ext, p)
	p = pint32(fi.Uidnum, p)
	p = pint32(fi.Gidnum, p)
	p = pint32(fi.Muidnum, p)
	p = pint64(fi.Root, p)

	encodeEntry(buf[0:sz])
	return buf, int(sz)
}

func gstr(buf []byte) (string, []byte) {
	n, p := gint16(buf)
	return string(p[0:n]), p[n:]
}
	
func decodeFileInfo(entry []byte, fi *FileInfo) []byte {
	var p []byte

	fi.Qid.Type, p = gint8(entry[4:])
	fi.Version, p = gint32(p)
	fi.Path, p = gint64(p)
	fi.Mode, p = gint32(p)
	fi.Mtime, p = gint32(p)
	fi.Length, p = gint64(p)
	fi.Name, p = gstr(p)
	fi.Uid, p = gstr(p)
	fi.Gid, p = gstr(p)
	fi.Muid, p = gstr(p)
	fi.Ext, p = gstr(p)
	fi.Uidnum, p = gint32(p)
	fi.Gidnum, p = gint32(p)
	fi.Muidnum, p = gint32(p)
	fi.Root, p = gint64(p)

	return p
}

func CreateEntry(dir uint64, fi *FileInfo) (newdir uint64, err os.Error) {
	offset, data, err := bsearchEntry(dir, fi.Name)
	if err != nil {
		return 0, err
	}

	if data!=nil {
		return 0, os.EEXIST
	}

	entry, _ := encodeFileInfo(fi, nil)
	dir, err = gostor.Write(dir, offset, offset, len(entry), entry, BTDirEntry)
	fi.entryOffset = offset
	fi.entrySize = uint64(len(entry))
	return dir, err
}

func RemoveEntry(dir uint64, fi *FileInfo) (newdir uint64, err os.Error) {
	return gostor.Write(dir, fi.entryOffset, fi.entryOffset + fi.entrySize, 0, nil, 0)
}

// uses fi.entryOffset and fi.entrySize to decide which entry to modify
// returns the new size in fi.entrySize
func ModifyEntry(dir uint64, fi *FileInfo) (newdir uint64, err os.Error) {
	newentry, _ := encodeFileInfo(fi, nil)
	dir, err = gostor.Write(dir, fi.entryOffset, fi.entryOffset + fi.entrySize, len(newentry), newentry, BTDirEntry)
	fi.entrySize = uint64(len(newentry))
	return dir, err
}

func RenameEntry(dir uint64, fi *FileInfo) (newdir uint64, err os.Error) {
	offset, entry, err := bsearchEntry(dir, fi.Name)
	if err!=nil {
		return 0, err
	}

	if entry!=nil {
		return 0, os.EEXIST
	}

	// remove the entry from the old offset
	newdir, err = gostor.Write(dir, fi.entryOffset, fi.entryOffset + fi.entrySize, 0, nil, 0)
	if err!= nil {
		return 0, err
	}

	// insert it at the new offset
	newentry, _ := encodeFileInfo(fi, nil)
	newdir, err = gostor.Write(newdir, offset, offset, len(newentry), newentry, BTDirEntry)
	return newdir, err
}

func FindEntry(dir uint64, name string) (fi *FileInfo, err os.Error) {
	offset, entry, err := bsearchEntry(dir, name)
	if err != nil {
		return nil, err
	}

	if entry==nil {
		return nil, os.ENOENT
	}

	fi = new(FileInfo)
	decodeFileInfo(entry, fi)
	fi.entryOffset = offset
	fi.entrySize = uint64(len(entry))
//	fmt.Printf("FindEntry: root %d\n", fi.Root)

	return fi, nil
}

type btDirEntry int
var btdirentry btDirEntry

func (btDirEntry) Id() uint16 {
	return BTDirEntry
}

// We write whole entries, so blocks should contain only whole entries
func (btDirEntry) Files(data []byte) []uint64 {
	var bs []uint64

	if len(data) == 0 {
		return make([]uint64, 0)
	}

	for len(data) > 0 {
		n := bytes.IndexByte(data[1:], 0xFF)
		if n < 0 {
			n = len(data)
		} else {
			n++
		}

//		fmt.Printf("btDirEntry.Files: n %d remaining data: %v\n", n, data)
		entry := data[0:n]
		decodeEntry(entry)
		id, _ := gint64(entry[len(entry) - 8:])
		encodeEntry(entry)
		bs = append(bs, id)
		data = data[n:]
	}

//	fmt.Printf("btDirEntry.Files: %v\n", bs)
	return bs
}

func (btDirEntry) Commit(data []byte) bool {
	if len(data) == 0 {
		return true
	}

	for len(data) > 0 {
		n := bytes.IndexByte(data[1:], 0xFF)
		if n < 0 {
			n = len(data)
		} else {
			n++
		}

		entry := data[0:n]
		decodeEntry(entry)
		id, _ := gint64(entry[len(entry) - 8:])
		newid, err := gostor.Id(id)
		if err!=nil {
			fmt.Printf("btDirEntry.Commit: Error %v\n", err)
			return false
		}

//		fmt.Printf("btDirEntry.Commit: %d -> %d\n", id, newid)
		pint64(newid, entry[len(entry) - 8:])
		encodeEntry(entry)
		data = data[n:]
	}

	return true
}

func gint8(buf []byte) (uint8, []byte) { return buf[0], buf[1:len(buf)] }

func gint16(buf []byte) (uint16, []byte) {
	return uint16(buf[0]) | (uint16(buf[1]) << 8), buf[2:len(buf)]
}

func gint32(buf []byte) (uint32, []byte) {
	return uint32(buf[0]) | (uint32(buf[1]) << 8) | (uint32(buf[2]) << 16) |
		(uint32(buf[3]) << 24),
		buf[4:len(buf)]
}

func gint64(buf []byte) (uint64, []byte) {
	return uint64(buf[0]) | (uint64(buf[1]) << 8) | (uint64(buf[2]) << 16) |
		(uint64(buf[3]) << 24) | (uint64(buf[4]) << 32) | (uint64(buf[5]) << 40) |
		(uint64(buf[6]) << 48) | (uint64(buf[7]) << 56),
		buf[8:len(buf)]
}

func pint8(val uint8, buf []byte) []byte {
	buf[0] = val
	return buf[1:len(buf)]
}

func pint16(val uint16, buf []byte) []byte {
	buf[0] = uint8(val)
	buf[1] = uint8(val >> 8)
	return buf[2:len(buf)]
}

func pint32(val uint32, buf []byte) []byte {
	buf[0] = uint8(val)
	buf[1] = uint8(val >> 8)
	buf[2] = uint8(val >> 16)
	buf[3] = uint8(val >> 24)
	return buf[4:len(buf)]
}

func pint64(val uint64, buf []byte) []byte {
	buf[0] = uint8(val)
	buf[1] = uint8(val >> 8)
	buf[2] = uint8(val >> 16)
	buf[3] = uint8(val >> 24)
	buf[4] = uint8(val >> 32)
	buf[5] = uint8(val >> 40)
	buf[6] = uint8(val >> 48)
	buf[7] = uint8(val >> 56)
	return buf[8:len(buf)]
}
