package egress

import "time"

// setClock replaces a meter's clock so a test can roll a window without
// waiting for one.
func setClock(m *Meter, now func() time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.now = now
}
