package main

import "iter"

const accountingBlockRecords = 256

// Historical accounting grows by bounded blocks. Appending a request never
// copies an entire retained history; small models grow their first block
// gradually instead of reserving a full block for one archived request.
type accountingBlocks struct {
	blocks [][]accountingRecord
	shared []bool // frozen snapshots may still reference these block arrays
	count  int
}

func (b *accountingBlocks) at(i int) *accountingRecord {
	return &b.blocks[i/accountingBlockRecords][i%accountingBlockRecords]
}

func (b *accountingBlocks) writableBlock(index int) []accountingRecord {
	if index < len(b.shared) && b.shared[index] {
		block := b.blocks[index]
		copyOfBlock := make([]accountingRecord, len(block), cap(block))
		copy(copyOfBlock, block)
		b.blocks[index] = copyOfBlock
		b.shared[index] = false
	}
	return b.blocks[index]
}

func (b *accountingBlocks) mutableAt(i int) *accountingRecord {
	return &b.writableBlock(i / accountingBlockRecords)[i%accountingBlockRecords]
}

// Copy only slice headers under the statistics lock. Appends outside a frozen
// header's length are safe; updates and truncation copy a shared block first.
func (b *accountingBlocks) freeze() [][]accountingRecord {
	if b.count == 0 {
		return nil
	}
	if len(b.shared) < len(b.blocks) {
		b.shared = append(b.shared, make([]bool, len(b.blocks)-len(b.shared))...)
	}
	for i := range b.blocks {
		b.shared[i] = true
	}
	return append([][]accountingRecord(nil), b.blocks...)
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
		if last < len(b.shared) {
			b.shared[last] = false
		}
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
	last := b.writableBlock(blocks - 1)
	end := count - (blocks-1)*accountingBlockRecords
	clear(last[end:])
	b.blocks[blocks-1] = last[:end]
	clear(b.blocks[blocks:])
	b.blocks = b.blocks[:blocks]
	if len(b.shared) > blocks {
		b.shared = b.shared[:blocks]
	}
	b.count = count
	if cap(b.blocks) >= 1024 && len(b.blocks) <= cap(b.blocks)/4 {
		compact := make([][]accountingRecord, len(b.blocks))
		copy(compact, b.blocks)
		b.blocks = compact
	}
}

func (b *accountingBlocks) remove(i int) {
	for next := i + 1; next < b.count; next++ {
		*b.mutableAt(next - 1) = *b.at(next)
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
