package main

import (
	"flag"
	"fmt"
	"os"
	"gostor"
	"gostortest"
)

var id2testid map[uint64]uint64
var disk *string = flag.String("disk", "godisk", "gostor disk")
var readid = flag.Int("r", 0, "read id")

func Write(root, start, end uint64, count int, data []byte, ctype uint16) (uint64, os.Error) {
	var nroot, ntestroot uint64
	var err1, err2 os.Error

	testroot, ok := id2testid[root]
	if !ok {
		return 0, os.NewError("invalid root")
	}

	nroot, err1 = gostor.Write(root, start, end, count, data, ctype)
	ntestroot, err2 = gostortest.Write(testroot, start, end, count, data, ctype)
	if err1!=nil {
		return 0, err1
	}

	if err2!=nil {
		return 0, err2
	}

	id2testid[nroot] = ntestroot
	return nroot, nil
}

func Read(root, offset uint64, data []byte) (int, os.Error) {
	testroot, ok := id2testid[root]
	if !ok {
		return 0, os.NewError("invalid root")
	}

	n1, err1 := gostor.Read(root, offset, data)
	if err1 != nil {
		return 0, err1
	}

	testdata := make([]byte, len(data))
	n2, err2 := gostortest.Read(testroot, offset, testdata)
	if err2 != nil {
		return 0, err2
	}

	if n1 != n2 {
		fmt.Printf("gostor and test return different data size: gostor %d test %d: %v %v\n", n1, n2, data, testdata)
		return 0, os.NewError("Test Error")
	}

	for i:=0; i < n1; i++ {
		if data[i] != testdata[i] {
			fmt.Printf("gostor and test return different content at %d: %v %v\n", i, data[i:], testdata[i:])
			return 0, os.NewError("Test Error")
		}
	}

	return n1, nil
}

func main() {
	var err os.Error
	var id uint64
	var id1 uint64
	var data, buf []byte
	var n int

	flag.Parse()
	id2testid = make(map[uint64]uint64)
	id2testid[0] = 0
	gostortest.Init()
	err = gostor.Init(*disk)
	if err!=nil {
		goto error
	}

	data = make([]byte, 100)
	buf = make([]byte, len(data))
	if *readid != 0 {
		n, err = gostor.Read(uint64(*readid), 0, buf)
		if err!=nil {
			goto error
		}
		fmt.Printf("id %v read %v bytes: %v\n", id, n, buf)
		return
	}

	for i:=0; i < 100; i++ {
		data[i] = byte(i)
	}

	id, err = Write(0, 50, 50, len(data), data, gostor.BTData)
	if err!=nil {
		goto error
	}

	n, err = Read(id, 0, buf)
	if err!=nil {
		goto error
	}

	id1, err = Write(id, 10, 20, 10, data[1:11], gostor.BTData)
	if err!=nil {
		goto error
	}
	gostor.Forget(id)

	n, err = Read(id, 0, buf)
	if err!=nil {
		goto error
	}

	id, err = Write(id1, 10, 19, 0, nil, gostor.BTData)
	if err!=nil {
		goto error
	}

	n, err = Read(id, 0, buf)
	if err!=nil {
		goto error
	}
	gostor.Forget(id)

	id, err = Write(id1, 15, 25, 0, nil, gostor.BTData)
	if err!=nil {
		goto error
	}
	gostor.Forget(id1)

	n, err = Read(id, 0, buf)
	if err!=nil {
		goto error
	}

	id1, err = Write(id, 15, 25, 10, data[1:11], gostor.BTData)
	if err!=nil {
		goto error
	}
	gostor.Forget(id)

	n, err = Read(id1, 0, buf)
	if err!=nil {
		goto error
	}

	fmt.Printf("id %v read %v bytes: %v\n", id1, n, buf)
	err = gostor.Sync(id1)
	if err!=nil {
		goto error
	}

	id, err = gostor.Id(id1)
	if err!=nil {
		goto error
	}

	fmt.Printf("root %v\n", id)
	return

error:
	fmt.Printf("Error: %v\n", err)
	return
}
