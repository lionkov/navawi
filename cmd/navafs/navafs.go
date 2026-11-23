package main

import (
	"flag"
	"fmt"
	"net/http"
	"log"
	"os"
	"syscall"
	"github.com/lionkov/navawi"
        "github.com/lionkov/go9p/p"
)

var addr = flag.String("addr", ":5640", "network address")
var debug = flag.Int("d", 0, "print debug messages")
var disk *string = flag.String("disk", "godisk", "navawi disk")
var Enoent = &p.Error{Err: "file not found", Errornum: uint32(syscall.ENOENT)}
var fs *Fs

func main() {
	flag.Parse()

	if err := navawi.Init(*disk); err != nil {
		log.Println(err)
		return
	}

	if ok:=navawi.RegisterDataType(btsuper); !ok {
		log.Println("can't register super type")
		return
	}

	if ok:=navawi.RegisterDataType(btdirentry); !ok {
		log.Println("can't register directory entry type")
		return
	}


	fs = new(Fs)
	fs.Dotu = true
	fs.Id = "ufs"
	fs.Debuglevel = *debug
	fs.user = p.OsUsers.Uid2User(os.Geteuid())
	fs.group = p.OsUsers.Gid2Group(os.Getegid())

	if err := ReadSuper(); err != nil {
		fmt.Printf("no super found, trying to create one...\n")
		if err := CreateSuper(); err != nil {
			log.Println(err)
			return
		}
	}

	fs.Start(fs)
	go http.ListenAndServe(":6060", nil)
	err := fs.StartNetListener("tcp", *addr)
	if err != nil {
		log.Println(err)
	}
}
