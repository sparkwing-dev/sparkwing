package nodeclient

import "time"

// SetRetryBudget shortens c's retry budget so a test can reach it.
func SetRetryBudget(c *Client, d time.Duration) { c.budget = d }
