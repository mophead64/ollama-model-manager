package library

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Health is whether browsing ollama.com is working, from the last search or
// tags page read there (by someone using Discover, or WatchHealth). It's
// "broken" when ollama.com can't be reached or read, which is most likely
// its layout changing under the parser: that can leave results with nothing
// but their names rather than failing outright, so results missing their
// details count too.
type Health struct {
	Broken  bool
	Reason  string    // why, when Broken
	Since   time.Time // when it broke
	Checked time.Time // when it was last known either way; zero before then
}

// Health reports how browsing ollama.com last went.
func (c *Client) Health() Health {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.health
}

// record notes how a read of ollama.com went: err nil for a good one. A
// request the caller gave up on (its context ended) says nothing either way,
// and nor does a page that doesn't exist.
func (c *Client) record(ctx context.Context, err error) {
	if ctx.Err() != nil || errors.Is(err, ErrNotFound) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	h := &c.health
	h.Checked = now
	if err == nil {
		h.Broken, h.Reason, h.Since = false, "", time.Time{}
		return
	}
	if !h.Broken {
		h.Since = now
	}
	h.Broken, h.Reason = true, err.Error()
}

// errMissingDetails is a search whose results came back with their names
// but nothing else: the markup around them has changed.
var errMissingDetails = fmt.Errorf("%w: results are missing their descriptions and details", ErrUnreadable)

// missingDetails reports whether a page of results looks read wrongly: none
// of several results has a description, which every library model has. With
// full set, the page is the library's unfiltered first page, which always
// has local models, so their sizes and pull counts must be there too.
func missingDetails(p Page, full bool) bool {
	if len(p.Models) < 3 {
		return false
	}
	var desc, sizes, pulls bool
	for _, m := range p.Models {
		desc = desc || m.Description != ""
		sizes = sizes || len(m.Sizes) > 0
		pulls = pulls || m.Pulls != ""
	}
	return !desc || full && (!sizes || !pulls)
}

// healthCheckEvery is how often WatchHealth looks at ollama.com.
const healthCheckEvery = 6 * time.Hour

// WatchHealth checks browsing ollama.com works now and every few hours,
// until ctx is cancelled, so it's known to be broken before anyone tries:
// the library's first page, its second (which is fetched differently), and
// the first model's tags. Each request is the same one Discover would make,
// and cached the same way.
func (c *Client) WatchHealth(ctx context.Context) {
	tick := time.NewTicker(healthCheckEvery)
	defer tick.Stop()
	for {
		c.checkHealth(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (c *Client) checkHealth(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	first, err := c.Search(ctx, Query{})
	if err != nil {
		return // recorded
	}
	if _, err := c.Search(ctx, Query{Page: 2}); err != nil {
		return
	}
	c.Tags(ctx, first.Models[0].Name)
}
