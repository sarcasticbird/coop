package runtime

import "sync"

// BoundedCapture consumes the entire stream, retaining only its first max bytes.
// Unlike LimitWriter, it never signals a short write when the retention cap is hit.
type BoundedCapture struct {
	mu    sync.Mutex
	max   int64
	data  []byte
	total int64
}

func NewBoundedCapture(max int64) *BoundedCapture {
	if max < 0 {
		max = 0
	}
	return &BoundedCapture{max: max}
}
func (b *BoundedCapture) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	remaining := b.max - int64(len(b.data))
	if remaining > 0 {
		keep := min(int64(n), remaining)
		b.data = append(b.data, p[:keep]...)
	}
	b.total += int64(n)
	return n, nil
}
func (b *BoundedCapture) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.data...)
}
func (b *BoundedCapture) String() string { return string(b.Bytes()) }
func (b *BoundedCapture) Total() int64   { b.mu.Lock(); defer b.mu.Unlock(); return b.total }
func (b *BoundedCapture) Dropped() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.total - int64(len(b.data))
}
