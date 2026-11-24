package storage

import (
	"runtime"
	"sync"
)

// gate lets many readers share the database, or one writer hold it until
// commit or rollback. The same goroutine can enter again without deadlocking.
type gate struct {
	mu      sync.Mutex
	cond    *sync.Cond
	writer  int64
	depth   int
	readers int
	waiting int
}

// recMutex keeps buffer-pool maps safe when several readers fetch at once.
type recMutex struct {
	mu    sync.Mutex
	cond  *sync.Cond
	owner int64
	depth int
}

func (g *gate) lock() {
	id := goid()
	g.mu.Lock()
	if g.writer == id {
		g.depth++
		g.mu.Unlock()
		return
	}
	g.waiting++
	for g.writer != 0 || g.readers > 0 {
		g.cond.Wait()
	}
	g.waiting--
	g.writer = id
	g.depth = 1
	g.mu.Unlock()
}

func (g *gate) unlock() {
	g.mu.Lock()
	if g.writer != goid() || g.depth == 0 {
		g.mu.Unlock()
		panic("database lock is not held")
	}
	g.depth--
	if g.depth == 0 {
		g.writer = 0
		g.cond.Broadcast()
	}
	g.mu.Unlock()
}

func (g *gate) rlock() {
	id := goid()
	g.mu.Lock()
	if g.writer == id {
		g.depth++
		g.mu.Unlock()
		return
	}
	for g.writer != 0 || g.waiting > 0 {
		g.cond.Wait()
	}
	g.readers++
	g.mu.Unlock()
}

func (g *gate) runlock() {
	g.mu.Lock()
	if g.writer == goid() {
		if g.depth == 0 {
			g.mu.Unlock()
			panic("database lock is not held")
		}
		g.depth--
		if g.depth == 0 {
			g.writer = 0
			g.cond.Broadcast()
		}
		g.mu.Unlock()
		return
	}
	if g.readers == 0 {
		g.mu.Unlock()
		panic("database lock is not held")
	}
	g.readers--
	if g.readers == 0 {
		g.cond.Broadcast()
	}
	g.mu.Unlock()
}

func (g *gate) writing() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.writer == goid() && g.depth > 0
}

func (r *recMutex) Lock() {
	id := goid()
	r.mu.Lock()
	for r.owner != 0 && r.owner != id {
		r.cond.Wait()
	}
	r.owner = id
	r.depth++
	r.mu.Unlock()
}

func (r *recMutex) Unlock() {
	r.mu.Lock()
	if r.owner != goid() || r.depth == 0 {
		r.mu.Unlock()
		panic("database lock is not held")
	}
	r.depth--
	if r.depth == 0 {
		r.owner = 0
		r.cond.Broadcast()
	}
	r.mu.Unlock()
}

func goid() int64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	const prefix = "goroutine "
	if n < len(prefix) {
		panic("database lock is not held")
	}
	var id int64
	for i := len(prefix); i < n && buf[i] >= '0' && buf[i] <= '9'; i++ {
		id = id*10 + int64(buf[i]-'0')
	}
	if id == 0 {
		panic("database lock is not held")
	}
	return id
}

// LockWrite holds the database for one writer until UnlockWrite.
func (p *Pool) LockWrite() { p.gate.lock() }

// UnlockWrite releases the writer lock.
func (p *Pool) UnlockWrite() { p.gate.unlock() }

// LockRead holds the database alongside other readers.
func (p *Pool) LockRead() { p.gate.rlock() }

// UnlockRead releases one reader.
func (p *Pool) UnlockRead() { p.gate.runlock() }

// Writing reports whether this goroutine holds the writer lock.
func (p *Pool) Writing() bool { return p.gate.writing() }

// Guard locks for a single call. The returned function releases that lock.
// A goroutine that already holds the writer lock is left unchanged.
func (p *Pool) Guard(write bool) func() {
	if p.gate.writing() {
		return func() {}
	}
	if write {
		p.gate.lock()
		return p.gate.unlock
	}
	p.gate.rlock()
	return p.gate.runlock
}
