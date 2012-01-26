package gostor

import (
_	"fmt"
	"os"
	"sync"
	"io"
	"fmt"
	"net/http"
	"sync/atomic"
)

type DiskCache struct {
	sync.Mutex
	disk		*os.File
	blksz		uint32
	maxcount	uint32
	cache		map[uint64] *entry
	lru, mru	*entry

	// stats
	nreads		uint64		// number of reads from the disk
	ndiscards	uint64		// number of discards
	ndrops		uint64		// number of drops (discards + full cache)
	nhits		uint64		// number of cache hits
	nshorts		uint64
	njoins		uint64
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

	http.Handle("/gostor/dcache", dc)
	return dc
}

func (dc *DiskCache) ServeHTTP(c http.ResponseWriter, r *http.Request) {
	io.WriteString(c, fmt.Sprintf("<html><body><h1>Disk Cache</h1>"))
	defer io.WriteString(c, "</body></html>")

	dc.Lock()
	io.WriteString(c, fmt.Sprintf("<p>Cache size: %d\n", len(dc.cache)))
	io.WriteString(c, fmt.Sprintf("<p>Number of Hits: %d\n", dc.nhits))
	io.WriteString(c, fmt.Sprintf("<p>Number of reads: %d\n", dc.nreads))
	io.WriteString(c, fmt.Sprintf("<p>Number of discards: %d\n", dc.ndiscards))
	io.WriteString(c, fmt.Sprintf("<p>Number of drops: %d\n", dc.ndrops))
	io.WriteString(c, fmt.Sprintf("<p>Short reads: %d\n", atomic.LoadUint64(&dc.nshorts)))
	io.WriteString(c, fmt.Sprintf("<p>Join reads: %d\n", atomic.LoadUint64(&dc.njoins)))
	dc.Unlock()
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

// called with bc lock held
func (dc *DiskCache) lrucheck() {
	m := make(map[string] *entry)
	n := 0
	for e := dc.mru; e != nil; e = e.lru {
		s := fmt.Sprintf("%p", e)
		if m[s] != nil {
			panic(fmt.Sprintf("e %p offset %d position %d\n", e, e.offset, n))
		}
		m[s] = e
		n++
	}
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
	e.lru = nil
	e.mru = nil
	dc.ndrops++
//	dc.lrucheck()
}

// called with lock held
func (dc *DiskCache) lruadd(e *entry) {
	e.mru = nil
	e.lru = dc.mru
	if dc.mru!=nil {
		dc.mru.mru = e
	}

	dc.mru = e
	if dc.lru == nil {
		dc.lru = e
	}
//	dc.lrucheck()
}

// called with lock held
func (dc *DiskCache) lruupdate(e *entry) {
	if e==nil {
		return
	}

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

	dc.lruadd(e)
//	dc.lrucheck()
}

// count <= blksz
func (dc *DiskCache) read(offset uint64, count uint32) ([]byte, error) {
	var en[2] *entry
	var f0, f1 bool
	var data []byte

	off := offset - offset%uint64(dc.blksz)
	over := (off+uint64(dc.blksz)) < (offset + uint64(count))
	if over {
		atomic.AddUint64(&dc.njoins, 1)
	}

	dc.Lock()
	en[0], f0 = dc.cache[off]
	if f0 {
		dc.lruupdate(en[0])
		dc.nhits++
	} else {
		dc.nreads++
	}

	if over {
		en[1], f1 = dc.cache[off+uint64(dc.blksz)]
		if f1 {
			dc.lruupdate(en[1])
			dc.nhits++
		} else {
			dc.nreads++
		}
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
			dc.lruadd(en[0])
		}

		if !f1 {
			if uint32(len(dc.cache)) >= dc.maxcount {
				dc.lruremove()
			}

			dc.cache[en[1].offset] = en[1]
			dc.lruadd(en[1])
		}
		dc.Unlock()
	}

	return data, nil
}

func (dc *DiskCache) Discard(start, end uint64) {
	dc.Lock()
	soff := start - start%uint64(dc.blksz)
	eoff := end - end%uint64(dc.blksz) + uint64(dc.blksz)
	for ; soff < eoff; soff += uint64(dc.blksz) {
		e := dc.cache[soff]
		if e==nil {
			continue
		}

		delete(dc.cache, soff)
		dc.ndiscards++

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
	}

	dc.Unlock()
}

func (dc *DiskCache) ReadAll(offset uint64, count uint32) (ret []byte, err error) {
	off := offset - offset%uint64(dc.blksz)
	over := (off+uint64(2*dc.blksz)) < (offset + uint64(count))

	if !over {
		atomic.AddUint64(&dc.nshorts, 1)
		return dc.read(offset, count)
	}

	ret = make([]byte, count)
	n := dc.blksz - uint32(offset - off)
	for b:=ret; len(b) > 0; {
		d, err := dc.read(offset, n)
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
