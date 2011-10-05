package main

import (
	"flag"
	"fmt"
	"os"

	"gostor"
)

var disk *string = flag.String("disk", "gostor", "gostor disk")

func main() {
	var err os.Error
	var d *os.File

	flag.Parse()
	var fi *os.FileInfo

	d, err = os.OpenFile(*disk, os.O_RDWR, 0666)
	if err!=nil {
		goto error
	}

	fi, err = d.Stat()
	if err!=nil {
		goto error
	}

	err = gostor.FormatDisk(d, uint64(fi.Size), gostor.Conf.Segmentsz)
	if err!=nil {
		goto error
	}

	return

error:
	fmt.Printf("Error: %v\n", err)
	return
}
