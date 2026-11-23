package navawi

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// The package keeps its state in globals and Init registers HTTP
// handlers, so a test binary can initialize it only once.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "navawi-test")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	err = initTestDisk(filepath.Join(dir, "disk"), 256*1024*1024)
	code := 1
	if err == nil {
		code = m.Run()
	} else {
		fmt.Fprintln(os.Stderr, err)
	}

	os.RemoveAll(dir)
	os.Exit(code)
}

func initTestDisk(name string, size int64) error {
	d, err := os.Create(name)
	if err != nil {
		return err
	}

	err = d.Truncate(size)
	if err == nil {
		err = FormatDisk(d, uint64(size), Conf.Segmentsz)
	}

	if cerr := d.Close(); err == nil {
		err = cerr
	}

	if err != nil {
		return err
	}

	return Init(name)
}

// A root must stay resolvable under the ID Write returned until it is
// forgotten, even after commits renumber it, and Forget must clear its
// live mark. Each writer keeps its current root live until its next
// write, so commits renumber live roots; the small commit threshold makes
// commits frequent. An expired ID panics in Get; a missed mark stays in
// liveblocks.
func TestLiveRoots(t *testing.T) {
	const (
		threads = 8
		writes  = 1000
		datasz  = 4096
	)

	bc.Lock()
	maxucsize := bc.maxucsize
	bc.maxucsize = 16 * 1024
	bc.Unlock()
	defer func() {
		bc.Lock()
		bc.maxucsize = maxucsize
		bc.Unlock()
	}()

	var wg sync.WaitGroup
	errs := make(chan error, threads)
	for i := 0; i < threads; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			data := make([]byte, datasz)
			root := uint64(0)
			for j := 0; j < writes; j++ {
				newroot, err := Write(root, 0, datasz, datasz, data, BTData)
				if err == nil && root != 0 {
					err = Forget(root)
				}

				if err != nil {
					errs <- err
					return
				}

				root = newroot
			}

			errs <- Forget(root)
		}()
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	bc.Lock()
	n := len(bc.liveblocks)
	bc.Unlock()
	if n != 0 {
		t.Errorf("%d live marks remain after every root was forgotten", n)
	}
}
