package main

import (
	"flag"
	"http"
	"log"
	"os"
	"syscall"
	"go9p.googlecode.com/hg/p"
//	"go9p.googlecode.com/hg/p/srv"
)

var addr = flag.String("addr", ":5640", "network address")
var debug = flag.Int("d", 0, "print debug messages")
var Enoent = &p.Error{"file not found", syscall.ENOENT}
var fs *Fs

func main() {
	flag.Parse()
	fs = new(Fs)
	fs.Dotu = true
	fs.Id = "ufs"
	fs.Debuglevel = *debug
	fs.user = p.OsUsers.Uid2User(os.Geteuid())
	fs.group = p.OsUsers.Gid2Group(os.Getegid())
	fs.Start(fs)
	go http.ListenAndServe(":6060", nil)
	err := fs.StartNetListener("tcp", *addr)
	if err != nil {
		log.Println(err)
	}
}
