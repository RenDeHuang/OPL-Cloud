package ent

import "entgo.io/ent/dialect"

// Driver exposes the configured driver to repository-owned transactional helpers.
func (c *Client) Driver() dialect.Driver { return c.driver }

// Driver exposes the transaction driver for one atomic store operation.
func (tx *Tx) Driver() dialect.Tx { return tx.config.driver.(*txDriver).tx }
