package main

import (
	"errors"
	"flag"
	"fmt"
	"gostor"
	"math/rand"
)

const (
	Opread	= iota
	Opwrite
	Oplast
)

type Op struct {
	op	int
	start	int64
	end	int64
	len	int32
	count	int32
	dump	string
}

type File struct {
	root	uint64
	datalen	uint64
	data	[]byte
	ops 	[]*Op
}

var debug = flag.Bool("d", false, "debug output for all operations")
var flen = flag.Int64("l", 262144, "upper bound on file size")
var oplen = flag.Int("o", 65536, "upper bound on operation size")
var numop = flag.Int("N", 1000000, "total number of operations")
var seed = flag.Int64("S", 1, "seed for random number generator")
var checkAll = flag.Bool("c", false, "check the content of the whole file after every write")
var checkSize = flag.Bool("s", false, "check the file size after each write operation")
var threadnum = flag.Int("t", 1, "number of op threads")
var dbgAt = flag.Int("g", -1, "turn on Gostor debugging for the specified operation number")

var testc chan bool

func (f *File) Read(op *Op) error {
	if *debug {
		fmt.Printf("** read offset %d count %d\n", op.start, op.count)
	}

	buf := make([]byte, op.count)
	l := 0
	for o, p := uint64(op.start), buf; len(p) > 0; {
		count, err, _ := gostor.Read(f.root, o, p)
		if err!=nil {
			return err
		}

		if count==0 {
			if o < f.datalen {
				return errors.New(fmt.Sprintf("short read: offset %d filesize %d", o, f.datalen))
			}

			break
		}

		p = p[count:]
		o += uint64(count)
		l += count
	}

	// check if the content is the same
	for i:=0; i<l; i++ {
		if f.data[int(op.start)+i] != buf[i] {
			return errors.New(fmt.Sprintf("wrong data: offset %d got %x should be %x\n", int(op.start)+i, buf[i], f.data[int(op.start)+i]))
		}
	}

	return nil
}

func (f *File) Write(op *Op) error {
	if op.len > op.count {
		op.len = op.count
	}

	filesz := op.start + int64(op.count)
	if op.end < int64(f.datalen) {
		filesz += int64(f.datalen) - op.end
	}

//	fmt.Printf("** start %d end %d count %d datalen %d filesz %d\n", op.start, op.end, op.count, datalen, filesz)
	if filesz > *flen {
		delta := filesz - *flen
		if op.start > delta {
			op.start -= delta
		} else {
			op.end += delta - op.start
			op.start = 0
		}

		filesz = op.start + int64(op.count)
		if op.end < int64(f.datalen) {
			filesz += int64(f.datalen) - op.end
		}
//		fmt.Printf("** updated: start %d end %d count %d datalen %d filesz %d\n", op.start, op.end, op.count, datalen, filesz)
	}

	if *debug {
		fmt.Printf("** write start %d end %d count %d len %d\n", op.start, op.end, op.count, op.len)
	}

	buf := make([]byte, op.len)
	for i:=int32(0); i < op.len; i++ {
		buf[i] = byte(rand.Intn(256))
	}

	newroot, err := gostor.Write(f.root, uint64(op.start), uint64(op.end), int(op.count), buf, gostor.BTData)
	if err!=nil {
		return err
	}

	op.dump, err = gostor.Dump(newroot)
	if op.count==op.len && op.count==int32(op.end - op.start) && uint64(op.end) < f.datalen {
		copy(f.data[op.start:], buf)
		gostor.Forget(f.root)
		f.root = newroot
		return nil
	}

	newdata := make([]byte, filesz)

	// copy left side
	n := op.start
	if n > int64(f.datalen) {
		n = int64(f.datalen)
	}
	copy(newdata, f.data[0:n])

	// copy new data
	copy(newdata[op.start:], buf)

	// copy right side
	n = op.end
	if n < int64(f.datalen) {
		copy(newdata[op.start + int64(op.count):], f.data[op.end:])
	}

	f.datalen = uint64(len(newdata))
	f.data = newdata
	gostor.Forget(f.root)
	f.root = newroot
	return err
}

func (f *File) checkFile() error {
//	fmt.Printf("checkFile\n")
	offset := uint64(0)
	buf := make([]byte, 1024*1024)
	for offset < f.datalen {
		n, err, _ := gostor.Read(f.root, offset, buf)
		if err != nil {
			return err
		}

		for i := 0; i < n; i++ {
			if f.data[offset+uint64(i)] != buf[i] {
				return errors.New(fmt.Sprintf("wrong data: offset %d got %x should be %x\n", offset+uint64(i), buf[i:i+2], f.data[offset+uint64(i):offset+uint64(i)+2]))
			}
		}

		offset += uint64(n)
	}

	if offset > f.datalen {
		return errors.New(fmt.Sprintf("can read more data that file size: offset %d filesize %d\n", offset, f.datalen))
	}

	return nil
}

func (f *File) test() (err error) {
	op := new(Op)
	op.op = rand.Intn(Oplast)
	op.start = rand.Int63n(*flen + int64(*oplen) * 2)
	op.end = op.start + rand.Int63n(int64(*oplen))
	op.count = rand.Int31n(int32(*oplen))
	op.len = rand.Int31n(int32(*oplen))

	f.ops = append(f.ops, op)
	switch op.op {
	case Opread:
		op.start = rand.Int63n(*flen + int64(*oplen) * 2)
		op.len = rand.Int31n(int32(*oplen))
		err = f.Read(op)

	case Opwrite:
		op.start = rand.Int63n(*flen)
		op.end = op.start + rand.Int63n(int64(*oplen))
		op.count = rand.Int31n(int32(*oplen))
		op.len = rand.Int31n(int32(*oplen))
		if op.len > op.count {
			op.len = op.count
		}
		err = f.Write(op)
		if err == nil {
			if *checkAll {
				err = f.checkFile()
			}

			if err==nil && *checkSize {
				err = f.checkSize()
			}
		}
	}

	return err
}

func (f *File) checkSize() error {
	sz, err := gostor.Size(f.root)
	if err!=nil {
		return err
	}

	if sz != f.datalen {
		return errors.New(fmt.Sprintf("incorrect file size: %d expected %d\n", sz, f.datalen))
	}

	return nil
}

func testloop() (err error) {
	f := new(File)
	for i := 0; i < *numop; i++ {
		if i==*dbgAt {
			gostor.SetDebug(true)
		}

		err = f.test()
		if err!=nil {
			break
		}
		
		if i==*dbgAt {
			gostor.SetDebug(false)
		}

		if rand.Intn(32) == 0 {
			err = f.checkSize()
			if err!=nil {
				break
			}
		}
	}

	if err==nil {
		testc <- true
		return nil
	}

	for i, op := range f.ops {
		nm := ""
		if op.op == Opread {
			nm = "READ"
		} else {
			nm = "WRITE"
		}

		fmt.Printf("%d. %s start %d end %d count %d len %d\n", i, nm, op.start, op.end, op.count, op.len)
		if op.op == Opwrite {
			fmt.Printf("    %s", op.dump)
		}
	}

	fmt.Printf("Error: %s\n", err)
	testc <- true
	gostor.Sync(f.root)
	return err
}

func main() {
	var err error

	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Printf("disk name expected\n")
		return
	}

	err = gostor.Init(flag.Arg(0))
	if err != nil {
		fmt.Printf("Error: %s\n", err)
		return
	}

	testc = make(chan bool)
	for i:=0; i < *threadnum; i++ {
		go testloop()
	}

	for i:=0; i < *threadnum; i++ {
		_ = <- testc
	}
}
