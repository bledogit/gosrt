package runtime

import (
	"sync"
	"time"
)

// PollDesc - Network poller descriptor.
type PollDesc interface {
	Close()
	Wait(mode int) int
	Reset(mode int) int
	SetDeadline(d time.Duration, mode int)
	Unblock()
}

type pollDesc struct {
	lock    sync.Mutex // protects the following fields
	fd      int
	closing bool
	seq     int // protects from stale timers and ready notifications
	rl      sync.Mutex
	rc      *sync.Cond
	rt      *time.Timer   // read deadline timer
	rd      time.Duration // read deadline
	wl      sync.Mutex
	wc      *sync.Cond
	wt      *time.Timer   // write deadline timer
	wd      time.Duration // write deadline
}

// PollServerInit initialize the poller
func PollServerInit() {
	netpollinit()
}

// PollServerShutdown shutdown the pollder
func PollServerShutdown() {
	netpollshutdown()
}

// PollServerDescriptor returns the descriptor being used
func PollServerDescriptor() int {
	return netpolldescriptor()
}

// PollOpen associate fd with pd
func PollOpen(fd int) (PollDesc, int) {
	pd := pollDesc{}
	pd.fd = fd
	pd.closing = false
	pd.seq++
	pd.rl = sync.Mutex{}
	pd.rc = sync.NewCond(&pd.rl)
	pd.wl = sync.Mutex{}
	pd.wc = sync.NewCond(&pd.wl)

	var errno int
	errno = netpollopen(fd, &pd)
	return &pd, errno
}

func (pd *pollDesc) Close() {
	netpollclose(pd.fd)
}

func (pd *pollDesc) Wait(mode int) int {
	err := netpollcheckerr(pd, mode)
	if err != 0 {
		return err
	}
	netpollblock(pd, mode)
	return 0
}

func (pd *pollDesc) Reset(mode int) int {
	err := netpollcheckerr(pd, mode)
	if err != 0 {
		return err
	}
	return 0
}

func (pd *pollDesc) SetDeadline(d time.Duration, mode int) {
	pd.lock.Lock()
	defer pd.lock.Unlock()
	if pd.closing {
		return
	}
	pd.seq++ // invalidate current timers
	// Reset current timers.
	if pd.rt != nil {
		pd.rt.Stop()
		pd.rt = nil
	}
	if pd.wt != nil {
		pd.wt.Stop()
		pd.wt = nil
	}
	// Setup new timers.
	if mode == 'r' || mode == 'r'+'w' {
		pd.rd = d
	}
	if mode == 'w' || mode == 'r'+'w' {
		pd.wd = d
	}
	if pd.rd > 0 && pd.rd == pd.wd {
		pd.rt = time.AfterFunc(pd.rd, func() {
			netpollDeadline(pd, pd.seq)
		})
	} else {
		if pd.rd > 0 {
			pd.rt = time.AfterFunc(pd.rd, func() {
				netpollReadDeadline(pd, pd.seq)
			})
		}
		if pd.wd > 0 {
			pd.rt = time.AfterFunc(pd.wd, func() {
				netpollWriteDeadline(pd, pd.seq)
			})
		}
	}
}

func (pd *pollDesc) Unblock() {
	pd.lock.Lock()
	defer pd.lock.Unlock()
	if pd.closing {
		panic("runtime: unblock on closing polldesc")
	}
	pd.closing = true
	pd.seq++
	netpollunblock(pd, 'r')
	netpollunblock(pd, 'w')
	if pd.rt != nil {
		pd.rt.Stop()
		pd.rt = nil
	}
	if pd.wt != nil {
		pd.wt.Stop()
		pd.wt = nil
	}
}

func netpollready(pd *pollDesc, mode int) {
	if mode == 'r' || mode == 'r'+'w' {
		netpollunblock(pd, 'r')
	}
	if mode == 'w' || mode == 'r'+'w' {
		netpollunblock(pd, 'w')
	}
}

func netpollcheckerr(pd *pollDesc, mode int) int {
	if pd.closing {
		return 1 // errClosing
	}
	if (mode == 'r' && pd.rd < 0) || (mode == 'w' && pd.wd < 0) {
		return 2 // errTimeout
	}
	return 0
}

func netpollblock(pd *pollDesc, mode int) {
	c := pd.rc
	if mode == 'w' {
		c = pd.wc
	}

	c.L.Lock()
	defer c.L.Unlock()
	c.Wait()
}

func netpollunblock(pd *pollDesc, mode int) {
	c := pd.rc
	if mode == 'w' {
		c = pd.wc
	}

	c.Broadcast()
}

func netpolldeadlineimpl(pd *pollDesc, seq int, read, write bool) {
	pd.lock.Lock()
	defer pd.lock.Unlock()
	if seq != pd.seq {
		return
	}
	if read {
		if pd.rd <= 0 || pd.rt == nil {
			panic("runtime: inconsistent read deadline")
		}
		pd.rd = -1
		pd.rt = nil
		netpollunblock(pd, 'r')
	}
	if write {
		if pd.wd <= 0 || pd.wt == nil && !read {
			panic("runtime: inconsistent write deadline")
		}
		pd.wd = -1
		pd.wt = nil
		netpollunblock(pd, 'w')
	}
}

func netpollDeadline(pd *pollDesc, seq int) {
	netpolldeadlineimpl(pd, seq, true, true)
}

func netpollReadDeadline(pd *pollDesc, seq int) {
	netpolldeadlineimpl(pd, seq, true, false)
}

func netpollWriteDeadline(pd *pollDesc, seq int) {
	netpolldeadlineimpl(pd, seq, false, true)
}
