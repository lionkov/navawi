package main

import (
	"flag"
	"fmt"
	"os"

	"gostor"
)

var disk *string = flag.String("disk", "gostor", "gostor disk")
var size = flag.Uint64("size", 0, "disk size")

func main() {
	var err error
	var d *os.File

	flag.Parse()
	sz := *size

	d, err = os.OpenFile(*disk, os.O_RDWR, 0666)
	if err!=nil {
		goto error
	}

	if sz==0 {
		var fi os.FileInfo
		fi, err = d.Stat()
		if err!=nil {
			goto error
		}

		sz = uint64(fi.Size())
	}

	err = gostor.FormatDisk(d, sz, gostor.Conf.Segmentsz)
	if err!=nil {
		goto error
	}

	return

error:
	fmt.Printf("Error: %v\n", err)
	return
}
