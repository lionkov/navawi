package navawitest

import (
	"errors"
//	"fmt"
)

var nextid uint64
var files [][]byte

func Init() {
	files = make([][]byte, 1)
	files[0] = make([]byte, 0)
}

func Write(root, start, end uint64, count int, data []byte, ctype uint16) (uint64, error) {
	f := files[root]
	if f==nil {
		return 0, errors.New("invalid root")
	}

	sz := start + uint64(count)
	if end < uint64(len(f)) {
		sz += uint64(len(f)) - end
	}

//	fmt.Printf("*** id %v start %d end %d count %d len(f) %d newsz %d\n", root, start, end, count, len(f), sz)
	nf := make([]byte, sz)
	n := start
	if uint64(len(f)) < start {
		n = uint64(len(f))
	}

//	fmt.Printf("*** copy start 0 .. %d\n", n)
	copy(nf, f[0:n])
	copy(nf[start:], data)

	if end < uint64(len(f)) {
//		fmt.Printf("*** copy end %d .. %d\n", end, len(f))
		copy(nf[start+uint64(count):], f[end:])
	}

	id := uint64(len(files))
	files = append(files, nf)

//	fmt.Printf("*** id %d\n", id)
	return id, nil
}

func Read(root, offset uint64, data []byte) (int, error) {
	f := files[root]
	if f==nil {
		return 0, errors.New("invalid root")
	}

	if offset >= uint64(len(f)) {
		return 0, nil
	}

	return copy(data, f[offset:]), nil
}

func Forget(root uint64) error {
	files[root] = nil
	return nil
}
