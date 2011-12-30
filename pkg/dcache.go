package gostor

import (
_	"fmt"
	"os"
	"sync"
)

type DiskCache struct {
	sync.Mutex
	disk		*os.File
	blksz		uint32
	maxcount	uint32
	cache		map[uint64] *entry
	lru, mru	*entry
}

type entry struct {
	offset		uint64
	data		[]byte
	lru, mru	*entry
}

func NewDiskCache(d *os.File, blksz uint32, maxcount uint32) *DiskCache {
	dc := new(DiskCache)
	dc.disk = d
	dc.blksz = blksz
	dc.maxcount = maxcount
	dc.cache = make(map[uint64] *entry)

	return dc
}

func (dc *DiskCache) newEntry(offset uint64) (*entry, error) {
	e := new(entry)
	e.offset = offset
	e.data = make([]byte, dc.blksz)
	n, err := dc.disk.ReadAt(e.data, int64(offset))
	if err!=nil || uint32(n)!=dc.blksz {
		if uint32(n)!=dc.blksz {
			err = os.ERANGE
		}

		return nil, err
	}

	return e, nil
}

// called with lock held
func (dc *DiskCache) lruremove() {
	e := dc.lru
	if e==nil {
		return
	}

	if e.mru!=nil {
		e.mru.lru = nil
	} else {
		dc.mru = nil
	}

	dc.lru = e.mru
	delete(dc.cache, e.offset)
}

// count <= blksz
func (dc *DiskCache) Read(offset uint64, count uint32) ([]byte, error) {
	var en[2] *entry
	var f0, f1 bool
	var data []byte

	off := offset - offset%uint64(dc.blksz)
	over := (off+uint64(dc.blksz)) < (offset + uint64(count))

	dc.Lock()
	en[0], f0 = dc.cache[off]
	if over {
		en[1], f1 = dc.cache[off+uint64(dc.blksz)]
	} else {
		f1 = true
	}
	dc.Unlock()

	if en[0] == nil {
		var err error
		en[0], err = dc.newEntry(off)
		if err!=nil {
			return nil, err
		}
	}

	if over && en[1] == nil {
		var err error
		en[1], err = dc.newEntry(off + uint64(dc.blksz))
		if err!=nil {
			return nil, err
		}
	}

	if over {
		en0 := en[0].data[offset - off:]
		n := count - uint32(len(en0))
		if n > dc.blksz {
			n = dc.blksz
		}

		en1 := en[1].data[0:n]
		data = make([]byte, len(en0) + len(en1))
		copy(data, en0)
		copy(data[len(en0):], en1)
	} else {
		data = en[0].data[offset - off:offset + uint64(count) - off]
	}

	if !f0 || !f1 {
		dc.Lock()
		if !f0 {
			if uint32(len(dc.cache)) >= dc.maxcount {
				dc.lruremove()
			}

			dc.cache[en[0].offset] = en[0]
		}

		if !f1 {
			if uint32(len(dc.cache)) >= dc.maxcount {
				dc.lruremove()
			}

			dc.cache[en[1].offset] = en[1]
		}
		dc.Unlock()
	}

	return data, nil
}

func (dc *DiskCache) Discard(start, end uint64) {
	dc.Lock()
	soff := start - start%uint64(dc.blksz)
	eoff := end - end%uint64(dc.blksz) + uint64(dc.blksz)
	for soff < eoff {
		e := dc.cache[soff]
		delete(dc.cache, soff)

		// remove from the LRU list
		if e.lru!=nil {
			e.lru.mru = e.mru
		} else {
			dc.lru = e.mru
		}

		if e.mru!=nil {
			e.mru.lru = e.lru
		} else {
			dc.mru = e.lru
		}

		soff += uint64(dc.blksz)
	}

	dc.Unlock()
}

func (dc *DiskCache) ReadAll(offset uint64, count uint32) (ret []byte, err error) {
	off := offset - offset%uint64(dc.blksz)
	over := (off+uint64(dc.blksz)) < (offset + uint64(count))

	if !over {
		return dc.Read(offset, count)
	}

	ret = make([]byte, count)
	n := dc.blksz - uint32(offset + off)
	for b:=ret; len(b) > 0; {
		d, err := dc.Read(offset, n)
		if err!=nil {
			return nil, err
		}

		m := copy(b, d)
		b = b[m:]
		offset += uint64(m)
		n = dc.blksz
	}

	return
}
