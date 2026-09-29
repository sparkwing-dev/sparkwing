package sparkwing

// Checkout is what a run's nodes ask of the source checkout a Sparkwing
// Cloud Job prepares before any pipeline code runs. The zero value is the
// default: one commit, no tags, no submodules, and LFS files left as pointers.
type Checkout struct {
	// Depth is how many commits of history to fetch; zero fetches one.
	Depth int
	// FullHistory fetches every commit and overrides Depth.
	FullHistory bool
	// Tags fetches the repository's tags.
	Tags bool
	// Submodules checks out submodules, which must live in repositories the
	// team's owner approved for the GitHub App.
	Submodules bool
	// LFS fetches Git LFS objects in place of their pointers.
	LFS bool
}

// Checkout sets the source checkout every node of a controller-dispatched
// Cloud run gets. It has no effect on a local run, which uses the working
// tree as it is.
//
//	plan.Checkout(sparkwing.Checkout{Depth: 50, Tags: true})
func (p *Plan) Checkout(c Checkout) *Plan {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.checkout = &c
	return p
}

// CheckoutValue returns what Checkout set, or nil for the default.
func (p *Plan) CheckoutValue() *Checkout {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.checkout == nil {
		return nil
	}
	c := *p.checkout
	return &c
}
