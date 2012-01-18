package main

import (
	"errors"
	"flag"
	"fmt"
	"gostor"
	"math"
	"math/rand"
	"net/http"
_	"net/http/pprof"
_	"runtime"
_	"time"
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
	root		uint64
	datalen		uint64
	data		[]byte
	ops 		[]*Op
	readnum		int		// number of read operations
	writenum	int		// number of write operations
	readlen		uint64		// number of bytes read
	writelen	uint64		// number of bytes written
}

var debug = flag.Bool("d", false, "debug output for all operations")
var flen = flag.Int64("l", 262144, "upper bound on file size")
var oplen = flag.Int("o", 65536, "upper bound on operation size")
var numop = flag.Int("N", math.MaxInt32, "total number of operations")
var seed = flag.Int64("S", 1, "seed for random number generator")
var checkAll = flag.Uint("c", math.MaxUint32, "check the content of the whole file after every write past the specified operation number")
var checkSize = flag.Uint("s", math.MaxUint32, "check the file size after each write operation past the specified operation number")
var threadnum = flag.Int("t", 1, "number of op threads")
var dbgFrom = flag.Int("df", -1, "turn on Gostor debugging at the specified operation number")
var dbgTo = flag.Int("dt", -1, "turn off Gostor debugging at the specified operation number")
var dump = flag.Uint("dump", math.MaxUint32, "attach dump to each write operation past the specified operation number")
var check = flag.Bool("C", true, "keep copy of the content of the file in memory for correctness checks")

var testc chan error

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
		f.readlen += uint64(count)
	}

	f.readnum++
	if *check {
		// check if the content is the same
		for i:=0; i<l; i++ {
			if f.data[int(op.start)+i] != buf[i] {
				return errors.New(fmt.Sprintf("wrong data: offset %d got %x should be %x\n", int(op.start)+i, buf[i], f.data[int(op.start)+i]))
			}
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

	f.writelen += uint64(op.len)
	f.writenum++
	if *check {
		if op.start > int64(f.datalen) {
			for i := uint64(f.datalen); i < uint64(op.start); i++ {
				f.data[i] = 0
			}
		}

		if op.end < int64(f.datalen) {
			copy(f.data[op.start + int64(op.count):], f.data[op.end:])
		}

		copy(f.data[op.start:], buf)
		for i := op.start + int64(op.len); i < op.start+int64(op.count); i++ {
			f.data[i] = 0
		}

		f.datalen = uint64(filesz)
	}

	gostor.Forget(f.root)
	f.root = newroot
	return err
}

func (f *File) checkFile() error {
	if !*check {
		return nil
	}

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

func (f *File) test(id uint) (err error) {
	op := new(Op)
	op.op = rand.Intn(Oplast)
	op.start = rand.Int63n(*flen + int64(*oplen) * 2)
	op.end = op.start + rand.Int63n(int64(*oplen))
	op.count = rand.Int31n(int32(*oplen))
	op.len = rand.Int31n(int32(*oplen))

	if *check {
		f.ops = append(f.ops, op)
	}

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
			if *dump <= id {
				op.dump, err = gostor.Dump(f.root, true)
			}

			if err==nil && *checkAll <= id {
//				gostor.Sync(f.root)
				err = f.checkFile()
//				if err!=nil {
//					s, _ := gostor.Dump(f.root)
//					fmt.Printf("## %s\n", s)
//				}
			}

			if err==nil && *checkSize <= id {
				err = f.checkSize()
			}
		}
	}

	return err
}

func (f *File) checkSize() error {
	if !*check {
		return nil
	}

	sz, err := gostor.Size(f.root)
	if err!=nil {
		return err
	}

	if sz != f.datalen {
		return errors.New(fmt.Sprintf("incorrect file size: %d expected %d\n", sz, f.datalen))
	}

	return nil
}

func testloop() {
	var err error

	f := new(File)
	if *check {
		f.data = make([]byte, *flen)
	}

	for i := 0; i < *numop; i++ {
		if i==*dbgFrom {
			gostor.SetDebug(true)
		}

		err = f.test(uint(i))
		if err!=nil {
			break
		}
		
		if i==*dbgTo {
			gostor.SetDebug(false)
		}

		if rand.Intn(32) == 0 {
			err = f.checkSize()
			if err!=nil {
				break
			}
		}
	}

	gostor.Sync(f.root)
	if err==nil {
		err = f.checkFile()
	}

	if err==nil {
		err = f.checkSize()
	}

	if err==nil {
		id, _ := gostor.Id(f.root)
		fmt.Printf("Root %d\n", id)
		fmt.Printf("Reads %d (%d bytes) Writes %d (%d bytes)\n", f.readnum, f.readlen, f.writenum, f.writelen)
		testc <- nil
		return
	}

	for i, op := range f.ops {
		nm := ""
		if op.op == Opread {
			nm = "READ"
		} else {
			nm = "WRITE"
		}

		fmt.Printf("%d. %s start %d end %d count %d len %d\n", i, nm, op.start, op.end, op.count, op.len)
		if op.op == Opwrite && op.dump != "" {
			fmt.Printf("    %s", op.dump)
		}
	}

	fmt.Printf("Error: %s\n", err)
	testc <- err
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

	go http.ListenAndServe(":6060", nil)
	testc = make(chan error)
	for i:=0; i < *threadnum; i++ {
		go testloop()
	}

	for i:=0; i < *threadnum; i++ {
		_ = <- testc
	}

/*
	fmt.Printf("Before GC\n")
	runtime.GC()
	fmt.Printf("Before Stats\n")
	fmt.Printf("%s", gostor.Stats())
	for {
		time.Sleep(1000000000)
	}
*/
}
