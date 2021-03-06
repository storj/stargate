// Copyright (C) 2020 Storj Labs, Inc.
// See LICENSE for copying information.

package memauth

import (
	"context"
	"sync"

	"github.com/spacemonkeygo/monkit/v3"
	"github.com/zeebo/errs"

	"storj.io/gateway-mt/auth"
)

var mon = monkit.Package()

// KV is a key/value store backed by an in memory map.
type KV struct {
	mu      sync.Mutex
	entries map[auth.KeyHash]*auth.Record
	invalid map[auth.KeyHash]string
}

// New constructs a KV.
func New() *KV {
	return &KV{
		entries: make(map[auth.KeyHash]*auth.Record),
		invalid: make(map[auth.KeyHash]string),
	}
}

// Put stores the record in the key/value store.
// It is an error if the key already exists.
func (d *KV) Put(ctx context.Context, keyHash auth.KeyHash, record *auth.Record) (err error) {
	defer mon.Task()(&ctx)(&err)

	d.mu.Lock()
	defer d.mu.Unlock()

	if _, ok := d.entries[keyHash]; ok {
		return errs.New("record already exists")
	}

	d.entries[keyHash] = record
	return nil
}

// Get retrieves the record from the key/value store.
// It returns nil if the key does not exist.
func (d *KV) Get(ctx context.Context, keyHash auth.KeyHash) (record *auth.Record, err error) {
	defer mon.Task()(&ctx)(&err)

	d.mu.Lock()
	defer d.mu.Unlock()

	if reason, ok := d.invalid[keyHash]; ok {
		return nil, auth.Invalid.New("%s", reason)
	}

	return d.entries[keyHash], nil
}

// Delete removes the record from the key/value store.
// It is not an error if the key does not exist.
func (d *KV) Delete(ctx context.Context, keyHash auth.KeyHash) (err error) {
	defer mon.Task()(&ctx)(&err)

	d.mu.Lock()
	defer d.mu.Unlock()

	delete(d.entries, keyHash)
	delete(d.invalid, keyHash)
	return nil
}

// Invalidate causes the record to become invalid.
// It is not an error if the key does not exist.
// It does not update the invalid reason if the record is already invalid.
func (d *KV) Invalidate(ctx context.Context, keyHash auth.KeyHash, reason string) (err error) {
	defer mon.Task()(&ctx)(&err)

	d.mu.Lock()
	defer d.mu.Unlock()

	if _, ok := d.invalid[keyHash]; !ok {
		d.invalid[keyHash] = reason
	}

	return nil
}

// Ping attempts to do a database roundtrip and returns an error if it can't.
func (d *KV) Ping(ctx context.Context) (err error) {
	defer mon.Task()(&ctx)(&err)

	d.mu.Lock()
	defer d.mu.Unlock()

	return nil
}
