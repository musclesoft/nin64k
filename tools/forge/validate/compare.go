package validate

import "bytes"

func SerializeWrites(writes []SIDWrite) []byte {
	out := make([]byte, len(writes)*3)
	for i, w := range writes {
		out[i*3] = byte(w.Addr)
		out[i*3+1] = byte(w.Addr >> 8)
		out[i*3+2] = w.Value
	}
	return out
}

func CompareRuns(origWrites, newWrites []SIDWrite) bool {
	return bytes.Equal(SerializeWrites(origWrites), SerializeWrites(newWrites))
}

type CompareResult struct {
	Passed       bool
	OrigWrites   int
	NewWrites    int
	FirstMismatch int
}

func Compare(origData, newData []byte, bufferBase uint16, frames int) CompareResult {
	playAddr := bufferBase + 3

	cpuOrig := NewCPU()
	copy(cpuOrig.Memory[bufferBase:], origData)
	cpuOrig.A = 0
	cpuOrig.Call(bufferBase)
	origWrites := cpuOrig.RunFrames(playAddr, frames)

	cpuNew := NewCPU()
	copy(cpuNew.Memory[bufferBase:], newData)
	cpuNew.A = 0
	cpuNew.Call(bufferBase)
	newWrites := cpuNew.RunFrames(playAddr, frames)

	result := CompareResult{
		OrigWrites: len(origWrites),
		NewWrites:  len(newWrites),
		FirstMismatch: -1,
	}

	if CompareRuns(origWrites, newWrites) {
		result.Passed = true
		return result
	}

	for i := 0; i < len(origWrites) && i < len(newWrites); i++ {
		if origWrites[i] != newWrites[i] {
			result.FirstMismatch = i
			break
		}
	}
	if result.FirstMismatch == -1 && len(origWrites) != len(newWrites) {
		if len(origWrites) < len(newWrites) {
			result.FirstMismatch = len(origWrites)
		} else {
			result.FirstMismatch = len(newWrites)
		}
	}

	return result
}
