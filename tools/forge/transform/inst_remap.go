package transform

import (
	"forge/analysis"
	"sort"
)

type instFreq struct {
	idx   int
	count int
}

// BuildInstRemap builds instrument remapping from analysis
func BuildInstRemap(anal analysis.SongAnalysis, numInst int) ([]int, int) {
	var used []instFreq
	for i := 1; i < numInst; i++ {
		if count, ok := anal.InstrumentFreq[i]; ok && count > 0 {
			used = append(used, instFreq{i, count})
		}
	}

	sort.Slice(used, func(a, b int) bool {
		if used[a].count != used[b].count {
			return used[a].count > used[b].count
		}
		return used[a].idx < used[b].idx
	})

	remap := make([]int, numInst)
	slotUsed := make([]bool, numInst)
	slotUsed[0] = true

	var filterTriggerInsts []instFreq
	var otherInsts []instFreq

	for _, u := range used {
		if anal.FilterTriggerInst[u.idx] {
			filterTriggerInsts = append(filterTriggerInsts, u)
		} else {
			otherInsts = append(otherInsts, u)
		}
	}

	nextSlot := 1
	for _, u := range filterTriggerInsts {
		if remap[u.idx] != 0 {
			continue
		}
		for nextSlot < len(slotUsed) && slotUsed[nextSlot] {
			nextSlot++
		}
		if nextSlot < len(slotUsed) {
			remap[u.idx] = nextSlot
			slotUsed[nextSlot] = true
			nextSlot++
		}
	}

	for _, u := range otherInsts {
		if remap[u.idx] != 0 {
			continue
		}
		for nextSlot < len(slotUsed) && slotUsed[nextSlot] {
			nextSlot++
		}
		if nextSlot < len(slotUsed) {
			remap[u.idx] = nextSlot
			slotUsed[nextSlot] = true
			nextSlot++
		}
	}

	for i := 1; i < numInst; i++ {
		if remap[i] != 0 {
			continue
		}
		for nextSlot < len(slotUsed) && slotUsed[nextSlot] {
			nextSlot++
		}
		if nextSlot < len(slotUsed) {
			remap[i] = nextSlot
			slotUsed[nextSlot] = true
			nextSlot++
		}
	}

	maxUsedSlot := 0
	for i := 1; i < numInst; i++ {
		if count, ok := anal.InstrumentFreq[i]; ok && count > 0 {
			if remap[i] > maxUsedSlot {
				maxUsedSlot = remap[i]
			}
		}
	}

	return remap, maxUsedSlot
}
