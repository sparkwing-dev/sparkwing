package nodeclient

import "time"

// SetRetryBudget shortens c's retry budget so a test can reach it.
func SetRetryBudget(c *Client, d time.Duration) { c.budget = d }

// SetClock replaces the clock c's retry budget reads.
func SetClock(c *Client, now func() time.Time) { c.now = now }
