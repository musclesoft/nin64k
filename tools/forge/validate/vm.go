package validate

import "fmt"

type SIDWrite struct {
	Addr  uint16
	Value byte
	Frame int
}

type CPU6502 struct {
	A, X, Y      byte
	SP           byte
	PC           uint16
	P            byte
	Memory       [65536]byte
	SIDWrites    []SIDWrite
	Cycles       uint64
	CurrentFrame int

	Coverage     map[uint16]bool
	DataCoverage map[uint16]bool
	DataBase     uint16
	DataSize     int

	RedundantCLC map[uint16]int
	RedundantSEC map[uint16]int
	TotalCLC     map[uint16]int
	TotalSEC     map[uint16]int

	CheckpointAddr       uint16
	LastCheckpointCycle  uint64
	LastCheckpointCaller uint16
	CheckpointGap        uint64
	CheckpointGapFrom    uint16
	CheckpointGapTo      uint16
}

const (
	FlagC byte = 1 << 0
	FlagZ byte = 1 << 1
	FlagI byte = 1 << 2
	FlagD byte = 1 << 3
	FlagB byte = 1 << 4
	FlagU byte = 1 << 5
	FlagV byte = 1 << 6
	FlagN byte = 1 << 7
)

func NewCPU() *CPU6502 {
	return &CPU6502{SP: 0xFF, P: FlagU | FlagI}
}

func (c *CPU6502) Reset() {
	c.A, c.X, c.Y = 0, 0, 0
	c.SP = 0xFF
	c.P = FlagU | FlagI
	c.SIDWrites = nil
	c.Cycles = 0
}

func (c *CPU6502) Read(addr uint16) byte {
	if c.DataCoverage != nil && addr >= c.DataBase && int(addr-c.DataBase) < c.DataSize {
		c.DataCoverage[addr] = true
	}
	val := c.Memory[addr]
	if DebugReadAddr != 0 && addr >= DebugReadAddr && addr <= DebugReadAddr+DebugReadRange && c.CurrentFrame >= DebugFrameRange[0] && c.CurrentFrame <= DebugFrameRange[1] {
		fmt.Printf("    [ASM f%d] READ $%04X = %02X\n", c.CurrentFrame, addr, val)
	}
	return val
}
func (c *CPU6502) Read16(addr uint16) uint16 {
	return uint16(c.Read(addr)) | uint16(c.Read(addr+1))<<8
}

var DebugSpeedAddr uint16 = 0 // Set to non-zero to debug speed writes
var DebugFrameRange = [2]int{0, 0} // Set to {start, end} to debug frame range
var DebugSIDAddr uint16 = 0 // Set to specific SID register to debug
var DebugMemAddr uint16 = 0 // Debug writes to this memory address
var DebugReadAddr uint16 = 0 // Debug reads from this memory address
var DebugReadRange uint16 = 0 // Debug reads from DebugReadAddr to DebugReadAddr+DebugReadRange

func (c *CPU6502) Write(addr uint16, val byte) {
	if addr >= 0xD400 && addr <= 0xD418 {
		c.SIDWrites = append(c.SIDWrites, SIDWrite{addr, val, c.CurrentFrame})
		if DebugSIDAddr != 0 && addr == DebugSIDAddr && c.CurrentFrame >= DebugFrameRange[0] && c.CurrentFrame <= DebugFrameRange[1] {
			fmt.Printf("    [ASM f%d] $%04X = %02X\n", c.CurrentFrame, addr, val)
		}
	}
	if DebugSpeedAddr != 0 && addr == DebugSpeedAddr && c.Memory[addr] != val {
		fmt.Printf("    [frame %d] Write $%04X = %d (was %d)\n", c.CurrentFrame, addr, val, c.Memory[addr])
	}
	if DebugMemAddr != 0 && addr == DebugMemAddr && c.CurrentFrame >= DebugFrameRange[0] && c.CurrentFrame <= DebugFrameRange[1] {
		fmt.Printf("    [ASM f%d] mem $%04X = %02X (was %02X)\n", c.CurrentFrame, addr, val, c.Memory[addr])
	}
	c.Memory[addr] = val
}

func (c *CPU6502) Push(val byte)   { c.Write(0x0100|uint16(c.SP), val); c.SP-- }
func (c *CPU6502) Pull() byte      { c.SP++; return c.Read(0x0100 | uint16(c.SP)) }
func (c *CPU6502) Push16(val uint16) { c.Push(byte(val >> 8)); c.Push(byte(val)) }
func (c *CPU6502) Pull16() uint16  { return uint16(c.Pull()) | uint16(c.Pull())<<8 }

func (c *CPU6502) setZ(val byte)   { if val == 0 { c.P |= FlagZ } else { c.P &^= FlagZ } }
func (c *CPU6502) setN(val byte)   { if val&0x80 != 0 { c.P |= FlagN } else { c.P &^= FlagN } }
func (c *CPU6502) setZN(val byte)  { c.setZ(val); c.setN(val) }
func (c *CPU6502) getFlag(f byte) bool { return c.P&f != 0 }
func (c *CPU6502) setFlag(f byte, v bool) { if v { c.P |= f } else { c.P &^= f } }

func (c *CPU6502) addrImm() uint16    { addr := c.PC; c.PC++; return addr }
func (c *CPU6502) addrZP() uint16     { addr := uint16(c.Memory[c.PC]); c.PC++; return addr }
func (c *CPU6502) addrZPX() uint16    { addr := uint16(c.Memory[c.PC] + c.X); c.PC++; return addr }
func (c *CPU6502) addrZPY() uint16    { addr := uint16(c.Memory[c.PC] + c.Y); c.PC++; return addr }
func (c *CPU6502) addrAbs() uint16    { lo := uint16(c.Memory[c.PC]); hi := uint16(c.Memory[c.PC+1]); c.PC += 2; return hi<<8 | lo }
func (c *CPU6502) addrAbsX() uint16   { return c.addrAbs() + uint16(c.X) }
func (c *CPU6502) addrAbsY() uint16   { return c.addrAbs() + uint16(c.Y) }
func (c *CPU6502) addrIndX() uint16   { zp := c.Read(c.PC) + c.X; c.PC++; return uint16(c.Read(uint16(zp))) | uint16(c.Read(uint16(zp+1)))<<8 }
func (c *CPU6502) addrIndY() uint16   { zp := c.Read(c.PC); c.PC++; base := uint16(c.Read(uint16(zp))) | uint16(c.Read(uint16(zp+1)))<<8; return base + uint16(c.Y) }

func (c *CPU6502) adc(val byte) {
	a, v := uint16(c.A), uint16(val)
	carry := uint16(0); if c.getFlag(FlagC) { carry = 1 }
	sum := a + v + carry
	c.setFlag(FlagC, sum > 0xFF)
	c.setFlag(FlagV, (^(a^v))&(a^sum)&0x80 != 0)
	c.A = byte(sum); c.setZN(c.A)
}
func (c *CPU6502) sbc(val byte) { c.adc(^val) }
func (c *CPU6502) cmp(reg, val byte) { c.setFlag(FlagC, reg >= val); c.setZN(reg - val) }
func (c *CPU6502) branch(cond bool) { offset := int8(c.Read(c.PC)); c.PC++; if cond { c.PC = uint16(int32(c.PC) + int32(offset)) } }

func (c *CPU6502) Step() bool {
	if c.Coverage != nil {
		c.Coverage[c.PC] = true
	}
	if c.CheckpointAddr != 0 && c.PC == c.CheckpointAddr {
		retLo := c.Memory[0x100+uint16(c.SP)+1]
		retHi := c.Memory[0x100+uint16(c.SP)+2]
		caller := (uint16(retHi)<<8 | uint16(retLo)) - 2
		if c.LastCheckpointCycle > 0 {
			gap := c.Cycles - c.LastCheckpointCycle
			if gap > c.CheckpointGap {
				c.CheckpointGap = gap
				c.CheckpointGapFrom = c.LastCheckpointCaller
				c.CheckpointGapTo = caller
			}
		}
		c.LastCheckpointCycle = c.Cycles
		c.LastCheckpointCaller = caller
	}
	opcode := c.Memory[c.PC]; c.PC++
	switch opcode {
	case 0xA9: c.A = c.Read(c.addrImm()); c.setZN(c.A)
	case 0xA5: c.A = c.Read(c.addrZP()); c.setZN(c.A)
	case 0xB5: c.A = c.Read(c.addrZPX()); c.setZN(c.A)
	case 0xAD: c.A = c.Read(c.addrAbs()); c.setZN(c.A)
	case 0xBD: c.A = c.Read(c.addrAbsX()); c.setZN(c.A)
	case 0xB9: c.A = c.Read(c.addrAbsY()); c.setZN(c.A)
	case 0xA1: c.A = c.Read(c.addrIndX()); c.setZN(c.A)
	case 0xB1: c.A = c.Read(c.addrIndY()); c.setZN(c.A)
	case 0xA2: c.X = c.Read(c.addrImm()); c.setZN(c.X)
	case 0xA6: c.X = c.Read(c.addrZP()); c.setZN(c.X)
	case 0xB6: c.X = c.Read(c.addrZPY()); c.setZN(c.X)
	case 0xAE: c.X = c.Read(c.addrAbs()); c.setZN(c.X)
	case 0xBE: c.X = c.Read(c.addrAbsY()); c.setZN(c.X)
	case 0xA0: c.Y = c.Read(c.addrImm()); c.setZN(c.Y)
	case 0xA4: c.Y = c.Read(c.addrZP()); c.setZN(c.Y)
	case 0xB4: c.Y = c.Read(c.addrZPX()); c.setZN(c.Y)
	case 0xAC: c.Y = c.Read(c.addrAbs()); c.setZN(c.Y)
	case 0xBC: c.Y = c.Read(c.addrAbsX()); c.setZN(c.Y)
	case 0x85: c.Write(c.addrZP(), c.A)
	case 0x95: c.Write(c.addrZPX(), c.A)
	case 0x8D: c.Write(c.addrAbs(), c.A)
	case 0x9D: c.Write(c.addrAbsX(), c.A)
	case 0x99: c.Write(c.addrAbsY(), c.A)
	case 0x81: c.Write(c.addrIndX(), c.A)
	case 0x91: c.Write(c.addrIndY(), c.A)
	case 0x86: c.Write(c.addrZP(), c.X)
	case 0x96: c.Write(c.addrZPY(), c.X)
	case 0x8E: c.Write(c.addrAbs(), c.X)
	case 0x84: c.Write(c.addrZP(), c.Y)
	case 0x94: c.Write(c.addrZPX(), c.Y)
	case 0x8C: c.Write(c.addrAbs(), c.Y)
	case 0xAA: c.X = c.A; c.setZN(c.X)
	case 0xA8: c.Y = c.A; c.setZN(c.Y)
	case 0x8A: c.A = c.X; c.setZN(c.A)
	case 0x98: c.A = c.Y; c.setZN(c.A)
	case 0xBA: c.X = c.SP; c.setZN(c.X)
	case 0x9A: c.SP = c.X
	case 0x48: c.Push(c.A)
	case 0x68: c.A = c.Pull(); c.setZN(c.A)
	case 0x08: c.Push(c.P | FlagB | FlagU)
	case 0x28: c.P = c.Pull()&^FlagB | FlagU
	case 0x29: c.A &= c.Read(c.addrImm()); c.setZN(c.A)
	case 0x25: c.A &= c.Read(c.addrZP()); c.setZN(c.A)
	case 0x35: c.A &= c.Read(c.addrZPX()); c.setZN(c.A)
	case 0x2D: c.A &= c.Read(c.addrAbs()); c.setZN(c.A)
	case 0x3D: c.A &= c.Read(c.addrAbsX()); c.setZN(c.A)
	case 0x39: c.A &= c.Read(c.addrAbsY()); c.setZN(c.A)
	case 0x21: c.A &= c.Read(c.addrIndX()); c.setZN(c.A)
	case 0x31: c.A &= c.Read(c.addrIndY()); c.setZN(c.A)
	case 0x09: c.A |= c.Read(c.addrImm()); c.setZN(c.A)
	case 0x05: c.A |= c.Read(c.addrZP()); c.setZN(c.A)
	case 0x15: c.A |= c.Read(c.addrZPX()); c.setZN(c.A)
	case 0x0D: c.A |= c.Read(c.addrAbs()); c.setZN(c.A)
	case 0x1D: c.A |= c.Read(c.addrAbsX()); c.setZN(c.A)
	case 0x19: c.A |= c.Read(c.addrAbsY()); c.setZN(c.A)
	case 0x01: c.A |= c.Read(c.addrIndX()); c.setZN(c.A)
	case 0x11: c.A |= c.Read(c.addrIndY()); c.setZN(c.A)
	case 0x49: c.A ^= c.Read(c.addrImm()); c.setZN(c.A)
	case 0x45: c.A ^= c.Read(c.addrZP()); c.setZN(c.A)
	case 0x55: c.A ^= c.Read(c.addrZPX()); c.setZN(c.A)
	case 0x4D: c.A ^= c.Read(c.addrAbs()); c.setZN(c.A)
	case 0x5D: c.A ^= c.Read(c.addrAbsX()); c.setZN(c.A)
	case 0x59: c.A ^= c.Read(c.addrAbsY()); c.setZN(c.A)
	case 0x41: c.A ^= c.Read(c.addrIndX()); c.setZN(c.A)
	case 0x51: c.A ^= c.Read(c.addrIndY()); c.setZN(c.A)
	case 0x24: val := c.Read(c.addrZP()); c.setFlag(FlagZ, c.A&val == 0); c.setFlag(FlagV, val&0x40 != 0); c.setFlag(FlagN, val&0x80 != 0)
	case 0x2C: val := c.Read(c.addrAbs()); c.setFlag(FlagZ, c.A&val == 0); c.setFlag(FlagV, val&0x40 != 0); c.setFlag(FlagN, val&0x80 != 0)
	case 0x69: c.adc(c.Read(c.addrImm()))
	case 0x65: c.adc(c.Read(c.addrZP()))
	case 0x75: c.adc(c.Read(c.addrZPX()))
	case 0x6D: c.adc(c.Read(c.addrAbs()))
	case 0x7D: c.adc(c.Read(c.addrAbsX()))
	case 0x79: c.adc(c.Read(c.addrAbsY()))
	case 0x61: c.adc(c.Read(c.addrIndX()))
	case 0x71: c.adc(c.Read(c.addrIndY()))
	case 0xE9: c.sbc(c.Read(c.addrImm()))
	case 0xE5: c.sbc(c.Read(c.addrZP()))
	case 0xF5: c.sbc(c.Read(c.addrZPX()))
	case 0xED: c.sbc(c.Read(c.addrAbs()))
	case 0xFD: c.sbc(c.Read(c.addrAbsX()))
	case 0xF9: c.sbc(c.Read(c.addrAbsY()))
	case 0xE1: c.sbc(c.Read(c.addrIndX()))
	case 0xF1: c.sbc(c.Read(c.addrIndY()))
	case 0xC9: c.cmp(c.A, c.Read(c.addrImm()))
	case 0xC5: c.cmp(c.A, c.Read(c.addrZP()))
	case 0xD5: c.cmp(c.A, c.Read(c.addrZPX()))
	case 0xCD: c.cmp(c.A, c.Read(c.addrAbs()))
	case 0xDD: c.cmp(c.A, c.Read(c.addrAbsX()))
	case 0xD9: c.cmp(c.A, c.Read(c.addrAbsY()))
	case 0xC1: c.cmp(c.A, c.Read(c.addrIndX()))
	case 0xD1: c.cmp(c.A, c.Read(c.addrIndY()))
	case 0xE0: c.cmp(c.X, c.Read(c.addrImm()))
	case 0xE4: c.cmp(c.X, c.Read(c.addrZP()))
	case 0xEC: c.cmp(c.X, c.Read(c.addrAbs()))
	case 0xC0: c.cmp(c.Y, c.Read(c.addrImm()))
	case 0xC4: c.cmp(c.Y, c.Read(c.addrZP()))
	case 0xCC: c.cmp(c.Y, c.Read(c.addrAbs()))
	case 0xE6: addr := c.addrZP(); val := c.Read(addr) + 1; c.Write(addr, val); c.setZN(val)
	case 0xF6: addr := c.addrZPX(); val := c.Read(addr) + 1; c.Write(addr, val); c.setZN(val)
	case 0xEE: addr := c.addrAbs(); val := c.Read(addr) + 1; c.Write(addr, val); c.setZN(val)
	case 0xFE: addr := c.addrAbsX(); val := c.Read(addr) + 1; c.Write(addr, val); c.setZN(val)
	case 0xC6: addr := c.addrZP(); val := c.Read(addr) - 1; c.Write(addr, val); c.setZN(val)
	case 0xD6: addr := c.addrZPX(); val := c.Read(addr) - 1; c.Write(addr, val); c.setZN(val)
	case 0xCE: addr := c.addrAbs(); val := c.Read(addr) - 1; c.Write(addr, val); c.setZN(val)
	case 0xDE: addr := c.addrAbsX(); val := c.Read(addr) - 1; c.Write(addr, val); c.setZN(val)
	case 0xE8: c.X++; c.setZN(c.X)
	case 0xC8: c.Y++; c.setZN(c.Y)
	case 0xCA: c.X--; c.setZN(c.X)
	case 0x88: c.Y--; c.setZN(c.Y)
	case 0x0A: c.setFlag(FlagC, c.A&0x80 != 0); c.A <<= 1; c.setZN(c.A)
	case 0x06: addr := c.addrZP(); val := c.Read(addr); c.setFlag(FlagC, val&0x80 != 0); val <<= 1; c.Write(addr, val); c.setZN(val)
	case 0x16: addr := c.addrZPX(); val := c.Read(addr); c.setFlag(FlagC, val&0x80 != 0); val <<= 1; c.Write(addr, val); c.setZN(val)
	case 0x0E: addr := c.addrAbs(); val := c.Read(addr); c.setFlag(FlagC, val&0x80 != 0); val <<= 1; c.Write(addr, val); c.setZN(val)
	case 0x1E: addr := c.addrAbsX(); val := c.Read(addr); c.setFlag(FlagC, val&0x80 != 0); val <<= 1; c.Write(addr, val); c.setZN(val)
	case 0x4A: c.setFlag(FlagC, c.A&0x01 != 0); c.A >>= 1; c.setZN(c.A)
	case 0x46: addr := c.addrZP(); val := c.Read(addr); c.setFlag(FlagC, val&0x01 != 0); val >>= 1; c.Write(addr, val); c.setZN(val)
	case 0x56: addr := c.addrZPX(); val := c.Read(addr); c.setFlag(FlagC, val&0x01 != 0); val >>= 1; c.Write(addr, val); c.setZN(val)
	case 0x4E: addr := c.addrAbs(); val := c.Read(addr); c.setFlag(FlagC, val&0x01 != 0); val >>= 1; c.Write(addr, val); c.setZN(val)
	case 0x5E: addr := c.addrAbsX(); val := c.Read(addr); c.setFlag(FlagC, val&0x01 != 0); val >>= 1; c.Write(addr, val); c.setZN(val)
	case 0x2A: carry := c.P & FlagC; c.setFlag(FlagC, c.A&0x80 != 0); c.A = c.A<<1 | carry; c.setZN(c.A)
	case 0x26: addr := c.addrZP(); val := c.Read(addr); carry := c.P & FlagC; c.setFlag(FlagC, val&0x80 != 0); val = val<<1 | carry; c.Write(addr, val); c.setZN(val)
	case 0x36: addr := c.addrZPX(); val := c.Read(addr); carry := c.P & FlagC; c.setFlag(FlagC, val&0x80 != 0); val = val<<1 | carry; c.Write(addr, val); c.setZN(val)
	case 0x2E: addr := c.addrAbs(); val := c.Read(addr); carry := c.P & FlagC; c.setFlag(FlagC, val&0x80 != 0); val = val<<1 | carry; c.Write(addr, val); c.setZN(val)
	case 0x3E: addr := c.addrAbsX(); val := c.Read(addr); carry := c.P & FlagC; c.setFlag(FlagC, val&0x80 != 0); val = val<<1 | carry; c.Write(addr, val); c.setZN(val)
	case 0x6A: carry := c.P & FlagC; c.setFlag(FlagC, c.A&0x01 != 0); c.A = c.A>>1 | carry<<7; c.setZN(c.A)
	case 0x66: addr := c.addrZP(); val := c.Read(addr); carry := c.P & FlagC; c.setFlag(FlagC, val&0x01 != 0); val = val>>1 | carry<<7; c.Write(addr, val); c.setZN(val)
	case 0x76: addr := c.addrZPX(); val := c.Read(addr); carry := c.P & FlagC; c.setFlag(FlagC, val&0x01 != 0); val = val>>1 | carry<<7; c.Write(addr, val); c.setZN(val)
	case 0x6E: addr := c.addrAbs(); val := c.Read(addr); carry := c.P & FlagC; c.setFlag(FlagC, val&0x01 != 0); val = val>>1 | carry<<7; c.Write(addr, val); c.setZN(val)
	case 0x7E: addr := c.addrAbsX(); val := c.Read(addr); carry := c.P & FlagC; c.setFlag(FlagC, val&0x01 != 0); val = val>>1 | carry<<7; c.Write(addr, val); c.setZN(val)
	case 0x4C: c.PC = c.addrAbs()
	case 0x6C: addr := c.Read16(c.PC); lo := uint16(c.Read(addr)); hi := uint16(c.Read((addr & 0xFF00) | ((addr + 1) & 0x00FF))); c.PC = hi<<8 | lo
	case 0x20: addr := c.addrAbs(); c.Push16(c.PC - 1); c.PC = addr
	case 0x60: c.PC = c.Pull16() + 1; return true
	case 0x40: c.P = c.Pull()&^FlagB | FlagU; c.PC = c.Pull16()
	case 0x10: c.branch(!c.getFlag(FlagN))
	case 0x30: c.branch(c.getFlag(FlagN))
	case 0x50: c.branch(!c.getFlag(FlagV))
	case 0x70: c.branch(c.getFlag(FlagV))
	case 0x90: c.branch(!c.getFlag(FlagC))
	case 0xB0: c.branch(c.getFlag(FlagC))
	case 0xD0: c.branch(!c.getFlag(FlagZ))
	case 0xF0: c.branch(c.getFlag(FlagZ))
	case 0x18:
		if c.TotalCLC != nil { c.TotalCLC[c.PC-1]++ }
		if c.RedundantCLC != nil && c.P&FlagC == 0 { c.RedundantCLC[c.PC-1]++ }
		c.P &^= FlagC
	case 0x38:
		if c.TotalSEC != nil { c.TotalSEC[c.PC-1]++ }
		if c.RedundantSEC != nil && c.P&FlagC != 0 { c.RedundantSEC[c.PC-1]++ }
		c.P |= FlagC
	case 0x58: c.P &^= FlagI
	case 0x78: c.P |= FlagI
	case 0xB8: c.P &^= FlagV
	case 0xD8: c.P &^= FlagD
	case 0xF8: c.P |= FlagD
	case 0xEA: // NOP
	case 0x00: c.PC++; c.Push16(c.PC); c.Push(c.P | FlagB | FlagU); c.P |= FlagI; c.PC = c.Read16(0xFFFE)
	default: panic(fmt.Sprintf("Unknown opcode: $%02X at $%04X", opcode, c.PC-1))
	}
	c.Cycles++
	return false
}

func (c *CPU6502) Call(addr uint16) {
	c.Push16(0xFFFF)
	c.PC = addr
	for count := 0; count < 1000000; count++ {
		if c.Step() && c.PC == 0x0000 { return }
	}
	panic("Infinite loop detected")
}

func (c *CPU6502) RunFrames(playAddr uint16, frames int) []SIDWrite {
	c.SIDWrites = nil
	c.CurrentFrame = 0
	c.Cycles = 0
	c.LastCheckpointCycle = 0
	for i := 0; i < frames; i++ {
		c.CurrentFrame = i
		c.Call(playAddr)
	}
	return c.SIDWrites
}

func (c *CPU6502) RunUntilFrame(playAddr uint16, targetFrame int) {
	c.SIDWrites = nil
	c.CurrentFrame = 0
	for i := 0; i <= targetFrame; i++ {
		c.CurrentFrame = i
		c.Call(playAddr)
	}
}

var instrLengths = map[byte]int{
	0x00: 1, 0x01: 2, 0x05: 2, 0x06: 2, 0x08: 1, 0x09: 2, 0x0A: 1, 0x0D: 3, 0x0E: 3,
	0x10: 2, 0x11: 2, 0x15: 2, 0x16: 2, 0x18: 1, 0x19: 3, 0x1D: 3, 0x1E: 3,
	0x20: 3, 0x21: 2, 0x24: 2, 0x25: 2, 0x26: 2, 0x28: 1, 0x29: 2, 0x2A: 1, 0x2C: 3, 0x2D: 3, 0x2E: 3,
	0x30: 2, 0x31: 2, 0x35: 2, 0x36: 2, 0x38: 1, 0x39: 3, 0x3D: 3, 0x3E: 3,
	0x40: 1, 0x41: 2, 0x45: 2, 0x46: 2, 0x48: 1, 0x49: 2, 0x4A: 1, 0x4C: 3, 0x4D: 3, 0x4E: 3,
	0x50: 2, 0x51: 2, 0x55: 2, 0x56: 2, 0x58: 1, 0x59: 3, 0x5D: 3, 0x5E: 3,
	0x60: 1, 0x61: 2, 0x65: 2, 0x66: 2, 0x68: 1, 0x69: 2, 0x6A: 1, 0x6C: 3, 0x6D: 3, 0x6E: 3,
	0x70: 2, 0x71: 2, 0x75: 2, 0x76: 2, 0x78: 1, 0x79: 3, 0x7D: 3, 0x7E: 3,
	0x81: 2, 0x84: 2, 0x85: 2, 0x86: 2, 0x88: 1, 0x8A: 1, 0x8C: 3, 0x8D: 3, 0x8E: 3,
	0x90: 2, 0x91: 2, 0x94: 2, 0x95: 2, 0x96: 2, 0x98: 1, 0x99: 3, 0x9A: 1, 0x9D: 3,
	0xA0: 2, 0xA1: 2, 0xA2: 2, 0xA4: 2, 0xA5: 2, 0xA6: 2, 0xA8: 1, 0xA9: 2, 0xAA: 1, 0xAC: 3, 0xAD: 3, 0xAE: 3,
	0xB0: 2, 0xB1: 2, 0xB4: 2, 0xB5: 2, 0xB6: 2, 0xB8: 1, 0xB9: 3, 0xBA: 1, 0xBC: 3, 0xBD: 3, 0xBE: 3,
	0xC0: 2, 0xC1: 2, 0xC4: 2, 0xC5: 2, 0xC6: 2, 0xC8: 1, 0xC9: 2, 0xCA: 1, 0xCC: 3, 0xCD: 3, 0xCE: 3,
	0xD0: 2, 0xD1: 2, 0xD5: 2, 0xD6: 2, 0xD8: 1, 0xD9: 3, 0xDD: 3, 0xDE: 3,
	0xE0: 2, 0xE1: 2, 0xE4: 2, 0xE5: 2, 0xE6: 2, 0xE8: 1, 0xE9: 2, 0xEA: 1, 0xEC: 3, 0xED: 3, 0xEE: 3,
	0xF0: 2, 0xF1: 2, 0xF5: 2, 0xF6: 2, 0xF8: 1, 0xF9: 3, 0xFD: 3, 0xFE: 3,
}

func InstrLengths() map[byte]int {
	return instrLengths
}

func FindInstructionStarts(code []byte, base uint16) []uint16 {
	var starts []uint16
	for i := 0; i < len(code); {
		opcode := code[i]
		if opcode == 0x00 && i+1 < len(code) && code[i+1] == 0x00 {
			break
		}
		starts = append(starts, base+uint16(i))
		if length, ok := instrLengths[opcode]; ok {
			i += length
		} else {
			break
		}
	}
	return starts
}
