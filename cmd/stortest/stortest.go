package main

import (
	"errors"
	"flag"
	"fmt"
	"github.com/lionkov/navawi"
	"github.com/lionkov/navawi/navawitest"
)

var id2testid map[uint64]uint64
var disk *string = flag.String("disk", "godisk", "navawi disk")
var readid = flag.Int("r", 0, "read id")

func Write(root, start, end uint64, count int, data []byte, ctype uint16) (uint64, error) {
	var nroot, ntestroot uint64
	var err1, err2 error

	testroot, ok := id2testid[root]
	if !ok {
		return 0, errors.New("invalid root")
	}

	nroot, err1 = navawi.Write(root, start, end, count, data, ctype)
	ntestroot, err2 = navawitest.Write(testroot, start, end, count, data, ctype)
	if err1 != nil {
		return 0, err1
	}

	if err2 != nil {
		return 0, err2
	}

	id2testid[nroot] = ntestroot
	return nroot, nil
}

func Read(root, offset uint64, data []byte) (int, error) {
	testroot, ok := id2testid[root]
	if !ok {
		return 0, errors.New("invalid root")
	}

	n1, err1, _ := navawi.Read(root, offset, data, true)
	if err1 != nil {
		return 0, err1
	}

	testdata := make([]byte, len(data))
	n2, err2 := navawitest.Read(testroot, offset, testdata)
	if err2 != nil {
		return 0, err2
	}

	if n1 != n2 {
		fmt.Printf("navawi and test return different data size: navawi %d test %d: %v %v\n", n1, n2, data, testdata)
		return 0, errors.New("Test Error")
	}

	for i := 0; i < n1; i++ {
		if data[i] != testdata[i] {
			fmt.Printf("navawi and test return different content at %d: %v %v\n", i, data[i:], testdata[i:])
			return 0, errors.New("Test Error")
		}
	}

	return n1, nil
}

func main() {
	var err error
	var id uint64
	var id1 uint64
	var data, buf []byte
	var n int

	flag.Parse()
	id2testid = make(map[uint64]uint64)
	id2testid[0] = 0
	navawitest.Init()
	err = navawi.Init(*disk)
	if err != nil {
		goto error
	}

	data = make([]byte, 100)
	buf = make([]byte, len(data))
	if *readid != 0 {
		n, err, _ = navawi.Read(uint64(*readid), 0, buf, true)
		if err != nil {
			goto error
		}
		fmt.Printf("id %v read %v bytes: %v\n", id, n, buf)
		return
	}

	for i := 0; i < 100; i++ {
		data[i] = byte(i)
	}

	id, err = Write(0, 50, 50, len(data), data, navawi.BTData)
	if err != nil {
		goto error
	}

	n, err = Read(id, 0, buf)
	if err != nil {
		goto error
	}

	id1, err = Write(id, 10, 20, 10, data[1:11], navawi.BTData)
	if err != nil {
		goto error
	}
	navawi.Forget(id)

	n, err = Read(id, 0, buf)
	if err != nil {
		goto error
	}

	id, err = Write(id1, 10, 19, 0, nil, navawi.BTData)
	if err != nil {
		goto error
	}

	n, err = Read(id, 0, buf)
	if err != nil {
		goto error
	}
	navawi.Forget(id)

	id, err = Write(id1, 15, 25, 0, nil, navawi.BTData)
	if err != nil {
		goto error
	}
	navawi.Forget(id1)

	n, err = Read(id, 0, buf)
	if err != nil {
		goto error
	}

	id1, err = Write(id, 15, 25, 10, data[1:11], navawi.BTData)
	if err != nil {
		goto error
	}
	navawi.Forget(id)

	n, err = Read(id1, 0, buf)
	if err != nil {
		goto error
	}

	fmt.Printf("id %v read %v bytes: %v\n", id1, n, buf)
	id, err = navawi.Sync(id1)
	if err != nil {
		goto error
	}

	fmt.Printf("root %v\n", id)
	return

error:
	fmt.Printf("Error: %v\n", err)
	return
}
