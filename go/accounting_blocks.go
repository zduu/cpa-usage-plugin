package main

import "iter"

const accountingBlockRecords = 256

// Historical accounting grows by bounded blocks. Appending a request never
// copies an entire retained history; small models grow their first block
// gradually instead of reserving a full block for one archived request.
type accountingBlocks struct {
	blocks [][]accountingRecord
	count  int
}

func (b *accountingBlocks) at(i int) *accountingRecord {
	return &b.blocks[i/accountingBlockRecords][i%accountingBlockRecords]
}

func (b *accountingBlocks) append(record accountingRecord) {
	if len(b.blocks) == 0 {
		b.blocks = append(b.blocks, make([]accountingRecord, 0, 1))
	}
	last := len(b.blocks) - 1
	block := b.blocks[last]
	if len(block) == accountingBlockRecords {
		b.blocks = append(b.blocks, make([]accountingRecord, 0, accountingBlockRecords))
		last++
		block = b.blocks[last]
	} else if len(block) == cap(block) {
		grown := make([]accountingRecord, len(block), min(accountingBlockRecords, max(1, cap(block)*2)))
		copy(grown, block)
		block = grown
	}
	b.blocks[last] = append(block, record)
	b.count++
}

func (b *accountingBlocks) records() iter.Seq[accountingRecord] {
	return func(yield func(accountingRecord) bool) {
		for _, block := range b.blocks {
			for _, record := range block {
				if !yield(record) {
					return
				}
			}
		}
	}
}

// Used after in-place expiry compaction and repair. Clear only the retained
// tail; whole discarded blocks can be released without touching their rows.
func (b *accountingBlocks) truncate(count int) {
	if count == 0 {
		*b = accountingBlocks{}
		return
	}
	blocks := (count + accountingBlockRecords - 1) / accountingBlockRecords
	last := b.blocks[blocks-1]
	end := count - (blocks-1)*accountingBlockRecords
	clear(last[end:])
	b.blocks[blocks-1] = last[:end]
	clear(b.blocks[blocks:])
	b.blocks = b.blocks[:blocks]
	b.count = count
	if cap(b.blocks) >= 1024 && len(b.blocks) <= cap(b.blocks)/4 {
		compact := make([][]accountingRecord, len(b.blocks))
		copy(compact, b.blocks)
		b.blocks = compact
	}
}

func (b *accountingBlocks) remove(i int) {
	for next := i + 1; next < b.count; next++ {
		*b.at(next - 1) = *b.at(next)
	}
	b.truncate(b.count - 1)
}

func (b *accountingBlocks) capacity() int {
	capacity := 0
	for _, block := range b.blocks {
		capacity += cap(block)
	}
	return capacity
}
