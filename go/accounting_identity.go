package main

import "hash/maphash"

// This multi-string identity is an indirect Go map key: storing it as
// both a map key and an interned value duplicates every identity. Keep one
// value behind a small hash key. Equality, including collisions, remains exact.
type accountingIdentityEntry struct {
	value accountingIdentity
	next  *accountingIdentityEntry
}

type accountingIdentityIndex map[uint64]*accountingIdentityEntry

var accountingIdentitySeed = maphash.MakeSeed()

func (index accountingIdentityIndex) intern(key accountingIdentity, hash uint64) *accountingIdentity {
	for entry := index[hash]; entry != nil; entry = entry.next {
		if entry.value == key {
			return &entry.value
		}
	}
	entry := &accountingIdentityEntry{value: key, next: index[hash]}
	index[hash] = entry
	return &entry.value
}

func (m *modelStats) internAccountingIdentity(key accountingIdentity) *accountingIdentity {
	if m.accountingIdentities == nil {
		m.accountingIdentities = make(accountingIdentityIndex)
	}
	return m.accountingIdentities.intern(key, maphash.Comparable(accountingIdentitySeed, key))
}
