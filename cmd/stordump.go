package main

import (
	"flag"
	"fmt"
	"gostor"
)

var root = flag.Int64("r", 0, "file id")

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

	s, err := gostor.Dump(uint64(*root), true)
	fmt.Printf("%s", s)
}
