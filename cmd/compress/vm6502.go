package main

import (
	"fmt"
)

// CPU6502 is a minimal 6502 emulator for testing decompression
type CPU6502 struct {
	A, X, Y byte   // Registers
	SP      byte   // Stack pointer
	PC      uint16 // Program counter
	P       byte   // Status flags: NV-BDIZC

	Mem    [65536]byte
	Cycles uint64

	// Breakpoint for stopping execution
	Breakpoint uint16
	Halted     bool

	// Write tracking
	LastWriteAddr uint16
	WriteCount    int

	// Coverage tracking for redundant flag operations
	CLCTotal     map[uint16]int // PC -> total CLC executions
	CLCRedundant map[uint16]int // PC -> count when C already 0
	SECTotal     map[uint16]int // PC -> total SEC executions
	SECRedundant map[uint16]int // PC -> count when C already 1

	// Memory access callbacks for validation
	OnRead  func(addr uint16) // Called on memory reads from copy operations
	OnWrite func(addr uint16) // Called on memory writes to buffers
}

// Status flag bits
const (
	FlagC byte = 1 << 0 // Carry
	FlagZ byte = 1 << 1 // Zero
	FlagI byte = 1 << 2 // Interrupt disable
	FlagD byte = 1 << 3 // Decimal mode
	FlagB byte = 1 << 4 // Break
	FlagU byte = 1 << 5 // Unused (always 1)
	FlagV byte = 1 << 6 // Overflow
	FlagN byte = 1 << 7 // Negative
)

func NewCPU6502() *CPU6502 {
	cpu := &CPU6502{
		SP:           0xFF,
		P:            FlagU | FlagI,
		CLCTotal:     make(map[uint16]int),
		CLCRedundant: make(map[uint16]int),
		SECTotal:     make(map[uint16]int),
		SECRedundant: make(map[uint16]int),
	}
	return cpu
}

// trackWrite tracks writes to the monitored memory range
func (c *CPU6502) trackWrite(addr uint16) {
	if addr >= 0x1000 && addr < 0xD000 {
		c.LastWriteAddr = addr
		c.WriteCount++
		if c.OnWrite != nil {
			c.OnWrite(addr)
		}
	}
}

// trackRead tracks reads from buffers (for copy operations)
func (c *CPU6502) trackRead(addr uint16) {
	if addr >= 0x1000 && addr < 0xD000 {
		if c.OnRead != nil {
			c.OnRead(addr)
		}
	}
}

func (c *CPU6502) setZ(v byte) {
	if v == 0 {
		c.P |= FlagZ
	} else {
		c.P &^= FlagZ
	}
}

func (c *CPU6502) setN(v byte) {
	if v&0x80 != 0 {
		c.P |= FlagN
	} else {
		c.P &^= FlagN
	}
}

func (c *CPU6502) setNZ(v byte) {
	c.setN(v)
	c.setZ(v)
}

func (c *CPU6502) push(v byte) {
	c.Mem[0x100+uint16(c.SP)] = v
	c.SP--
}

func (c *CPU6502) pop() byte {
	c.SP++
	return c.Mem[0x100+uint16(c.SP)]
}

func (c *CPU6502) push16(v uint16) {
	c.push(byte(v >> 8))
	c.push(byte(v))
}

func (c *CPU6502) pop16() uint16 {
	lo := uint16(c.pop())
	hi := uint16(c.pop())
	return hi<<8 | lo
}

func (c *CPU6502) read16(addr uint16) uint16 {
	lo := uint16(c.Mem[addr])
	hi := uint16(c.Mem[addr+1])
	return hi<<8 | lo
}

// Addressing mode helpers
func (c *CPU6502) addrZP() uint16 {
	addr := uint16(c.Mem[c.PC])
	c.PC++
	return addr
}

func (c *CPU6502) addrZPX() uint16 {
	addr := uint16(c.Mem[c.PC] + c.X)
	c.PC++
	return addr
}

func (c *CPU6502) addrZPY() uint16 {
	addr := uint16(c.Mem[c.PC] + c.Y)
	c.PC++
	return addr
}

func (c *CPU6502) addrAbs() uint16 {
	lo := uint16(c.Mem[c.PC])
	hi := uint16(c.Mem[c.PC+1])
	c.PC += 2
	return hi<<8 | lo
}

func (c *CPU6502) addrAbsX() uint16 {
	lo := uint16(c.Mem[c.PC])
	hi := uint16(c.Mem[c.PC+1])
	c.PC += 2
	return (hi<<8 | lo) + uint16(c.X)
}

func (c *CPU6502) addrAbsY() uint16 {
	lo := uint16(c.Mem[c.PC])
	hi := uint16(c.Mem[c.PC+1])
	c.PC += 2
	return (hi<<8 | lo) + uint16(c.Y)
}

func (c *CPU6502) addrIndX() uint16 {
	zp := c.Mem[c.PC] + c.X
	c.PC++
	lo := uint16(c.Mem[zp])
	hi := uint16(c.Mem[zp+1])
	return hi<<8 | lo
}

func (c *CPU6502) addrIndY() uint16 {
	zp := c.Mem[c.PC]
	c.PC++
	lo := uint16(c.Mem[zp])
	hi := uint16(c.Mem[zp+1])
	return (hi<<8 | lo) + uint16(c.Y)
}

func (c *CPU6502) branch(cond bool) {
	offset := int8(c.Mem[c.PC])
	c.PC++
	if cond {
		c.PC = uint16(int32(c.PC) + int32(offset))
		c.Cycles++
	}
}

func (c *CPU6502) compare(a, b byte) {
	result := uint16(a) - uint16(b)
	if a >= b {
		c.P |= FlagC
	} else {
		c.P &^= FlagC
	}
	c.setNZ(byte(result))
}

// Step executes one instruction
func (c *CPU6502) Step() error {
	if c.PC == c.Breakpoint {
		c.Halted = true
		return nil
	}

	opcode := c.Mem[c.PC]
	c.PC++
	c.Cycles++

	switch opcode {
	// LDA
	case 0xA9: // LDA #imm
		c.A = c.Mem[c.PC]
		c.PC++
		c.setNZ(c.A)
	case 0xA5: // LDA zp
		c.A = c.Mem[c.addrZP()]
		c.setNZ(c.A)
	case 0xB5: // LDA zp,X
		c.A = c.Mem[c.addrZPX()]
		c.setNZ(c.A)
	case 0xAD: // LDA abs
		c.A = c.Mem[c.addrAbs()]
		c.setNZ(c.A)
	case 0xBD: // LDA abs,X
		c.A = c.Mem[c.addrAbsX()]
		c.setNZ(c.A)
	case 0xB9: // LDA abs,Y
		c.A = c.Mem[c.addrAbsY()]
		c.setNZ(c.A)
	case 0xA1: // LDA (zp,X)
		c.A = c.Mem[c.addrIndX()]
		c.setNZ(c.A)
	case 0xB1: // LDA (zp),Y
		zpAddr := c.Mem[c.PC] // Get zero page address before addrIndY increments PC
		addr := c.addrIndY()
		// Only track reads from zp_ref ($09) - copy operations
		// Don't track reads from zp_src ($02) - compressed stream reads
		if zpAddr == 0x09 {
			c.trackRead(addr)
		}
		c.A = c.Mem[addr]
		c.setNZ(c.A)

	// LDX
	case 0xA2: // LDX #imm
		c.X = c.Mem[c.PC]
		c.PC++
		c.setNZ(c.X)
	case 0xA6: // LDX zp
		c.X = c.Mem[c.addrZP()]
		c.setNZ(c.X)
	case 0xB6: // LDX zp,Y
		c.X = c.Mem[c.addrZPY()]
		c.setNZ(c.X)
	case 0xAE: // LDX abs
		c.X = c.Mem[c.addrAbs()]
		c.setNZ(c.X)
	case 0xBE: // LDX abs,Y
		c.X = c.Mem[c.addrAbsY()]
		c.setNZ(c.X)

	// LDY
	case 0xA0: // LDY #imm
		c.Y = c.Mem[c.PC]
		c.PC++
		c.setNZ(c.Y)
	case 0xA4: // LDY zp
		c.Y = c.Mem[c.addrZP()]
		c.setNZ(c.Y)
	case 0xB4: // LDY zp,X
		c.Y = c.Mem[c.addrZPX()]
		c.setNZ(c.Y)
	case 0xAC: // LDY abs
		c.Y = c.Mem[c.addrAbs()]
		c.setNZ(c.Y)
	case 0xBC: // LDY abs,X
		c.Y = c.Mem[c.addrAbsX()]
		c.setNZ(c.Y)

	// STA
	case 0x85: // STA zp
		addr := c.addrZP()
		c.Mem[addr] = c.A
		c.trackWrite(addr)
	case 0x95: // STA zp,X
		addr := c.addrZPX()
		c.Mem[addr] = c.A
		c.trackWrite(addr)
	case 0x8D: // STA abs
		addr := c.addrAbs()
		c.Mem[addr] = c.A
		c.trackWrite(addr)
	case 0x9D: // STA abs,X
		addr := c.addrAbsX()
		c.Mem[addr] = c.A
		c.trackWrite(addr)
	case 0x99: // STA abs,Y
		addr := c.addrAbsY()
		c.Mem[addr] = c.A
		c.trackWrite(addr)
	case 0x81: // STA (zp,X)
		addr := c.addrIndX()
		c.Mem[addr] = c.A
		c.trackWrite(addr)
	case 0x91: // STA (zp),Y
		addr := c.addrIndY()
		c.Mem[addr] = c.A
		c.trackWrite(addr)

	// STX
	case 0x86: // STX zp
		c.Mem[c.addrZP()] = c.X
	case 0x96: // STX zp,Y
		c.Mem[c.addrZPY()] = c.X
	case 0x8E: // STX abs
		c.Mem[c.addrAbs()] = c.X

	// STY
	case 0x84: // STY zp
		c.Mem[c.addrZP()] = c.Y
	case 0x94: // STY zp,X
		c.Mem[c.addrZPX()] = c.Y
	case 0x8C: // STY abs
		c.Mem[c.addrAbs()] = c.Y

	// Transfer
	case 0xAA: // TAX
		c.X = c.A
		c.setNZ(c.X)
	case 0xA8: // TAY
		c.Y = c.A
		c.setNZ(c.Y)
	case 0x8A: // TXA
		c.A = c.X
		c.setNZ(c.A)
	case 0x98: // TYA
		c.A = c.Y
		c.setNZ(c.A)
	case 0xBA: // TSX
		c.X = c.SP
		c.setNZ(c.X)
	case 0x9A: // TXS
		c.SP = c.X

	// Stack
	case 0x48: // PHA
		c.push(c.A)
	case 0x68: // PLA
		c.A = c.pop()
		c.setNZ(c.A)
	case 0x08: // PHP
		c.push(c.P | FlagB | FlagU)
	case 0x28: // PLP
		c.P = c.pop()&^FlagB | FlagU

	// INC/DEC
	case 0xE6: // INC zp
		addr := c.addrZP()
		c.Mem[addr]++
		c.setNZ(c.Mem[addr])
	case 0xF6: // INC zp,X
		addr := c.addrZPX()
		c.Mem[addr]++
		c.setNZ(c.Mem[addr])
	case 0xEE: // INC abs
		addr := c.addrAbs()
		c.Mem[addr]++
		c.setNZ(c.Mem[addr])
	case 0xFE: // INC abs,X
		addr := c.addrAbsX()
		c.Mem[addr]++
		c.setNZ(c.Mem[addr])
	case 0xC6: // DEC zp
		addr := c.addrZP()
		c.Mem[addr]--
		c.setNZ(c.Mem[addr])
	case 0xD6: // DEC zp,X
		addr := c.addrZPX()
		c.Mem[addr]--
		c.setNZ(c.Mem[addr])
	case 0xCE: // DEC abs
		addr := c.addrAbs()
		c.Mem[addr]--
		c.setNZ(c.Mem[addr])
	case 0xDE: // DEC abs,X
		addr := c.addrAbsX()
		c.Mem[addr]--
		c.setNZ(c.Mem[addr])
	case 0xE8: // INX
		c.X++
		c.setNZ(c.X)
	case 0xC8: // INY
		c.Y++
		c.setNZ(c.Y)
	case 0xCA: // DEX
		c.X--
		c.setNZ(c.X)
	case 0x88: // DEY
		c.Y--
		c.setNZ(c.Y)

	// AND
	case 0x29: // AND #imm
		c.A &= c.Mem[c.PC]
		c.PC++
		c.setNZ(c.A)
	case 0x25: // AND zp
		c.A &= c.Mem[c.addrZP()]
		c.setNZ(c.A)
	case 0x35: // AND zp,X
		c.A &= c.Mem[c.addrZPX()]
		c.setNZ(c.A)
	case 0x2D: // AND abs
		c.A &= c.Mem[c.addrAbs()]
		c.setNZ(c.A)
	case 0x3D: // AND abs,X
		c.A &= c.Mem[c.addrAbsX()]
		c.setNZ(c.A)
	case 0x39: // AND abs,Y
		c.A &= c.Mem[c.addrAbsY()]
		c.setNZ(c.A)
	case 0x21: // AND (zp,X)
		c.A &= c.Mem[c.addrIndX()]
		c.setNZ(c.A)
	case 0x31: // AND (zp),Y
		c.A &= c.Mem[c.addrIndY()]
		c.setNZ(c.A)

	// ORA
	case 0x09: // ORA #imm
		c.A |= c.Mem[c.PC]
		c.PC++
		c.setNZ(c.A)
	case 0x05: // ORA zp
		c.A |= c.Mem[c.addrZP()]
		c.setNZ(c.A)
	case 0x15: // ORA zp,X
		c.A |= c.Mem[c.addrZPX()]
		c.setNZ(c.A)
	case 0x0D: // ORA abs
		c.A |= c.Mem[c.addrAbs()]
		c.setNZ(c.A)
	case 0x1D: // ORA abs,X
		c.A |= c.Mem[c.addrAbsX()]
		c.setNZ(c.A)
	case 0x19: // ORA abs,Y
		c.A |= c.Mem[c.addrAbsY()]
		c.setNZ(c.A)
	case 0x01: // ORA (zp,X)
		c.A |= c.Mem[c.addrIndX()]
		c.setNZ(c.A)
	case 0x11: // ORA (zp),Y
		c.A |= c.Mem[c.addrIndY()]
		c.setNZ(c.A)

	// EOR
	case 0x49: // EOR #imm
		c.A ^= c.Mem[c.PC]
		c.PC++
		c.setNZ(c.A)
	case 0x45: // EOR zp
		c.A ^= c.Mem[c.addrZP()]
		c.setNZ(c.A)
	case 0x55: // EOR zp,X
		c.A ^= c.Mem[c.addrZPX()]
		c.setNZ(c.A)
	case 0x4D: // EOR abs
		c.A ^= c.Mem[c.addrAbs()]
		c.setNZ(c.A)
	case 0x5D: // EOR abs,X
		c.A ^= c.Mem[c.addrAbsX()]
		c.setNZ(c.A)
	case 0x59: // EOR abs,Y
		c.A ^= c.Mem[c.addrAbsY()]
		c.setNZ(c.A)
	case 0x41: // EOR (zp,X)
		c.A ^= c.Mem[c.addrIndX()]
		c.setNZ(c.A)
	case 0x51: // EOR (zp),Y
		c.A ^= c.Mem[c.addrIndY()]
		c.setNZ(c.A)

	// ASL
	case 0x0A: // ASL A
		if c.A&0x80 != 0 {
			c.P |= FlagC
		} else {
			c.P &^= FlagC
		}
		c.A <<= 1
		c.setNZ(c.A)
	case 0x06: // ASL zp
		addr := c.addrZP()
		if c.Mem[addr]&0x80 != 0 {
			c.P |= FlagC
		} else {
			c.P &^= FlagC
		}
		c.Mem[addr] <<= 1
		c.setNZ(c.Mem[addr])
	case 0x16: // ASL zp,X
		addr := c.addrZPX()
		if c.Mem[addr]&0x80 != 0 {
			c.P |= FlagC
		} else {
			c.P &^= FlagC
		}
		c.Mem[addr] <<= 1
		c.setNZ(c.Mem[addr])
	case 0x0E: // ASL abs
		addr := c.addrAbs()
		if c.Mem[addr]&0x80 != 0 {
			c.P |= FlagC
		} else {
			c.P &^= FlagC
		}
		c.Mem[addr] <<= 1
		c.setNZ(c.Mem[addr])
	case 0x1E: // ASL abs,X
		addr := c.addrAbsX()
		if c.Mem[addr]&0x80 != 0 {
			c.P |= FlagC
		} else {
			c.P &^= FlagC
		}
		c.Mem[addr] <<= 1
		c.setNZ(c.Mem[addr])

	// LSR
	case 0x4A: // LSR A
		if c.A&0x01 != 0 {
			c.P |= FlagC
		} else {
			c.P &^= FlagC
		}
		c.A >>= 1
		c.setNZ(c.A)
	case 0x46: // LSR zp
		addr := c.addrZP()
		if c.Mem[addr]&0x01 != 0 {
			c.P |= FlagC
		} else {
			c.P &^= FlagC
		}
		c.Mem[addr] >>= 1
		c.setNZ(c.Mem[addr])
	case 0x56: // LSR zp,X
		addr := c.addrZPX()
		if c.Mem[addr]&0x01 != 0 {
			c.P |= FlagC
		} else {
			c.P &^= FlagC
		}
		c.Mem[addr] >>= 1
		c.setNZ(c.Mem[addr])
	case 0x4E: // LSR abs
		addr := c.addrAbs()
		if c.Mem[addr]&0x01 != 0 {
			c.P |= FlagC
		} else {
			c.P &^= FlagC
		}
		c.Mem[addr] >>= 1
		c.setNZ(c.Mem[addr])
	case 0x5E: // LSR abs,X
		addr := c.addrAbsX()
		if c.Mem[addr]&0x01 != 0 {
			c.P |= FlagC
		} else {
			c.P &^= FlagC
		}
		c.Mem[addr] >>= 1
		c.setNZ(c.Mem[addr])

	// ROL
	case 0x2A: // ROL A
		carry := c.P & FlagC
		if c.A&0x80 != 0 {
			c.P |= FlagC
		} else {
			c.P &^= FlagC
		}
		c.A = c.A<<1 | carry
		c.setNZ(c.A)
	case 0x26: // ROL zp
		addr := c.addrZP()
		carry := c.P & FlagC
		if c.Mem[addr]&0x80 != 0 {
			c.P |= FlagC
		} else {
			c.P &^= FlagC
		}
		c.Mem[addr] = c.Mem[addr]<<1 | carry
		c.setNZ(c.Mem[addr])
	case 0x36: // ROL zp,X
		addr := c.addrZPX()
		carry := c.P & FlagC
		if c.Mem[addr]&0x80 != 0 {
			c.P |= FlagC
		} else {
			c.P &^= FlagC
		}
		c.Mem[addr] = c.Mem[addr]<<1 | carry
		c.setNZ(c.Mem[addr])
	case 0x2E: // ROL abs
		addr := c.addrAbs()
		carry := c.P & FlagC
		if c.Mem[addr]&0x80 != 0 {
			c.P |= FlagC
		} else {
			c.P &^= FlagC
		}
		c.Mem[addr] = c.Mem[addr]<<1 | carry
		c.setNZ(c.Mem[addr])
	case 0x3E: // ROL abs,X
		addr := c.addrAbsX()
		carry := c.P & FlagC
		if c.Mem[addr]&0x80 != 0 {
			c.P |= FlagC
		} else {
			c.P &^= FlagC
		}
		c.Mem[addr] = c.Mem[addr]<<1 | carry
		c.setNZ(c.Mem[addr])

	// ROR
	case 0x6A: // ROR A
		carry := c.P & FlagC
		if c.A&0x01 != 0 {
			c.P |= FlagC
		} else {
			c.P &^= FlagC
		}
		c.A = c.A>>1 | carry<<7
		c.setNZ(c.A)
	case 0x66: // ROR zp
		addr := c.addrZP()
		carry := c.P & FlagC
		if c.Mem[addr]&0x01 != 0 {
			c.P |= FlagC
		} else {
			c.P &^= FlagC
		}
		c.Mem[addr] = c.Mem[addr]>>1 | carry<<7
		c.setNZ(c.Mem[addr])
	case 0x76: // ROR zp,X
		addr := c.addrZPX()
		carry := c.P & FlagC
		if c.Mem[addr]&0x01 != 0 {
			c.P |= FlagC
		} else {
			c.P &^= FlagC
		}
		c.Mem[addr] = c.Mem[addr]>>1 | carry<<7
		c.setNZ(c.Mem[addr])
	case 0x6E: // ROR abs
		addr := c.addrAbs()
		carry := c.P & FlagC
		if c.Mem[addr]&0x01 != 0 {
			c.P |= FlagC
		} else {
			c.P &^= FlagC
		}
		c.Mem[addr] = c.Mem[addr]>>1 | carry<<7
		c.setNZ(c.Mem[addr])
	case 0x7E: // ROR abs,X
		addr := c.addrAbsX()
		carry := c.P & FlagC
		if c.Mem[addr]&0x01 != 0 {
			c.P |= FlagC
		} else {
			c.P &^= FlagC
		}
		c.Mem[addr] = c.Mem[addr]>>1 | carry<<7
		c.setNZ(c.Mem[addr])

	// ADC
	case 0x69: // ADC #imm
		c.adc(c.Mem[c.PC])
		c.PC++
	case 0x65: // ADC zp
		c.adc(c.Mem[c.addrZP()])
	case 0x75: // ADC zp,X
		c.adc(c.Mem[c.addrZPX()])
	case 0x6D: // ADC abs
		c.adc(c.Mem[c.addrAbs()])
	case 0x7D: // ADC abs,X
		c.adc(c.Mem[c.addrAbsX()])
	case 0x79: // ADC abs,Y
		c.adc(c.Mem[c.addrAbsY()])
	case 0x61: // ADC (zp,X)
		c.adc(c.Mem[c.addrIndX()])
	case 0x71: // ADC (zp),Y
		c.adc(c.Mem[c.addrIndY()])

	// SBC
	case 0xE9: // SBC #imm
		c.sbc(c.Mem[c.PC])
		c.PC++
	case 0xE5: // SBC zp
		c.sbc(c.Mem[c.addrZP()])
	case 0xF5: // SBC zp,X
		c.sbc(c.Mem[c.addrZPX()])
	case 0xED: // SBC abs
		c.sbc(c.Mem[c.addrAbs()])
	case 0xFD: // SBC abs,X
		c.sbc(c.Mem[c.addrAbsX()])
	case 0xF9: // SBC abs,Y
		c.sbc(c.Mem[c.addrAbsY()])
	case 0xE1: // SBC (zp,X)
		c.sbc(c.Mem[c.addrIndX()])
	case 0xF1: // SBC (zp),Y
		c.sbc(c.Mem[c.addrIndY()])

	// CMP
	case 0xC9: // CMP #imm
		c.compare(c.A, c.Mem[c.PC])
		c.PC++
	case 0xC5: // CMP zp
		c.compare(c.A, c.Mem[c.addrZP()])
	case 0xD5: // CMP zp,X
		c.compare(c.A, c.Mem[c.addrZPX()])
	case 0xCD: // CMP abs
		c.compare(c.A, c.Mem[c.addrAbs()])
	case 0xDD: // CMP abs,X
		c.compare(c.A, c.Mem[c.addrAbsX()])
	case 0xD9: // CMP abs,Y
		c.compare(c.A, c.Mem[c.addrAbsY()])
	case 0xC1: // CMP (zp,X)
		c.compare(c.A, c.Mem[c.addrIndX()])
	case 0xD1: // CMP (zp),Y
		c.compare(c.A, c.Mem[c.addrIndY()])

	// CPX
	case 0xE0: // CPX #imm
		c.compare(c.X, c.Mem[c.PC])
		c.PC++
	case 0xE4: // CPX zp
		c.compare(c.X, c.Mem[c.addrZP()])
	case 0xEC: // CPX abs
		c.compare(c.X, c.Mem[c.addrAbs()])

	// CPY
	case 0xC0: // CPY #imm
		c.compare(c.Y, c.Mem[c.PC])
		c.PC++
	case 0xC4: // CPY zp
		c.compare(c.Y, c.Mem[c.addrZP()])
	case 0xCC: // CPY abs
		c.compare(c.Y, c.Mem[c.addrAbs()])

	// BIT
	case 0x24: // BIT zp
		v := c.Mem[c.addrZP()]
		c.setZ(c.A & v)
		c.P = c.P&^(FlagN|FlagV) | (v & (FlagN | FlagV))
	case 0x2C: // BIT abs
		v := c.Mem[c.addrAbs()]
		c.setZ(c.A & v)
		c.P = c.P&^(FlagN|FlagV) | (v & (FlagN | FlagV))

	// Branches
	case 0x10: // BPL
		c.branch(c.P&FlagN == 0)
	case 0x30: // BMI
		c.branch(c.P&FlagN != 0)
	case 0x50: // BVC
		c.branch(c.P&FlagV == 0)
	case 0x70: // BVS
		c.branch(c.P&FlagV != 0)
	case 0x90: // BCC
		c.branch(c.P&FlagC == 0)
	case 0xB0: // BCS
		c.branch(c.P&FlagC != 0)
	case 0xD0: // BNE
		c.branch(c.P&FlagZ == 0)
	case 0xF0: // BEQ
		c.branch(c.P&FlagZ != 0)

	// JMP
	case 0x4C: // JMP abs
		c.PC = c.addrAbs()
	case 0x6C: // JMP (abs)
		addr := c.addrAbs()
		// 6502 bug: wraps within page
		lo := uint16(c.Mem[addr])
		hi := uint16(c.Mem[(addr&0xFF00)|((addr+1)&0xFF)])
		c.PC = hi<<8 | lo

	// JSR/RTS
	case 0x20: // JSR abs
		addr := c.addrAbs()
		c.push16(c.PC - 1)
		c.PC = addr
	case 0x60: // RTS
		c.PC = c.pop16() + 1

	// RTI
	case 0x40: // RTI
		c.P = c.pop() | FlagU
		c.PC = c.pop16()

	// Flags
	case 0x18: // CLC
		c.CLCTotal[c.PC-1]++
		if c.P&FlagC == 0 {
			c.CLCRedundant[c.PC-1]++
		}
		c.P &^= FlagC
	case 0x38: // SEC
		c.SECTotal[c.PC-1]++
		if c.P&FlagC != 0 {
			c.SECRedundant[c.PC-1]++
		}
		c.P |= FlagC
	case 0x58: // CLI
		c.P &^= FlagI
	case 0x78: // SEI
		c.P |= FlagI
	case 0xB8: // CLV
		c.P &^= FlagV
	case 0xD8: // CLD
		c.P &^= FlagD
	case 0xF8: // SED
		c.P |= FlagD

	// NOP
	case 0xEA: // NOP
		// do nothing

	// BRK
	case 0x00: // BRK
		c.PC++
		c.push16(c.PC)
		c.push(c.P | FlagB | FlagU)
		c.P |= FlagI
		c.PC = c.read16(0xFFFE)
		c.Halted = true // Stop on BRK for testing

	default:
		return fmt.Errorf("unknown opcode $%02X at $%04X", opcode, c.PC-1)
	}

	return nil
}

func (c *CPU6502) adc(v byte) {
	carry := uint16(c.P & FlagC)
	sum := uint16(c.A) + uint16(v) + carry
	if sum > 0xFF {
		c.P |= FlagC
	} else {
		c.P &^= FlagC
	}
	// Overflow: sign of result differs from sign of both operands
	if (c.A^byte(sum))&(v^byte(sum))&0x80 != 0 {
		c.P |= FlagV
	} else {
		c.P &^= FlagV
	}
	c.A = byte(sum)
	c.setNZ(c.A)
}

func (c *CPU6502) sbc(v byte) {
	// SBC is ADC with complement
	c.adc(^v)
}

// Run executes until halted or breakpoint
func (c *CPU6502) Run(maxCycles uint64) error {
	for !c.Halted && c.Cycles < maxCycles {
		if err := c.Step(); err != nil {
			return err
		}
	}
	return nil
}

// LoadAt loads data into memory at the specified address
func (c *CPU6502) LoadAt(addr uint16, data []byte) {
	copy(c.Mem[addr:], data)
}

// DumpZP prints zero page for debugging
func (c *CPU6502) DumpZP() {
	fmt.Println("Zero Page:")
	for i := 0; i < 256; i += 16 {
		fmt.Printf("$%02X: ", i)
		for j := 0; j < 16; j++ {
			fmt.Printf("%02X ", c.Mem[i+j])
		}
		fmt.Println()
	}
}

// DumpRegs prints registers
func (c *CPU6502) DumpRegs() {
	fmt.Printf("A=%02X X=%02X Y=%02X SP=%02X PC=%04X P=%02X [%s]\n",
		c.A, c.X, c.Y, c.SP, c.PC, c.P, c.flagString())
}

func (c *CPU6502) flagString() string {
	flags := []byte("NV-BDIZC")
	for i := 0; i < 8; i++ {
		if c.P&(1<<(7-i)) == 0 {
			flags[i] = '-'
		}
	}
	return string(flags)
}

// Has100PctRedundantFlagOps returns true if any CLC/SEC are always redundant
func (c *CPU6502) Has100PctRedundantFlagOps() bool {
	for pc, total := range c.CLCTotal {
		if c.CLCRedundant[pc] == total {
			return true
		}
	}
	for pc, total := range c.SECTotal {
		if c.SECRedundant[pc] == total {
			return true
		}
	}
	return false
}
