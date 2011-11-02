package main

import (
	"fmt"
	"go9p.googlecode.com/hg/p"
	"go9p.googlecode.com/hg/p/srv"
	"log"
	"os"
	"syscall"
)

type Fs struct {
	srv.Srv
	user  p.User
	group p.Group
}

type Fid struct {
	file      *File
	diroffset uint64
}

func toError(err error) *p.Error {
	var ecode os.Errno

	ename := err.Error()
	if e, ok := err.(os.Errno); ok {
		ecode = e
	} else {
		ecode = syscall.EIO
	}

	return &p.Error{ename, int(ecode)}
}

func (*Fs) ConnOpened(conn *srv.Conn) {
	if conn.Srv.Debuglevel > 0 {
		log.Println("connected")
	}
}

func (*Fs) ConnClosed(conn *srv.Conn) {
	if conn.Srv.Debuglevel > 0 {
		log.Println("disconnected")
	}
}

func (*Fs) FidDestroy(sfid *srv.Fid) {
	var fid *Fid

	if sfid.Aux == nil {
		return
	}

	fid = sfid.Aux.(*Fid)
	if fid.file != nil {
		fid.file.Unref()
	}
}

func (*Fs) Attach(req *srv.Req) {
	if req.Afid != nil {
		req.RespondError(srv.Enoauth)
		return
	}

	fid := new(Fid)
	fid.file = super.root
	fid.file.Ref()
	req.Fid.Aux = fid
	req.RespondRattach(&fid.file.Qid)
}

func (*Fs) Flush(req *srv.Req) {}

func (*Fs) Walk(req *srv.Req) {
	fid := req.Fid.Aux.(*Fid)
	tc := req.Tc

	if req.Newfid.Aux == nil {
		req.Newfid.Aux = new(Fid)
	}

	nfid := req.Newfid.Aux.(*Fid)
	nfid.file.Ref()
	wqids := make([]p.Qid, len(tc.Wname))
	i := 0
	f := fid.file
	f.Ref()
	for ; i < len(tc.Wname); i++ {
		fn, err := f.Find(tc.Wname[i])
		if err != nil {
			break
		}

		f.Unref()
		f = fn
		wqids[i] = f.Qid
	}

	if len(tc.Wname) > 0 && i == 0 {
		f.Unref()
		req.RespondError(Enoent)
		return
	}

	nfid.file.Unref()
	nfid.file = f
	req.RespondRwalk(wqids[0:i])
}

func (*Fs) Open(req *srv.Req) {
	fid := req.Fid.Aux.(*Fid)
	req.RespondRopen(&fid.file.Qid, 0)
}

func (*Fs) Create(req *srv.Req) {
	var dir *File

	fid := req.Fid.Aux.(*Fid)
	tc := req.Tc

	dir = fid.file
	user := req.Fid.User
	f, err := dir.Create(tc.Name, tc.Perm, user, fs.group, tc.Ext)
	if err != nil {
		req.RespondError(toError(err))
		return
	}

	fid.file.Unref()
	fid.file = f

	req.RespondRcreate(&fid.file.Qid, 0)
}

func (*Fs) Read(req *srv.Req) {
	var fi FileInfo
	var err error

	fid := req.Fid.Aux.(*Fid)
	tc := req.Tc
	rc := req.Rc
	p.InitRread(rc, tc.Count)

	count := 0
	if fid.file.Mode&p.DMDIR != 0 {
		offset := tc.Offset
		if tc.Offset != 0 {
			offset = fid.diroffset
		}

		n, err := fid.file.Read(offset, rc.Data)
		if err != nil {
			req.RespondError(toError(err))
			return
		}

		elen := 0
		for f, t := rc.Data[0:n], rc.Data[0:n]; len(f) > 0; {
			decodeEntry(f)
			f = decodeFileInfo(f, &fi)
			fmt.Printf("9P Read: name %s entrySize %d\n", fi.Name, fi.entrySize)
			n := p.PackDir(&fi.Dir, t, req.Conn.Dotu)
			if n == 0 {
				break
			}

			t = t[n:]
			count += n
			elen += int(sizeFileInfo(&fi))
		}
		fid.diroffset = offset + uint64(elen)
	} else {
		count, err = fid.file.Read(tc.Offset, rc.Data)
		if err != nil {
			req.RespondError(toError(err))
			return
		}
	}

	p.SetRreadCount(rc, uint32(count))
	req.Respond()
}

func (*Fs) Write(req *srv.Req) {
	fid := req.Fid.Aux.(*Fid)
	tc := req.Tc
	n, e := fid.file.Write(tc.Offset, tc.Data)
	if e != nil {
		req.RespondError(toError(e))
		return
	}

	req.RespondRwrite(uint32(n))
}

func (*Fs) Clunk(req *srv.Req) { req.RespondRclunk() }

func (*Fs) Remove(req *srv.Req) {
	fid := req.Fid.Aux.(*Fid)
	e := fid.file.Remove()
	if e != nil {
		req.RespondError(toError(e))
		return
	}

	req.RespondRremove()
}

func (*Fs) Stat(req *srv.Req) {
	fid := req.Fid.Aux.(*Fid)
	req.RespondRstat(&fid.file.Dir)
}

func (*Fs) Wstat(req *srv.Req) {
	var uid p.User
	var gid p.Group

	fid := req.Fid.Aux.(*Fid)
	dir := &req.Tc.Dir
	fi := fid.file.FileInfo
	up := req.Conn.Srv.Upool
	if req.Conn.Dotu {
		uid = up.Uid2User(int(dir.Uidnum))
		gid = up.Gid2Group(int(dir.Gidnum))
	} else {
		uid = nil
		gid = nil
	}

	if uid == nil && dir.Uid != "" {
		uid = up.Uname2User(dir.Uid)
		if uid == nil {
			req.RespondError(srv.Enouser)
			return
		}
	}

	if gid == nil && dir.Gid != "" {
		gid = up.Gname2Group(dir.Gid)
		if gid == nil {
			req.RespondError(srv.Enouser)
			return
		}
	}

	if dir.Mode != 0xFFFFFFFF {
		fi.Mode = dir.Mode
	}

	if uid != nil {
		fi.Uid = gid.Name()
		fi.Uidnum = uint32(uid.Id())
	}

	if gid != nil {
		fi.Gid = gid.Name()
		fi.Gidnum = uint32(gid.Id())
	}

	if dir.Length != 0xFFFFFFFFFFFFFFFF {
		fi.Length = dir.Length
	}

	if dir.Name != "" {
		fi.Name = dir.Name
	}

	err := fid.file.Wstat(&fi)
	if err != nil {
		req.RespondError(toError(err))
	} else {
		req.RespondRwstat()
	}
}
